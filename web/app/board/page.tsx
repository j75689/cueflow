"use client";

import { useRouter } from "next/navigation";
import { type DragEvent, type ReactNode, useMemo, useState } from "react";
import {
  AgentAvatar,
  AgentSelect,
  NewTaskModal,
  PriorityDot,
  TASK_STATUS_LABEL,
  useAgents,
} from "@/components/agents";
import { useServerEvent, useToast } from "@/components/providers";
import { Button, Checkbox, ErrorBox, Icon, Input, Loading, PageHeader, Pill, cx } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative } from "@/lib/format";
import { useAsync } from "@/lib/hooks";
import type { Agent, Task, TaskStatus } from "@/lib/types";

interface Column {
  key: string;
  label: string;
  statuses: TaskStatus[];
  // Status assigned when a card is dropped here from another column; null = not a drop target.
  drop: TaskStatus | null;
  hint?: string;
  addable?: boolean;
}

const COLUMNS: Column[] = [
  { key: "backlog", label: "Backlog", statuses: ["BACKLOG"], drop: "BACKLOG", addable: true },
  { key: "todo", label: "To do", statuses: ["TODO"], drop: "TODO", addable: true, hint: "Assigned agents start automatically" },
  { key: "progress", label: "In progress", statuses: ["IN_PROGRESS"], drop: null, hint: "Agents move tasks here" },
  { key: "waiting", label: "Waiting", statuses: ["WAITING_HUMAN", "BLOCKED"], drop: null, hint: "Needs you, or waiting on subtasks" },
  { key: "review", label: "Review", statuses: ["REVIEW"], drop: "REVIEW" },
  { key: "done", label: "Done", statuses: ["DONE"], drop: "DONE" },
];
const CANCELED_COLUMN: Column = { key: "canceled", label: "Canceled", statuses: ["CANCELED"], drop: "CANCELED" };

const DONE_WINDOW_MS = 14 * 24 * 3600 * 1000;
const STEP = 1024;

function sortTasks(a: Task, b: Task) {
  if (a.position !== b.position) return a.position - b.position;
  return a.created_at.localeCompare(b.created_at);
}

