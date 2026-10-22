"use client";

import Link from "next/link";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { AgentAvatar, PriorityDot, TASK_STATUS_LABEL } from "@/components/agents";
import { useDialog } from "@/components/dialog";
import { useToast } from "@/components/providers";
import { Checkbox, Select, cx } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative, plural } from "@/lib/format";
import type { Task, TaskStatus } from "@/lib/types";

export interface Column {
  key: string;
  label: string;
  statuses: TaskStatus[];
  // Status assigned when a card is dropped here from another column; null = not a drop target.
  drop: TaskStatus | null;
  hint?: string;
  addable?: boolean;
  // Status color of the column's dot, matching the task status badges.
  dot: string;
}

export const COLUMNS: Column[] = [
  { key: "backlog", label: "Backlog", statuses: ["BACKLOG"], drop: "BACKLOG", addable: true, dot: "bg-fg-4" },
  { key: "todo", label: "To do", statuses: ["TODO"], drop: "TODO", addable: true, hint: "Assigned agents start automatically", dot: "bg-sky-500" },
  { key: "progress", label: "In progress", statuses: ["IN_PROGRESS"], drop: null, hint: "Agents move tasks here", dot: "bg-indigo-500" },
  { key: "waiting", label: "Waiting", statuses: ["WAITING_HUMAN", "BLOCKED"], drop: null, hint: "Needs you, or waiting on subtasks", dot: "bg-amber-500" },
  { key: "review", label: "Review", statuses: ["REVIEW"], drop: "REVIEW", dot: "bg-teal-500" },
  { key: "done", label: "Done", statuses: ["DONE"], drop: "DONE", dot: "bg-emerald-500" },
];
export const CANCELED_COLUMN: Column = { key: "canceled", label: "Canceled", statuses: ["CANCELED"], drop: "CANCELED", dot: "bg-fg-4" };

// List mode always offers every category, Canceled included.
export const LIST_COLUMNS: Column[] = [...COLUMNS, CANCELED_COLUMN];

export function sortTasks(a: Task, b: Task) {
  if (a.position !== b.position) return a.position - b.position;
  return a.created_at.localeCompare(b.created_at);
}

// Finished columns read like a log: most recently finished first.
export function sortFinished(a: Task, b: Task) {
  return (b.completed_at ?? b.updated_at).localeCompare(a.completed_at ?? a.updated_at);
}

export function FilterChip({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      onClick={onClick}
      aria-pressed={active}
      className={cx(
        "inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs transition-colors",
        active
          ? "border-accent-line bg-accent-soft text-accent"
          : "border-line bg-surface text-fg-2 hover:bg-subtle",
      )}
    >
      {children}
    </button>
  );
}

// subtasksUnder counts the tasks a delete of chosen takes with it. A delete removes a task's whole
// subtree, so it walks parent_id over all (the board's unfiltered list) and counts every descendant
// once; a chosen card under another chosen card is already in the chosen count. all stops at the
// server's row limit, so a parent with more subtasks than all holds adds the missing children and
// marks the count partial: their own subtasks can't be seen.
export function subtasksUnder(chosen: Task[], all: Task[]): { count: number; partial: boolean } {
  const children = new Map<string, Task[]>();
  for (const t of all) if (t.parent_id) children.set(t.parent_id, [...(children.get(t.parent_id) ?? []), t]);
  const seen = new Set(chosen.map((t) => t.id));
  const queue = [...chosen];
  let count = 0;
  let partial = false;
  for (const t of queue) {
    const kids = children.get(t.id) ?? [];
    const missing = (t.subtask_counts?.total ?? 0) - kids.length;
    if (missing > 0) {
      count += missing;
      partial = true;
    }
    for (const c of kids) {
      if (seen.has(c.id)) continue;
      seen.add(c.id);
      count++;
      queue.push(c);
    }
  }
  return { count, partial };
}

