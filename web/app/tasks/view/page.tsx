"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import {
  Suspense,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  ALL_TASK_STATUSES,
  AgentAvatar,
  AgentSelect,
  NewTaskModal,
  PRIORITIES,
  PriorityDot,
  TASK_STATUS_LABEL,
  TaskStatusBadge,
  useAgents,
} from "@/components/agents";
import { RunStatusBadge, StatusBadge } from "@/components/badges";
import { TagInput } from "@/components/inputs";
import { useDialog } from "@/components/dialog";
import { useOrgFollow } from "@/components/org";
import { PublishTarget } from "@/components/publish-target";
import { useServerEvent, useToast } from "@/components/providers";
import {
  Button,
  Card,
  CardHeader,
  DateTimePicker,
  ErrorBox,
  Field,
  Icon,
  Input,
  Loading,
  Pill,
  Select,
  Textarea,
  cx,
} from "@/components/ui";
import { api } from "@/lib/api";
import {
  fmtRelative,
  fmtTime,
  fromLocalInput,
  toLocalInput,
} from "@/lib/format";
import { useAsync } from "@/lib/hooks";
import type {
  Agent,
  TaskDetail,
  TaskMessage,
  ToolApproval,
  TaskPatch,
  TaskPriority,
  TaskStatus,
} from "@/lib/types";

