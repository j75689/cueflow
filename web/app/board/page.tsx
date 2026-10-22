"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { type DragEvent, Suspense, useEffect, useMemo, useRef, useState } from "react";
import {
  AgentAvatar,
  AgentSelect,
  NewTaskModal,
  PriorityDot,
  TASK_STATUS_LABEL,
  useAgents,
} from "@/components/agents";
import {
  BoardList,
  CANCELED_COLUMN,
  COLUMNS,
  type Column,
  FilterChip,
  LIST_COLUMNS,
  sortFinished,
  sortTasks,
} from "@/components/board";
import { useServerEvent, useToast } from "@/components/providers";
import { Button, Checkbox, ErrorBox, Icon, Input, Loading, PageHeader, Pill, cx } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative } from "@/lib/format";
import { useAsync, useDebouncedCallback } from "@/lib/hooks";
import type { Agent, Task, TaskStatus } from "@/lib/types";

const DONE_WINDOW_MS = 14 * 24 * 3600 * 1000;
const STEP = 1024;

// Cards a column shows before "Show more"; finished columns grow every day.
const PAGE = 20;

function Board() {
  const router = useRouter();
  const params = useSearchParams();
  // List mode, its category and page live in the URL (?view=list&status=<column key>&page=N, no page
  // = 1); no view = kanban.
  const listMode = params.get("view") === "list";
  const category = LIST_COLUMNS.find((c) => c.key === params.get("status")) ?? COLUMNS[1];
  const page = Math.max(1, Math.floor(Number(params.get("page"))) || 1);
  const showList = (key: string, page = 1) =>
    router.replace(`/board/?view=list&status=${key}${page > 1 ? `&page=${page}` : ""}`, { scroll: false });
  const toast = useToast();
  const agents = useAgents();
  const [showCanceled, setShowCanceled] = useState(false);
  const [filterAgent, setFilterAgent] = useState<string | "none" | null>(null);
  const [q, setQ] = useState("");
  const [newFor, setNewFor] = useState<TaskStatus | null>(null);
  // List mode searches the server once typing pauses, not on every key; a new search or agent filter
  // starts it over at page 1. The search resets the page with the query it applies, so no page is
  // fetched with the old query.
  const [query, setQuery] = useState(q);
  useEffect(() => {
    if (q === query) return;
    const t = setTimeout(() => {
      setQuery(q);
      if (listMode && page > 1) showList(category.key);
    }, 300);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q, listMode, page, category.key]);
  const pickAgent = (a: string | "none" | null) => {
    setFilterAgent(a);
    if (listMode && page > 1) showList(category.key);
  };

  // List mode loads its own pages (BoardList); the board loads every task once.
  const tasks = useAsync(() => (listMode ? Promise.resolve(undefined) : api.listTasks({ all: true })), [listMode]);
  // A bulk action sends one event per card; one reload covers them all, and a steady stream still
  // reloads every second.
  const reload = useDebouncedCallback(() => {
    if (!listMode) void tasks.reload();
  }, 250, 1000);
  useServerEvent(["task.updated", "task.deleted"], reload);

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
    for (const c of columns) {
      const finished = c.key === "done" || c.key === "canceled";
      m[c.key] = filtered.filter((t) => c.statuses.includes(t.status)).sort(finished ? sortFinished : sortTasks);
    }
    return m;
  }, [filtered, columns]);

  // ---- drag & drop ----
  const [dragId, setDragId] = useState<string | null>(null);
  const [over, setOver] = useState<{ col: string; index: number } | null>(null);

  const dragged = dragId ? (tasks.data?.items ?? []).find((t) => t.id === dragId) : undefined;
  const boardRef = useRef<HTMLDivElement>(null);
  const [shown, setShown] = useState<Record<string, number>>({});

  // From md up the board fills the rest of the window and each column scrolls on its own, so a long
  // Done column doesn't stretch the page. Phones keep the normal page scroll.
  const [boardHeight, setBoardHeight] = useState<number | null>(null);
  useEffect(() => {
    const measure = () => {
      const el = boardRef.current;
      if (!el || !window.matchMedia("(min-width: 768px)").matches) return setBoardHeight(null);
      const top = el.getBoundingClientRect().top + window.scrollY;
      setBoardHeight(Math.max(360, window.innerHeight - top - 24));
    };
    measure();
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
    // Re-measure when the board first renders and when the filter chips (one per agent) may wrap.
  }, [tasks.data === undefined, showCanceled, agents.data?.length, listMode]);

  // Scroll the board sideways while a card is held near its left or right edge.
  function autoScroll(e: DragEvent) {
    const el = boardRef.current;
    if (!dragId || !el) return;
    const rect = el.getBoundingClientRect();
    const zone = 96;
    const left = e.clientX - rect.left;
    const right = rect.right - e.clientX;
    if (left < zone) el.scrollLeft -= Math.ceil((zone - left) / 4);
    else if (right < zone) el.scrollLeft += Math.ceil((zone - right) / 4);
  }

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
        subtitle={`Assign tasks to agents and track their work. ${listMode ? "Select cards to move or delete them." : "Drag cards between columns."}`}
        actions={
          <>
            <div className="inline-flex rounded-md border border-line-strong p-0.5" role="group" aria-label="View">
              {[
                { list: false, label: "Board" },
                { list: true, label: "List" },
              ].map((v) => (
                <button
                  key={v.label}
                  aria-pressed={listMode === v.list}
                  onClick={() => (v.list ? showList(category.key) : router.replace("/board/", { scroll: false }))}
                  className={cx(
                    "inline-flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium",
                    listMode === v.list ? "bg-inverse text-on-inverse" : "text-fg-2 hover:text-fg",
                  )}
                >
                  {v.label}
                </button>
              ))}
            </div>
            <Button variant="primary" onClick={() => setNewFor("TODO")}>
              <Icon name="plus" className="h-4 w-4" /> New task
            </Button>
          </>
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
          <FilterChip active={filterAgent === null} onClick={() => pickAgent(null)}>
            All
          </FilterChip>
          {agentList.map((a) => (
            <FilterChip key={a.id} active={filterAgent === a.id} onClick={() => pickAgent(filterAgent === a.id ? null : a.id)}>
              <AgentAvatar agent={a} size="xs" /> {a.name}
            </FilterChip>
          ))}
          <FilterChip active={filterAgent === "none"} onClick={() => pickAgent(filterAgent === "none" ? null : "none")}>
            Unassigned
          </FilterChip>
        </div>
        {!listMode && (
          <div className="ml-auto">
            <Checkbox checked={showCanceled} onChange={setShowCanceled} label="Show canceled" />
          </div>
        )}
      </div>

      {!listMode && tasks.error && (
        <div className="mb-3">
          <ErrorBox onRetry={tasks.reload}>{tasks.error}</ErrorBox>
        </div>
      )}
      {listMode ? (
        <BoardList
          category={category}
          page={page}
          onCategory={showList}
          onPage={(n) => showList(category.key, n)}
          q={query}
          assignee={filterAgent}
        />
      ) : tasks.loading && !tasks.data ? (
        <Loading />
      ) : (
        <div
          ref={boardRef}
          onDragOverCapture={autoScroll}
          className="-mx-4 flex flex-1 gap-3 overflow-x-auto px-4 pb-4 sm:-mx-6 sm:px-6 lg:-mx-8 lg:px-8"
          style={boardHeight ? { height: boardHeight, flex: "none" } : undefined}
        >
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
                  "flex min-w-[160px] max-w-[340px] flex-1 basis-0 flex-col rounded-xl border bg-sunken transition-colors",
                  dragId && droppable
                    ? isOver
                      ? "border-accent-line bg-accent-soft"
                      : "border-dashed border-line-strong"
                    : "border-line",
                  dragId && !droppable && "opacity-60",
                )}
              >
                {/* Every column reserves the same rows (title, hint, add) so the lists start at one height. */}
                <header className="flex items-start justify-between gap-2 px-3 pt-3 pb-2">
                  <div className="min-w-0 flex-1">
                    <h2 className="flex h-6 items-center gap-2 text-[13px] font-semibold text-fg">
                      <span className={cx("h-2 w-2 shrink-0 rounded-full", col.dot)} aria-hidden />
                      {col.label}
                      <span className="rounded-md bg-muted px-1.5 text-[11px] font-medium tabular-nums text-fg-2">
                        {list.length}
                      </span>
                    </h2>
                    <p className="h-4 truncate text-[11px] leading-4 text-fg-3">{col.hint ?? "\u00a0"}</p>
                  </div>
                  {col.addable && (
                    <button
                      onClick={() => setNewFor(col.drop)}
                      className="rounded p-1 text-fg-3 hover:bg-muted hover:text-fg"
                      title={`Add to ${col.label}`}
                    >
                      <Icon name="plus" className="h-4 w-4" />
                    </button>
                  )}
                </header>
                {col.addable ? (
                  <InlineAdd status={col.drop as TaskStatus} agents={agentList} onCreated={() => void tasks.reload()} />
                ) : (
                  <div className="mx-2 mb-2 h-6" aria-hidden />
                )}
                <div className="flex min-h-[80px] flex-1 flex-col gap-2 overflow-y-auto px-2 pb-3 md:min-h-0">
                  {list.slice(0, shown[col.key] ?? PAGE).map((t, i) => (
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
                  {list.length > (shown[col.key] ?? PAGE) && (
                    <button
                      onClick={() => setShown({ ...shown, [col.key]: (shown[col.key] ?? PAGE) + PAGE })}
                      className="rounded-md px-2 py-1.5 text-xs text-fg-3 hover:bg-subtle hover:text-fg"
                    >
                      Show {Math.min(PAGE, list.length - (shown[col.key] ?? PAGE))} more of{" "}
                      {list.length - (shown[col.key] ?? PAGE)}
                    </button>
                  )}
                  {list.length === 0 && !isOver && (
                    <p className="px-2 py-6 text-center text-xs text-fg-4">
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

export default function BoardPage() {
  return (
    <Suspense fallback={<Loading />}>
      <Board />
    </Suspense>
  );
}

function DropLine() {
  return <div className="my-1 h-0.5 rounded bg-indigo-500" />;
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
        "group cursor-pointer rounded-lg border bg-surface p-2.5 shadow-card transition-[border-color,box-shadow,opacity] duration-150 hover:border-line-strong hover:shadow-pop focus-visible:outline-2 focus-visible:outline-accent-solid",
        needsYou ? "border-amber-400/70 ring-2 ring-amber-400/15 dark:border-amber-500/50" : "border-line",
        dragging && "opacity-40",
      )}
    >
      <div className="mb-1 flex items-center gap-2 text-[11px] text-fg-3">
        <span className="font-mono">#{t.number}</span>
        {(t.priority === "high" || t.priority === "urgent") && <PriorityDot priority={t.priority} withLabel />}
        {t.running && (
          <span className="inline-flex items-center gap-1 text-accent">
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
      <p className="line-clamp-3 text-sm leading-snug text-fg">{t.title}</p>
      {(t.labels?.length ?? 0) > 0 && (
        <div className="mt-1.5 flex flex-wrap gap-1">
          {t.labels!.slice(0, 4).map((l) => (
            <Pill key={l}>{l}</Pill>
          ))}
        </div>
      )}
      <div className="mt-2 flex flex-wrap items-center justify-between gap-x-2 gap-y-1">
        <span className="flex max-w-full items-center gap-1.5 text-xs text-fg-2">
          <AgentAvatar agent={t.assignee} size="xs" />
          <span className="truncate">{t.assignee?.name ?? "Unassigned"}</span>
        </span>
        <span className="flex shrink-0 items-center gap-2 text-[11px] text-fg-3">
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
        className="mx-2 mb-2 h-6 rounded-md px-2 text-left text-xs leading-6 text-fg-3 hover:bg-subtle hover:text-fg"
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
      className="mx-2 mb-2 space-y-1.5 rounded-md border border-line bg-surface p-2"
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