export default function BoardPage() {
  const router = useRouter();
  const toast = useToast();
  const agents = useAgents();
  const [showCanceled, setShowCanceled] = useState(false);
  const [filterAgent, setFilterAgent] = useState<string | "none" | null>(null);
  const [q, setQ] = useState("");
  const [newFor, setNewFor] = useState<TaskStatus | null>(null);

  const tasks = useAsync(() => api.listTasks({ all: true }), []);
  useServerEvent(["task.updated", "task.deleted"], () => void tasks.reload());

  const columns = showCanceled ? [...COLUMNS, CANCELED_COLUMN] : COLUMNS;

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const now = Date.now();
    return (tasks.data?.items ?? []).filter((t) => {
      if ((t.status === "DONE" || t.status === "CANCELED") && t.completed_at) {
        if (now - new Date(t.completed_at).getTime() > DONE_WINDOW_MS) return false;
      }
      if (filterAgent === "none" && t.assignee_agent_id) return false;
      if (filterAgent && filterAgent !== "none" && t.assignee_agent_id !== filterAgent) return false;
      if (needle && !t.title.toLowerCase().includes(needle) && `#${t.number}` !== needle && String(t.number) !== needle)
        return false;
      return true;
    });
  }, [tasks.data, q, filterAgent]);

  const byColumn = useMemo(() => {
    const m: Record<string, Task[]> = {};
    for (const c of columns) m[c.key] = filtered.filter((t) => c.statuses.includes(t.status)).sort(sortTasks);
    return m;
  }, [filtered, columns]);

  // ---- drag & drop ----
  const [dragId, setDragId] = useState<string | null>(null);
  const [over, setOver] = useState<{ col: string; index: number } | null>(null);

  const dragged = dragId ? (tasks.data?.items ?? []).find((t) => t.id === dragId) : undefined;

  function canDrop(col: Column, t: Task | undefined): boolean {
    if (!t) return false;
    if (col.statuses.includes(t.status)) return true; // reorder within column
    return col.drop !== null && !t.running;
  }

  function onDragOverCard(e: DragEvent, col: Column, index: number) {
    if (!canDrop(col, dragged)) return;
    e.preventDefault();
    e.stopPropagation();
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
    const after = e.clientY > rect.top + rect.height / 2;
    const idx = after ? index + 1 : index;
    if (!over || over.col !== col.key || over.index !== idx) setOver({ col: col.key, index: idx });
  }

  function onDragOverColumn(e: DragEvent, col: Column) {
    if (!canDrop(col, dragged)) return;
    e.preventDefault();
    const len = byColumn[col.key]?.length ?? 0;
    if (!over || over.col !== col.key) setOver({ col: col.key, index: len });
  }

  async function onDrop(e: DragEvent, col: Column) {
    e.preventDefault();
    const t = dragged;
    const target = over;
    setDragId(null);
    setOver(null);
    if (!t || !target || target.col !== col.key || !canDrop(col, t)) return;

    const list = (byColumn[col.key] ?? []).filter((x) => x.id !== t.id);
    // index in the list without the dragged card
    const origIndex = (byColumn[col.key] ?? []).findIndex((x) => x.id === t.id);
    let index = target.index;
    if (origIndex !== -1 && origIndex < index) index -= 1;
    const prev = list[index - 1];
    const next = list[index];
    let position: number;
    if (prev && next) position = (prev.position + next.position) / 2;
    else if (prev) position = prev.position + STEP;
    else if (next) position = next.position - STEP;
    else position = STEP;

    const sameColumn = col.statuses.includes(t.status);
    const status = sameColumn ? undefined : (col.drop as TaskStatus);
    if (sameColumn && origIndex === index) return;

    // optimistic update
    if (tasks.data) {
      tasks.setData({
        items: tasks.data.items.map((x) => (x.id === t.id ? { ...x, position, status: status ?? x.status } : x)),
      });
    }
    try {
      await api.updateTask(t.id, status ? { status, position } : { position });
      if (status === "TODO" && t.assignee) toast.info(`${t.assignee.name} will pick up #${t.number}`);
    } catch (err) {
      toast.error(err);
    } finally {
      void tasks.reload();
    }
  }

  const agentList = (agents.data ?? []).filter((a) => a.is_active);

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title="Board"
        subtitle="Assign tasks to agents and track their work. Drag cards between columns."
        actions={
          <Button variant="primary" onClick={() => setNewFor("TODO")}>
            <Icon name="plus" className="h-4 w-4" /> New task
          </Button>
        }
      />

      <div className="mb-4 flex flex-wrap items-center gap-3">
        <Input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search tasks or #number…"
          className="w-full max-w-xs"
        />
        <div className="flex flex-wrap items-center gap-1.5">
          <FilterChip active={filterAgent === null} onClick={() => setFilterAgent(null)}>
            All
          </FilterChip>
          {agentList.map((a) => (
            <FilterChip key={a.id} active={filterAgent === a.id} onClick={() => setFilterAgent(filterAgent === a.id ? null : a.id)}>
              <AgentAvatar agent={a} size="xs" /> {a.name}
            </FilterChip>
          ))}
          <FilterChip active={filterAgent === "none"} onClick={() => setFilterAgent(filterAgent === "none" ? null : "none")}>
            Unassigned
          </FilterChip>
        </div>
        <div className="ml-auto">
          <Checkbox checked={showCanceled} onChange={setShowCanceled} label="Show canceled" />
        </div>
      </div>

      {tasks.error && (
        <div className="mb-3">
          <ErrorBox onRetry={tasks.reload}>{tasks.error}</ErrorBox>
        </div>
      )}
      {tasks.loading && !tasks.data ? (
        <Loading />
      ) : (
        <div className="-mx-4 flex flex-1 gap-3 overflow-x-auto px-4 pb-4 sm:-mx-6 sm:px-6 lg:-mx-8 lg:px-8">
          {columns.map((col) => {
            const list = byColumn[col.key] ?? [];
            const droppable = canDrop(col, dragged);
            const isOver = over?.col === col.key;
            return (
              <section
                key={col.key}
                onDragOver={(e) => onDragOverColumn(e, col)}
                onDrop={(e) => onDrop(e, col)}
                onDragLeave={(e) => {
                  if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) setOver(null);
                }}
                className={cx(
                  "flex w-[300px] shrink-0 flex-col rounded-lg border bg-zinc-50 transition-colors dark:bg-zinc-900/50",
                  dragId && droppable
                    ? isOver
                      ? "border-indigo-400 bg-indigo-50/50 dark:border-indigo-500/60 dark:bg-indigo-500/5"
                      : "border-dashed border-zinc-300 dark:border-zinc-700"
                    : "border-zinc-200 dark:border-zinc-800",
                  dragId && !droppable && "opacity-60",
                )}
              >
                <header className="flex items-center justify-between gap-2 px-3 pt-3 pb-2">
                  <div className="min-w-0">
                    <h2 className="flex items-center gap-2 text-sm font-semibold text-zinc-800 dark:text-zinc-200">
                      {col.label}
                      <span className="rounded bg-zinc-200 px-1.5 text-[11px] font-medium text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400">
                        {list.length}
                      </span>
                    </h2>
                    {col.hint && <p className="truncate text-[11px] text-zinc-500">{col.hint}</p>}
                  </div>
                  {col.addable && (
                    <button
                      onClick={() => setNewFor(col.drop)}
                      className="rounded p-1 text-zinc-500 hover:bg-zinc-200 hover:text-zinc-800 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
                      title={`Add to ${col.label}`}
                    >
                      <Icon name="plus" className="h-4 w-4" />
                    </button>
                  )}
                </header>
                {col.addable && <InlineAdd status={col.drop as TaskStatus} agents={agentList} onCreated={() => void tasks.reload()} />}
                <div className="flex min-h-[80px] flex-1 flex-col gap-2 px-2 pb-3">
                  {list.map((t, i) => (
                    <div key={t.id}>
                      {isOver && over?.index === i && dragId !== t.id && <DropLine />}
                      <TaskCard
                        task={t}
                        dragging={dragId === t.id}
                        onDragStart={(e) => {
                          e.dataTransfer.effectAllowed = "move";
                          e.dataTransfer.setData("text/plain", t.id);
                          setDragId(t.id);
                        }}
                        onDragEnd={() => {
                          setDragId(null);
                          setOver(null);
                        }}
                        onDragOver={(e) => onDragOverCard(e, col, i)}
                        onOpen={() => router.push(`/tasks/view/?id=${t.id}`)}
                      />
                    </div>
                  ))}
                  {isOver && over?.index === list.length && <DropLine />}
                  {list.length === 0 && !isOver && (
                    <p className="px-2 py-6 text-center text-xs text-zinc-400">
                      {col.key === "waiting" ? "Nothing waiting on you" : "No tasks"}
                    </p>
                  )}
                </div>
              </section>
            );
          })}
        </div>
      )}

      <NewTaskModal
        open={newFor !== null}
        onClose={() => setNewFor(null)}
        agents={agentList}
        defaultStatus={newFor === "BACKLOG" ? "BACKLOG" : undefined}
        onCreated={() => void tasks.reload()}
      />
    </div>
  );
}