function TaskView() {
  const params = useSearchParams();
  const id = params.get("id") ?? "";
  const router = useRouter();
  const toast = useToast();
  const dialog = useDialog();
  const agents = useAgents();

  const task = useAsync(() => api.getTask(id), [id]);
  const messages = useAsync(() => api.taskMessages(id), [id]);
  const runs = useAsync(() => api.taskRuns(id), [id]);
  useOrgFollow(task.data?.org_id);

  useServerEvent("task.message", (d) => {
    if (d.task_id === id) void messages.reload();
  });
  useServerEvent(["task.updated", "task.deleted"], (d) => {
    const t = task.data;
    if (!t) return;
    const related =
      d.id === id ||
      d.id === t.parent_id ||
      (t.subtasks ?? []).some((s) => s.id === d.id);
    if (!related) return;
    void task.reload();
    if (d.id === id) void runs.reload();
  });

  const patch = async (body: TaskPatch, okMsg?: string) => {
    try {
      const updated = await api.updateTask(id, body);
      if (task.data) task.setData({ ...task.data, ...updated });
      if (okMsg) toast.success(okMsg);
    } catch (e) {
      toast.error(e);
      void task.reload();
    }
  };

  if (!id) return <ErrorBox>Missing task id.</ErrorBox>;
  if (task.loading && !task.data) return <Loading />;
  if (task.error && !task.data)
    return <ErrorBox onRetry={task.reload}>{task.error}</ErrorBox>;
  const t = task.data!;

  const deleteTask = async () => {
    if (
      !(await dialog.confirm({
        title: `Delete task #${t.number}?`,
        message:
          (t.subtasks?.length ?? 0) > 0
            ? `Its ${t.subtasks?.length} subtask(s) and the whole conversation are deleted too. A running agent is stopped.`
            : "The conversation is deleted too. A running agent is stopped.",
        confirmLabel: "Delete task",
        tone: "danger",
      }))
    )
      return;
    try {
      await api.deleteTask(id);
      toast.success("Task deleted");
      router.push("/board/");
    } catch (e) {
      toast.error(e);
    }
  };

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center gap-2 text-sm text-zinc-500">
        <Link
          href="/board/"
          className="hover:text-zinc-800 dark:hover:text-zinc-200"
        >
          Board
        </Link>
        <span>/</span>
        {t.parent && (
          <>
            <Link
              href={`/tasks/view/?id=${t.parent.id}`}
              className="hover:text-zinc-800 dark:hover:text-zinc-200"
            >
              #{t.parent.number}
            </Link>
            <span>/</span>
          </>
        )}
        <span className="font-mono">#{t.number}</span>
        <TaskStatusBadge status={t.status} running={t.running} />
        <Button
          variant="ghost"
          size="sm"
          onClick={deleteTask}
          className="ml-auto text-red-600 dark:text-red-400"
        >
          <Icon name="trash" className="h-3.5 w-3.5" /> Delete task
        </Button>
      </div>

      <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_320px]">
        <div className="min-w-0 space-y-4">
          <TitleEditor value={t.title} onSave={(title) => patch({ title })} />
          <DescriptionEditor
            value={t.description}
            onSave={(description) => patch({ description })}
          />
          {t.result && t.status !== "IN_PROGRESS" && (
            <ResultCard text={t.result} agentName={t.assignee?.name} />
          )}
          <Thread
            task={t}
            messages={messages.data ?? []}
            loading={messages.loading && !messages.data}
            error={messages.error}
            onRetry={messages.reload}
            agents={agents.data ?? []}
            onSent={() => {
              void messages.reload();
              void task.reload();
            }}
          />
        </div>

        <aside className="space-y-4">
          <Sidebar
            task={t}
            agents={agents.data}
            onPatch={patch}
            onReload={() => {
              void task.reload();
              void runs.reload();
            }}
          />
          <Card>
            <CardHeader title="Agent runs" />
            {runs.loading && !runs.data ? (
              <Loading />
            ) : (runs.data ?? []).length === 0 ? (
              <p className="px-4 py-3 text-xs text-zinc-500">No runs yet.</p>
            ) : (
              <ul className="divide-y divide-zinc-100 text-xs dark:divide-zinc-800">
                {(runs.data ?? []).slice(0, 10).map((r) => (
                  <li key={r.id} className="space-y-1 px-4 py-2.5">
                    <div className="flex items-center justify-between gap-2">
                      <RunStatusBadge status={r.status} />
                      <span
                        className="text-zinc-500"
                        title={fmtTime(r.started_at)}
                      >
                        {fmtRelative(r.started_at)}
                      </span>
                    </div>
                    <p className="text-zinc-600 dark:text-zinc-400">
                      {r.turns} turns · {r.tool_calls} tools ·{" "}
                      {(r.input_tokens + r.output_tokens).toLocaleString()}{" "}
                      tokens
                    </p>
                    {(r.model || r.runtime === "acp") && (
                      <p className="flex min-w-0 items-center gap-1.5 text-zinc-500">
                        {r.runtime === "acp" && <Pill>ACP</Pill>}
                        {r.model && (
                          <span className="truncate font-mono text-[11px]">
                            {r.model}
                          </span>
                        )}
                      </p>
                    )}
                    {r.error && (
                      <p className="break-words text-red-600 dark:text-red-400">
                        {r.error}
                      </p>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </aside>
      </div>
    </div>
  );
}

/* --------------------------------- Header ---------------------------------- */

function TitleEditor({
  value,
  onSave,
}: {
  value: string;
  onSave: (v: string) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  if (!editing) {
    return (
      <h1
        onClick={() => setEditing(true)}
        title="Click to edit"
        className="cursor-text text-xl font-semibold tracking-tight text-zinc-900 hover:bg-zinc-100/60 dark:text-zinc-50 dark:hover:bg-zinc-800/40"
      >
        {value || "Untitled task"}
      </h1>
    );
  }
  const commit = () => {
    setEditing(false);
    if (draft.trim() && draft.trim() !== value) onSave(draft.trim());
    else setDraft(value);
  };
  return (
    <Input
      autoFocus
      value={draft}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === "Enter") commit();
        if (e.key === "Escape") {
          setDraft(value);
          setEditing(false);
        }
      }}
      className="h-10 text-lg font-semibold"
    />
  );
}

function DescriptionEditor({
  value,
  onSave,
}: {
  value: string;
  onSave: (v: string) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  if (editing) {
    return (
      <div className="space-y-2">
        <Textarea
          autoFocus
          rows={5}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
        />
        <div className="flex justify-end gap-2">
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              setDraft(value);
              setEditing(false);
            }}
          >
            Cancel
          </Button>
          <Button
            size="sm"
            variant="primary"
            onClick={() => {
              setEditing(false);
              if (draft !== value) onSave(draft);
            }}
          >
            Save
          </Button>
        </div>
      </div>
    );
  }
  return (
    <div className="group relative">
      {value ? (
        <p className="text-sm leading-relaxed whitespace-pre-wrap text-zinc-700 dark:text-zinc-300">
          {value}
        </p>
      ) : (
        <p className="text-sm text-zinc-400 italic">No description.</p>
      )}
      <button
        onClick={() => setEditing(true)}
        className="mt-1 inline-flex items-center gap-1 text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200"
      >
        <Icon name="edit" className="h-3 w-3" /> Edit description
      </button>
    </div>
  );
}

function ResultCard({ text, agentName }: { text: string; agentName?: string }) {
  return (
    <div className="rounded-lg border border-teal-200 bg-teal-50/70 p-4 dark:border-teal-900/60 dark:bg-teal-950/30">
      <p className="mb-1.5 flex items-center gap-1.5 text-xs font-semibold tracking-wide text-teal-800 uppercase dark:text-teal-300">
        <Icon name="check" className="h-3.5 w-3.5" /> Result
        {agentName ? ` from ${agentName}` : ""}
      </p>
      <p className="text-sm leading-relaxed whitespace-pre-wrap text-zinc-800 dark:text-zinc-200">
        {text}
      </p>
    </div>
  );
}

/* --------------------------------- Thread ---------------------------------- */

function Thread({
  task,
  messages,
  loading,
  error,
  onRetry,
  agents,
  onSent,
}: {
  task: TaskDetail;
  messages: TaskMessage[];
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  agents: Agent[];
  onSent: () => void;
}) {
  // Keep the thread pinned to the newest message: jump to the bottom on first load, then follow
  // new messages only while the reader is already near the bottom (don't yank them away from
  // older messages they scrolled up to read). Scrolls the thread container, never the page.
  const scroller = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const initialized = useRef(false);
  const lastId = messages.length ? messages[messages.length - 1].id : "";
  const onScroll = () => {
    const el = scroller.current;
    if (el)
      pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  };
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el || messages.length === 0) return;
    if (!initialized.current || pinned.current) {
      el.scrollTop = el.scrollHeight;
      initialized.current = true;
      pinned.current = true;
    }
  }, [lastId, messages.length, task.running]);
  const handleSent = () => {
    pinned.current = true; // your own message always scrolls into view
    onSent();
  };
  // Tool calls waiting for (or decided by) the human; refreshed as the thread grows.
  const hasApprovals = messages.some((m) => m.kind === "approval");
  const approvals = useAsync(
    () =>
      hasApprovals
        ? api.taskApprovals(task.id)
        : Promise.resolve([] as ToolApproval[]),
    [task.id, hasApprovals, messages.length],
  );
  const approvalById = Object.fromEntries(
    (approvals.data ?? []).map((a) => [a.id, a]),
  );

  return (
    <Card>
      <CardHeader
        title="Conversation"
        subtitle="Talk to the assigned agent. Start with @slug to hand the task to another agent."
      />
      <div
        ref={scroller}
        onScroll={onScroll}
        className="max-h-[65vh] min-h-[200px] space-y-3 overflow-y-auto overscroll-contain px-4 py-4"
      >
        {loading && <Loading />}
        {error && <ErrorBox onRetry={onRetry}>{error}</ErrorBox>}
        {!loading && messages.length === 0 && (
          <p className="py-6 text-center text-sm text-zinc-500">
            {task.assignee
              ? task.status === "BACKLOG"
                ? `In backlog. Move it to To do (or press Run now) and ${task.assignee.name} will start.`
                : `${task.assignee.name} will reply here.`
              : "Assign an agent to get started."}
          </p>
        )}
        {messages.map((m) =>
          m.kind === "approval" ? (
            <ApprovalCard
              key={m.id}
              m={m}
              approval={approvalById[String(m.data?.approval_id ?? "")]}
              onDecided={() => {
                void approvals.reload();
                handleSent();
              }}
            />
          ) : (
            <MessageRow key={m.id} m={m} />
          ),
        )}
        {task.running && (
          <div className="flex items-center gap-2 text-sm text-zinc-500">
            <AgentAvatar agent={task.assignee} size="sm" />
            <span className="inline-flex items-center gap-1.5 rounded-full bg-zinc-100 px-3 py-1.5 dark:bg-zinc-800">
              <TypingDots /> {task.assignee?.name ?? "Agent"} is working…
            </span>
          </div>
        )}
      </div>
      <Composer task={task} agents={agents} onSent={handleSent} />
    </Card>
  );
}