// BoardList is the board's list mode: one category's cards as rows, ticked ones moved or deleted
// with one bulk request. tasks is the board's filtered list (agent, search); all is every task the
// board loaded, for counting what a delete takes with it.
export function BoardList({
  tasks,
  all,
  category,
  onCategory,
  onChanged,
}: {
  tasks: Task[];
  all: Task[];
  category: Column;
  onCategory: (key: string) => void;
  onChanged: () => void;
}) {
  const toast = useToast();
  const dialog = useDialog();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState<"move" | "delete" | null>(null);

  const rows = useMemo(() => {
    const finished = category.key === "done" || category.key === "canceled";
    return tasks.filter((t) => category.statuses.includes(t.status)).sort(finished ? sortFinished : sortTasks);
  }, [tasks, category]);

  // A new category starts with nothing ticked.
  useEffect(() => setSelected(new Set()), [category.key]);
  // A filter, a search or a live update that drops a row drops it from the selection too.
  useEffect(() => {
    setSelected((cur) => {
      const keep = new Set(rows.filter((t) => cur.has(t.id)).map((t) => t.id));
      return keep.size === cur.size ? cur : keep;
    });
  }, [rows]);

  const chosen = rows.filter((t) => selected.has(t.id));
  const running = chosen.filter((t) => t.running).length;
  const toggleSelect = (id: string, on: boolean) =>
    setSelected((cur) => {
      const next = new Set(cur);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });

  // bulk sends the chosen tasks to the server in one request and reports what failed and why. The
  // ones that failed stay selected, so retrying them is one click.
  const bulk = async (action: "move" | "delete", status?: TaskStatus) => {
    setBusy(action);
    try {
      const res = await api.bulkTasks({ action, ids: chosen.map((t) => t.id), status });
      // A delete that finds the task gone is what was asked: a subtask deleted with its selected
      // parent, or one deleted elsewhere meanwhile.
      const failed = action === "delete" ? res.failed.filter((f) => f.code !== "not_found") : res.failed;
      const done = res.ok.length + res.failed.length - failed.length;
      const number = Object.fromEntries(chosen.map((t) => [t.id, `#${t.number}`]));
      setSelected(new Set(failed.map((f) => f.id)));
      if (done) {
        const to = LIST_COLUMNS.find((c) => c.drop === status)?.label;
        toast.success(action === "delete" ? `Deleted ${plural(done, "task")}` : `Moved ${plural(done, "task")} to ${to}`);
      }
      if (failed.length) {
        // A few are named; a long list is summarised, the kept selection shows which they are.
        const shown = failed.slice(0, 3).map((f) => `${number[f.id] ?? f.id}: ${f.message}`);
        const more = failed.length - shown.length;
        toast.error(
          `Could not ${action} ${plural(failed.length, "task")} (still selected): ${shown.join("; ")}${more > 0 ? `; and ${more} more` : ""}`,
        );
      }
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
      onChanged();
    }
  };

  const move = async (status: TaskStatus) => {
    // Moving an assigned card to To do starts its agent, so a batch of them asks first.
    const assigned = chosen.filter((t) => t.assignee_agent_id).length;
    if (
      status === "TODO" &&
      assigned > 0 &&
      !(await dialog.confirm({
        title: `Move ${plural(chosen.length, "task")} to To do?`,
        message: `${plural(assigned, "assigned agent")} ${assigned === 1 ? "is" : "are"} queued to start working.`,
        confirmLabel: "Move to To do",
      }))
    )
      return;
    await bulk("move", status);
  };

  const remove = async () => {
    const { count: subtasks, partial } = subtasksUnder(chosen, all);
    let message = "Their conversations and files are deleted too.";
    if (subtasks > 0)
      message += ` ${partial ? "At least " : ""}${plural(subtasks, "subtask")} under them ${subtasks === 1 ? "is" : "are"} deleted as well.`;
    if (running > 0) message += ` ${plural(running, "running agent")} ${running === 1 ? "is" : "are"} stopped.`;
    if (
      !(await dialog.confirm({
        title: `Delete ${plural(chosen.length, "task")}?`,
        message,
        confirmLabel: `Delete ${plural(chosen.length, "task")}`,
        tone: "danger",
      }))
    )
      return;
    await bulk("delete");
  };

  const targets = LIST_COLUMNS.filter((c) => c.drop !== null && c.key !== category.key);

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center gap-1.5" role="group" aria-label="Category">
        {LIST_COLUMNS.map((c) => (
          <FilterChip key={c.key} active={c.key === category.key} onClick={() => onCategory(c.key)}>
            <span className={cx("h-2 w-2 shrink-0 rounded-full", c.dot)} aria-hidden />
            {c.label}
            <span className="tabular-nums text-fg-3">{tasks.filter((t) => c.statuses.includes(t.status)).length}</span>
          </FilterChip>
        ))}
      </div>

      {/* top-12 clears the shell's mobile header (h-12), which is sticky in the same scroll flow. */}
      {rows.length > 0 && (
        <div
          role="toolbar"
          aria-label="Bulk actions"
          className="sticky top-12 z-20 mb-2 flex min-h-11 flex-wrap items-center gap-3 rounded-lg border border-line bg-surface/95 px-3 py-1.5 backdrop-blur md:top-0"
        >
          <Checkbox
            checked={chosen.length > 0 && chosen.length === rows.length}
            onChange={(on) => setSelected(on ? new Set(rows.map((t) => t.id)) : new Set())}
            label={<span aria-live="polite">{chosen.length ? `${chosen.length} selected` : "Select all"}</span>}
            disabled={!!busy}
          />
          {chosen.length > 0 && (
            <div className="ml-auto flex flex-wrap items-center gap-1.5">
              {running > 0 && <span className="text-xs text-fg-3">Running cards can&apos;t be moved; stop them first</span>}
              <div className="w-40">
                <Select
                  aria-label="Move selected to"
                  placeholder="Move to…"
                  value=""
                  disabled={!!busy || running > 0}
                  onChange={(e) => void move(e.target.value as TaskStatus)}
                  className="h-8 py-1 text-xs"
                >
                  {targets.map((c) => (
                    <option key={c.key} value={c.drop!}>
                      {c.label}
                    </option>
                  ))}
                </Select>
              </div>
              <button
                onClick={() => void remove()}
                disabled={!!busy}
                className="inline-flex h-8 items-center rounded-md px-2.5 text-xs font-medium text-red-600 hover:bg-red-500/10 disabled:opacity-50 dark:text-red-400"
              >
                Delete {chosen.length}
              </button>
            </div>
          )}
        </div>
      )}

      {rows.length === 0 ? (
        <p className="rounded-lg border border-dashed border-line-strong px-2 py-10 text-center text-xs text-fg-4">
          {category.key === "waiting" ? "Nothing waiting on you" : "No tasks"}
        </p>
      ) : (
        <ul
          className="divide-y divide-line overflow-hidden rounded-lg border border-line bg-surface"
          onKeyDown={(e) => e.key === "Escape" && setSelected(new Set())}
        >
          {rows.map((t) => {
            const sub = t.subtask_counts;
            return (
              <li
                key={t.id}
                className={cx(
                  "flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2",
                  selected.has(t.id) && "bg-accent-soft",
                )}
              >
                <Checkbox
                  checked={selected.has(t.id)}
                  onChange={(on) => toggleSelect(t.id, on)}
                  label={<span className="sr-only">Select #{t.number} {t.title}</span>}
                  disabled={!!busy}
                />
                <span className="font-mono text-[11px] text-fg-3">#{t.number}</span>
                <div className="flex min-w-0 flex-1 basis-48 items-center gap-2">
                  {t.parent_id && <span className="text-sm text-fg-3" title="Subtask">↳</span>}
                  <Link href={`/tasks/view/?id=${t.id}`} className="truncate text-sm text-fg hover:text-accent">
                    {t.title}
                  </Link>
                  {t.status === "WAITING_HUMAN" && (
                    <span className="shrink-0 text-[11px] font-medium text-amber-700 dark:text-amber-400">needs you</span>
                  )}
                  {t.status === "BLOCKED" && (
                    <span className="shrink-0 text-[11px] text-violet-600 dark:text-violet-400">
                      {TASK_STATUS_LABEL.BLOCKED.toLowerCase()}
                    </span>
                  )}
                  {t.running && (
                    <span className="inline-flex shrink-0 items-center gap-1 text-[11px] text-accent">
                      <span className="h-2 w-2 rounded-full bg-indigo-500" />
                      working
                    </span>
                  )}
                </div>
                <span className="flex items-center gap-3 text-[11px] text-fg-3">
                  <PriorityDot priority={t.priority} withLabel />
                  <span className="flex w-32 items-center gap-1.5 text-xs text-fg-2">
                    <AgentAvatar agent={t.assignee} size="xs" />
                    <span className="truncate">{t.assignee?.name ?? "Unassigned"}</span>
                  </span>
                  <span className="w-10 tabular-nums" title="Subtasks done">
                    {sub && sub.total > 0 ? `${sub.done}/${sub.total}` : ""}
                  </span>
                  <span className="w-16 text-right" title={t.updated_at}>
                    {fmtRelative(t.updated_at)}
                  </span>
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