function DropLine() {
  return <div className="my-1 h-0.5 rounded bg-indigo-500" />;
}

function FilterChip({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      onClick={onClick}
      className={cx(
        "inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs transition-colors",
        active
          ? "border-indigo-300 bg-indigo-50 text-indigo-800 dark:border-indigo-500/50 dark:bg-indigo-500/10 dark:text-indigo-200"
          : "border-zinc-200 bg-white text-zinc-600 hover:bg-zinc-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-800",
      )}
    >
      {children}
    </button>
  );
}

function TaskCard({
  task: t,
  dragging,
  onDragStart,
  onDragEnd,
  onDragOver,
  onOpen,
}: {
  task: Task;
  dragging: boolean;
  onDragStart: (e: DragEvent) => void;
  onDragEnd: () => void;
  onDragOver: (e: DragEvent) => void;
  onOpen: () => void;
}) {
  const needsYou = t.status === "WAITING_HUMAN";
  const sub = t.subtask_counts;
  return (
    <article
      draggable
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      onDragOver={onDragOver}
      onClick={onOpen}
      onKeyDown={(e) => e.key === "Enter" && onOpen()}
      tabIndex={0}
      className={cx(
        "group cursor-pointer rounded-md border bg-white p-2.5 shadow-sm transition-all hover:border-zinc-300 hover:shadow focus-visible:outline-2 focus-visible:outline-indigo-500 dark:bg-zinc-900 dark:hover:border-zinc-600",
        needsYou ? "border-amber-300 ring-1 ring-amber-200 dark:border-amber-600/60 dark:ring-amber-900/50" : "border-zinc-200 dark:border-zinc-800",
        dragging && "opacity-40",
      )}
    >
      <div className="mb-1 flex items-center gap-2 text-[11px] text-zinc-500">
        <span className="font-mono">#{t.number}</span>
        <PriorityDot priority={t.priority} />
        {t.running && (
          <span className="inline-flex items-center gap-1 text-indigo-600 dark:text-indigo-400">
            <span className="relative flex h-2 w-2">
              <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-indigo-400 opacity-75" />
              <span className="relative inline-flex h-2 w-2 rounded-full bg-indigo-500" />
            </span>
            working
          </span>
        )}
        {needsYou && <span className="font-medium text-amber-700 dark:text-amber-400">needs you</span>}
        {t.status === "BLOCKED" && <span className="text-violet-600 dark:text-violet-400">{TASK_STATUS_LABEL.BLOCKED.toLowerCase()}</span>}
        {t.parent_id && <span title="Subtask">↳</span>}
      </div>
      <p className="line-clamp-3 text-sm leading-snug text-zinc-900 dark:text-zinc-100">{t.title}</p>
      {(t.labels?.length ?? 0) > 0 && (
        <div className="mt-1.5 flex flex-wrap gap-1">
          {t.labels!.slice(0, 4).map((l) => (
            <Pill key={l}>{l}</Pill>
          ))}
        </div>
      )}
      <div className="mt-2 flex items-center justify-between gap-2">
        <span className="flex min-w-0 items-center gap-1.5 text-xs text-zinc-600 dark:text-zinc-400">
          <AgentAvatar agent={t.assignee} size="xs" />
          <span className="truncate">{t.assignee?.name ?? "Unassigned"}</span>
        </span>
        <span className="flex shrink-0 items-center gap-2 text-[11px] text-zinc-500">
          {sub && sub.total > 0 && (
            <span title="Subtasks done" className="inline-flex items-center gap-1">
              <Icon name="list" className="h-3 w-3" />
              {sub.done}/{sub.total}
            </span>
          )}
          <span>{fmtRelative(t.updated_at)}</span>
        </span>
      </div>
    </article>
  );
}