function TypingDots() {
  return (
    <span className="inline-flex gap-0.5">
      {[0, 1, 2].map((i) => (
        <span
          key={i}
          className="h-1.5 w-1.5 animate-bounce rounded-full bg-zinc-400"
          style={{ animationDelay: `${i * 0.15}s` }}
        />
      ))}
    </span>
  );
}

function argSummary(args: unknown): string {
  if (!args || typeof args !== "object") return "";
  const vals = Object.values(args as Record<string, unknown>)
    .map((v) => (typeof v === "string" ? v : JSON.stringify(v)))
    .filter(Boolean);
  const s = vals.join(" · ");
  return Array.from(s).length > 90
    ? Array.from(s).slice(0, 90).join("") + "…"
    : s;
}

/** An agent's request to run an "ask" tool, with Approve / Reject while pending. */
function ApprovalCard({
  m,
  approval,
  onDecided,
}: {
  m: TaskMessage;
  approval: ToolApproval | undefined;
  onDecided: () => void;
}) {
  const toast = useToast();
  const dialog = useDialog();
  const [busy, setBusy] = useState<"approve" | "reject" | null>(null);
  const [showResult, setShowResult] = useState(false);
  const tool = String(m.data?.tool ?? approval?.tool ?? "tool");
  const server = String(m.data?.server ?? approval?.server_name ?? "");
  const args = JSON.stringify(
    m.data?.arguments ?? approval?.arguments ?? {},
    null,
    2,
  );
  const status = approval?.status;

  const decide = async (decision: "approve" | "reject") => {
    if (!approval) return;
    let note: string | undefined;
    if (decision === "reject") {
      const v = await dialog.prompt({
        title: `Reject ${tool}?`,
        message:
          "The agent is told not to retry it. Optionally say why or what to do instead.",
        label: "Note for the agent",
        multiline: true,
        confirmLabel: "Reject",
        tone: "danger",
      });
      if (v === null) return;
      note = v;
    }
    setBusy(decision);
    try {
      await api.decideApproval(m.task_id, approval.id, decision, note);
      toast.success(decision === "approve" ? `${tool} ran` : "Rejected");
      onDecided();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="flex items-start gap-2">
      <AgentAvatar agent={m.agent} size="sm" className="mt-0.5" />
      <div className="min-w-0 max-w-[85%] flex-1">
        <p className="mb-0.5 flex items-center gap-2 text-xs font-medium text-zinc-700 dark:text-zinc-300">
          {m.agent?.name ?? "Agent"}
          <span className="text-amber-700 dark:text-amber-400">
            asks to run a tool
          </span>
          <span
            className="text-[11px] text-zinc-400"
            title={fmtTime(m.created_at)}
          >
            {fmtRelative(m.created_at)}
          </span>
        </p>
        <div
          className={cx(
            "rounded-2xl rounded-tl-sm border px-3.5 py-2.5 text-sm",
            status === "pending" || !status
              ? "border-amber-300 bg-amber-50 dark:border-amber-700/60 dark:bg-amber-950/40"
              : status === "approved"
                ? "border-emerald-200 bg-emerald-50 dark:border-emerald-900/60 dark:bg-emerald-950/30"
                : "border-zinc-200 bg-zinc-50 dark:border-zinc-800 dark:bg-zinc-900",
          )}
        >
          <p className="text-zinc-900 dark:text-zinc-100">
            <span className="font-mono font-semibold">{tool}</span>
            {server && <span className="text-zinc-500"> on {server}</span>}
          </p>
          <pre className="mt-1.5 max-h-48 overflow-auto rounded-md bg-white/70 p-2 font-mono text-[11px] leading-relaxed text-zinc-700 dark:bg-black/30 dark:text-zinc-300">
            {args}
          </pre>
          {status === "pending" && (
            <div className="mt-2 flex gap-2">
              <Button
                size="sm"
                variant="primary"
                onClick={() => void decide("approve")}
                loading={busy === "approve"}
                disabled={!!busy}
              >
                Approve &amp; run
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => void decide("reject")}
                loading={busy === "reject"}
                disabled={!!busy}
              >
                Reject
              </Button>
            </div>
          )}
          {status === "approved" && (
            <div className="mt-2 text-xs">
              <button
                className="text-emerald-700 hover:underline dark:text-emerald-400"
                onClick={() => setShowResult((v) => !v)}
              >
                {approval?.is_error ? "Approved · failed" : "Approved · ran"}{" "}
                {showResult ? "▾" : "▸"}
              </button>
              {showResult && (
                <pre className="mt-1 max-h-60 overflow-auto rounded-md bg-white/70 p-2 font-mono text-[11px] whitespace-pre-wrap text-zinc-700 dark:bg-black/30 dark:text-zinc-300">
                  {approval?.result}
                </pre>
              )}
            </div>
          )}
          {status === "rejected" && (
            <p className="mt-2 text-xs text-zinc-500">
              Rejected{approval?.note ? `: ${approval.note}` : ""}
            </p>
          )}
        </div>
      </div>
    </div>
  );
}