function InlineAdd({ status, agents, onCreated }: { status: TaskStatus; agents: Agent[]; onCreated: () => void }) {
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [assignee, setAssignee] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (!open) {
    return (
      <button
        onClick={() => setOpen(true)}
        className="mx-2 mb-2 rounded-md px-2 py-1 text-left text-xs text-zinc-500 hover:bg-zinc-200/60 hover:text-zinc-800 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
      >
        + Add task
      </button>
    );
  }
  const submit = async () => {
    if (!title.trim()) return;
    setBusy(true);
    try {
      await api.createTask({ title: title.trim(), assignee_agent_id: assignee, status });
      setTitle("");
      onCreated();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      className="mx-2 mb-2 space-y-1.5 rounded-md border border-zinc-200 bg-white p-2 dark:border-zinc-700 dark:bg-zinc-900"
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <Input
        autoFocus
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && setOpen(false)}
        placeholder="Task title"
        className="h-8 text-xs"
      />
      <AgentSelect agents={agents} value={assignee} onChange={setAssignee} className="h-8 py-1 text-xs" />
      <div className="flex justify-end gap-1.5">
        <Button size="sm" variant="ghost" onClick={() => setOpen(false)}>
          Cancel
        </Button>
        <Button size="sm" variant="primary" type="submit" loading={busy} disabled={!title.trim()}>
          Add
        </Button>
      </div>
    </form>
  );
}