function MessageRow({ m }: { m: TaskMessage }) {
  const [open, setOpen] = useState(false);
  const time = (
    <span className="text-[11px] text-zinc-400" title={fmtTime(m.created_at)}>
      {fmtRelative(m.created_at)}
    </span>
  );

  if (m.kind === "status" || m.author_type === "system") {
    // Long system notes (e.g. an approved tool's output) show their first line, expandable.
    const [first, ...rest] = m.content.split("\n");
    return (
      <div className="py-0.5 text-[11px] text-zinc-500">
        <div className="flex items-center gap-3">
          <span className="h-px flex-1 bg-zinc-200 dark:bg-zinc-800" />
          <span className="max-w-[80%] text-center">
            {first} · {fmtRelative(m.created_at)}
            {rest.length > 0 && (
              <button
                className="ml-1 text-zinc-600 hover:underline dark:text-zinc-400"
                onClick={() => setOpen((v) => !v)}
              >
                {open ? "hide" : "details"}
              </button>
            )}
          </span>
          <span className="h-px flex-1 bg-zinc-200 dark:bg-zinc-800" />
        </div>
        {open && rest.length > 0 && (
          <pre className="mx-auto mt-1 max-h-60 max-w-[80%] overflow-auto rounded-md bg-zinc-100 p-2 font-mono whitespace-pre-wrap dark:bg-zinc-900">
            {rest.join("\n")}
          </pre>
        )}
      </div>
    );
  }

  if (m.kind === "tool_call" || m.kind === "tool_result") {
    const tool = String(m.data?.tool ?? "tool");
    const isErr = m.kind === "tool_result" && Boolean(m.data?.is_error);
    const summary =
      m.kind === "tool_call"
        ? argSummary(m.data?.arguments)
        : m.content.replace(/\s+/g, " ").slice(0, 90);
    const detail =
      m.kind === "tool_call"
        ? JSON.stringify(m.data?.arguments ?? {}, null, 2)
        : m.content;
    return (
      <div className="ml-10">
        <button
          onClick={() => setOpen(!open)}
          className={cx(
            "flex w-full items-center gap-2 rounded px-2 py-1 text-left font-mono text-[11px] hover:bg-zinc-100 dark:hover:bg-zinc-800/60",
            isErr ? "text-red-600 dark:text-red-400" : "text-zinc-500",
          )}
        >
          <span className="shrink-0">
            {m.kind === "tool_call" ? "🔧" : isErr ? "⚠" : "↳"}
          </span>
          <span className="shrink-0 font-semibold">
            {m.kind === "tool_call" ? tool : `${tool} result`}
          </span>
          <span className="min-w-0 truncate opacity-80">{summary}</span>
          <span className="ml-auto shrink-0 opacity-60">
            {open ? "▾" : "▸"}
          </span>
        </button>
        {open && (
          <pre className="mt-1 max-h-64 overflow-auto rounded bg-zinc-50 p-2 text-[11px] leading-relaxed whitespace-pre-wrap text-zinc-700 dark:bg-zinc-950 dark:text-zinc-300">
            {detail}
          </pre>
        )}
      </div>
    );
  }

  if (m.author_type === "human") {
    return (
      <div className="flex flex-col items-end gap-0.5">
        <div className="max-w-[85%] rounded-2xl rounded-br-sm bg-indigo-600 px-3.5 py-2 text-sm leading-relaxed whitespace-pre-wrap text-white dark:bg-indigo-500">
          {m.content}
        </div>
        {time}
      </div>
    );
  }

  // agent messages: message | question | result
  const tone =
    m.kind === "question"
      ? "border border-amber-300 bg-amber-50 dark:border-amber-700/60 dark:bg-amber-950/40"
      : m.kind === "result"
        ? "border border-teal-200 bg-teal-50 dark:border-teal-900/60 dark:bg-teal-950/30"
        : "bg-zinc-100 dark:bg-zinc-800";
  return (
    <div className="flex items-start gap-2">
      <AgentAvatar agent={m.agent} size="sm" className="mt-0.5" />
      <div className="min-w-0 max-w-[85%]">
        <p className="mb-0.5 flex items-center gap-2 text-xs font-medium text-zinc-700 dark:text-zinc-300">
          {m.agent?.name ?? "Agent"}
          {m.kind === "question" && (
            <span className="text-amber-700 dark:text-amber-400">asks you</span>
          )}
          {m.kind === "result" && (
            <span className="text-teal-700 dark:text-teal-400">finished</span>
          )}
          {time}
        </p>
        <div
          className={cx(
            "rounded-2xl rounded-tl-sm px-3.5 py-2 text-sm leading-relaxed break-words whitespace-pre-wrap text-zinc-900 dark:text-zinc-100",
            tone,
          )}
        >
          {m.content}
        </div>
      </div>
    </div>
  );
}

/* -------------------------------- Composer --------------------------------- */

function Composer({
  task,
  agents,
  onSent,
}: {
  task: TaskDetail;
  agents: Agent[];
  onSent: () => void;
}) {
  const toast = useToast();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [mention, setMention] = useState<{
    query: string;
    start: number;
  } | null>(null);
  const [sel, setSel] = useState(0);
  const ref = useRef<HTMLTextAreaElement>(null);

  const candidates = useMemo(() => {
    if (!mention) return [];
    const q = mention.query.toLowerCase();
    return agents
      .filter(
        (a) =>
          a.is_active &&
          (a.slug.startsWith(q) || a.name.toLowerCase().includes(q)),
      )
      .slice(0, 6);
  }, [mention, agents]);

  const detect = (value: string, caret: number) => {
    const before = value.slice(0, caret);
    const m = /(^|\s)@([a-z0-9-]*)$/i.exec(before);
    if (m) {
      setMention({ query: m[2], start: caret - m[2].length - 1 });
      setSel(0);
    } else setMention(null);
  };

  const pick = (a: Agent) => {
    if (!mention) return;
    const caret = ref.current?.selectionStart ?? text.length;
    const next =
      text.slice(0, mention.start) + "@" + a.slug + " " + text.slice(caret);
    setText(next);
    setMention(null);
    requestAnimationFrame(() => {
      const pos = mention.start + a.slug.length + 2;
      ref.current?.focus();
      ref.current?.setSelectionRange(pos, pos);
    });
  };

  const send = async () => {
    const content = text.trim();
    if (!content || busy) return;
    setBusy(true);
    try {
      await api.postTaskMessage(task.id, content);
      setText("");
      setMention(null);
      onSent();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  const willWake =
    task.assignee && task.status !== "BACKLOG" && task.status !== "CANCELED";

  return (
    <div className="relative border-t border-zinc-200 p-3 dark:border-zinc-800">
      {mention && candidates.length > 0 && (
        <ul className="absolute bottom-full left-3 z-10 mb-1 w-64 overflow-hidden rounded-md border border-zinc-200 bg-white shadow-lg dark:border-zinc-700 dark:bg-zinc-900">
          {candidates.map((a, i) => (
            <li key={a.id}>
              <button
                onMouseDown={(e) => {
                  e.preventDefault();
                  pick(a);
                }}
                className={cx(
                  "flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm",
                  i === sel
                    ? "bg-indigo-50 dark:bg-indigo-500/10"
                    : "hover:bg-zinc-50 dark:hover:bg-zinc-800",
                )}
              >
                <AgentAvatar agent={a} size="xs" />
                <span className="text-zinc-900 dark:text-zinc-100">
                  {a.name}
                </span>
                <span className="ml-auto font-mono text-xs text-zinc-500">
                  @{a.slug}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="flex items-end gap-2">
        <Textarea
          ref={ref}
          rows={2}
          value={text}
          placeholder={
            task.assignee
              ? `Message ${task.assignee.name}… (Enter to send, Shift+Enter for a new line)`
              : "Add a comment… (@slug to assign an agent)"
          }
          onChange={(e) => {
            setText(e.target.value);
            detect(
              e.target.value,
              e.target.selectionStart ?? e.target.value.length,
            );
          }}
          onKeyDown={(e) => {
            if (mention && candidates.length > 0) {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setSel((s) => (s + 1) % candidates.length);
                return;
              }
              if (e.key === "ArrowUp") {
                e.preventDefault();
                setSel((s) => (s - 1 + candidates.length) % candidates.length);
                return;
              }
              if (e.key === "Enter" || e.key === "Tab") {
                e.preventDefault();
                pick(candidates[sel]);
                return;
              }
              if (e.key === "Escape") {
                setMention(null);
                return;
              }
            }
            if (
              e.key === "Enter" &&
              !e.shiftKey &&
              !e.nativeEvent.isComposing
            ) {
              e.preventDefault();
              void send();
            }
          }}
          className="min-h-[44px] resize-none"
        />
        <Button
          variant="primary"
          loading={busy}
          disabled={!text.trim()}
          onClick={send}
          aria-label="Send"
        >
          <Icon name="send" className="h-4 w-4" />
        </Button>
      </div>
      <p className="mt-1.5 text-[11px] text-zinc-500">
        {willWake
          ? `${task.assignee!.name} will respond to your message.`
          : "Comments on unassigned or backlog tasks don't start an agent."}
      </p>
    </div>
  );
}

/* --------------------------------- Sidebar --------------------------------- */

function Sidebar({
  task: t,
  agents,
  onPatch,
  onReload,
}: {
  task: TaskDetail;
  agents: Agent[] | undefined;
  onPatch: (b: TaskPatch, ok?: string) => Promise<void>;
  onReload: () => void;
}) {
  const toast = useToast();
  const [busy, setBusy] = useState<"run" | "stop" | null>(null);
  const [subOpen, setSubOpen] = useState(false);
  const statusOptions = ALL_TASK_STATUSES.filter(
    (s) => s !== "IN_PROGRESS" || t.status === "IN_PROGRESS",
  );

  const run = async () => {
    setBusy("run");
    try {
      await api.runTask(t.id);
      toast.success(`${t.assignee?.name ?? "Agent"} is on it`);
      onReload();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };
  const stop = async () => {
    setBusy("stop");
    try {
      await api.cancelTask(t.id);
      toast.info("Stopping the agent");
      onReload();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <Card>
        <div className="space-y-3 p-4">
          <div className="flex gap-2">
            {t.running ? (
              <Button
                variant="danger"
                className="flex-1"
                loading={busy === "stop"}
                onClick={stop}
              >
                <Icon name="stop" className="h-4 w-4" /> Stop agent
              </Button>
            ) : (
              <Button
                variant="primary"
                className="flex-1"
                loading={busy === "run"}
                disabled={!t.assignee_agent_id}
                title={
                  t.assignee_agent_id
                    ? "Start the assigned agent now"
                    : "Assign an agent first"
                }
                onClick={run}
              >
                <Icon name="play" className="h-4 w-4" /> Run now
              </Button>
            )}
          </div>
          <Field label="Status">
            <Select
              value={t.status}
              disabled={t.status === "IN_PROGRESS"}
              onChange={(e) =>
                void onPatch({ status: e.target.value as TaskStatus })
              }
            >
              {statusOptions.map((s) => (
                <option key={s} value={s} disabled={s === "IN_PROGRESS"}>
                  {TASK_STATUS_LABEL[s]}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Assignee">
            <AgentSelect
              agents={agents}
              value={t.assignee_agent_id}
              onChange={(v) => void onPatch({ assignee_agent_id: v })}
            />
          </Field>
          <Field label="Priority" right={<PriorityDot priority={t.priority} />}>
            <Select
              value={t.priority}
              onChange={(e) =>
                void onPatch({ priority: e.target.value as TaskPriority })
              }
            >
              {PRIORITIES.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Labels">
            <TagInput
              value={t.labels ?? []}
              onChange={(labels) => void onPatch({ labels })}
              placeholder="Add label"
            />
          </Field>
          <Field label="Due">
            <DateTimePicker
              aria-label="Due"
              value={toLocalInput(t.due_at)}
              onChange={(v) => void onPatch({ due_at: fromLocalInput(v) })}
            />
          </Field>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 border-t border-zinc-100 pt-3 text-xs dark:border-zinc-800">
            <dt className="text-zinc-500">Created</dt>
            <dd className="text-zinc-700 dark:text-zinc-300">
              {fmtTime(t.created_at)} by{" "}
              {t.created_by === "agent" ? "an agent" : "you"}
            </dd>
            {t.started_at && (
              <>
                <dt className="text-zinc-500">Started</dt>
                <dd className="text-zinc-700 dark:text-zinc-300">
                  {fmtTime(t.started_at)}
                </dd>
              </>
            )}
            {t.completed_at && (
              <>
                <dt className="text-zinc-500">Completed</dt>
                <dd className="text-zinc-700 dark:text-zinc-300">
                  {fmtTime(t.completed_at)}
                </dd>
              </>
            )}
            <dt className="text-zinc-500">Attempts</dt>
            <dd className="text-zinc-700 dark:text-zinc-300">{t.attempts}</dd>
            {t.schedule_id && (
              <>
                <dt className="text-zinc-500">From</dt>
                <dd>
                  <Link
                    href="/schedules/"
                    className="text-indigo-600 hover:underline dark:text-indigo-400"
                  >
                    schedule
                  </Link>
                </dd>
              </>
            )}
          </dl>
        </div>
      </Card>

      <Card>
        <CardHeader title="Publishing" />
        <div className="px-4 py-3">
          <PublishTarget
            publisherId={t.publisher_connector_id ?? null}
            accounts={t.publish_accounts ?? []}
            onChange={(pub, accts) =>
              onPatch({ publisher_connector_id: pub, publish_accounts: accts })
            }
          />
        </div>
      </Card>

      {t.parent && (
        <Card>
          <CardHeader title="Parent task" />
          <Link
            href={`/tasks/view/?id=${t.parent.id}`}
            className="flex items-center gap-2 px-4 py-3 text-sm hover:bg-zinc-50 dark:hover:bg-zinc-800/40"
          >
            <span className="font-mono text-xs text-zinc-400">
              #{t.parent.number}
            </span>
            <span className="min-w-0 flex-1 truncate">{t.parent.title}</span>
            <TaskStatusBadge
              status={t.parent.status}
              running={t.parent.running}
            />
          </Link>
        </Card>
      )}

      <Card>
        <CardHeader
          title={`Subtasks${(t.subtasks?.length ?? 0) > 0 ? ` (${t.subtasks!.filter((s) => s.status === "DONE").length}/${t.subtasks!.length})` : ""}`}
          actions={
            <Button size="sm" variant="ghost" onClick={() => setSubOpen(true)}>
              <Icon name="plus" className="h-3.5 w-3.5" /> Add
            </Button>
          }
        />
        {(t.subtasks ?? []).length === 0 ? (
          <p className="px-4 py-3 text-xs text-zinc-500">
            No subtasks. Manager agents create them when they delegate.
          </p>
        ) : (
          <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {t.subtasks!.map((s) => (
              <li key={s.id}>
                <Link
                  href={`/tasks/view/?id=${s.id}`}
                  className="flex items-center gap-2 px-4 py-2 text-sm hover:bg-zinc-50 dark:hover:bg-zinc-800/40"
                >
                  <AgentAvatar agent={s.assignee} size="xs" />
                  <span className="min-w-0 flex-1 truncate">
                    <span className="mr-1 font-mono text-xs text-zinc-400">
                      #{s.number}
                    </span>
                    {s.title}
                  </span>
                  <TaskStatusBadge status={s.status} running={s.running} />
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card>
        <CardHeader
          title="Drafts"
          subtitle="Posts written for this task (they wait in Review)"
        />
        {(t.posts ?? []).length === 0 ? (
          <p className="px-4 py-3 text-xs text-zinc-500">No drafts yet.</p>
        ) : (
          <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {t.posts!.map((p) => (
              <li key={p.id}>
                <Link
                  href={`/posts/view/?id=${p.id}`}
                  className="flex items-center gap-2 px-4 py-2 text-sm hover:bg-zinc-50 dark:hover:bg-zinc-800/40"
                >
                  <span className="min-w-0 flex-1 truncate">
                    {p.title || "(untitled draft)"}
                  </span>
                  <span className="shrink-0 text-[11px] text-zinc-500">
                    {(p.platforms ?? []).join(", ")}
                  </span>
                  <StatusBadge status={p.status} />
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <NewTaskModal
        open={subOpen}
        onClose={() => setSubOpen(false)}
        agents={(agents ?? []).filter((a) => a.is_active)}
        parentId={t.id}
        onCreated={onReload}
      />
    </>
  );
}

export default function Page() {
  return (
    <Suspense fallback={<Loading />}>
      <TaskView />
    </Suspense>
  );
}
