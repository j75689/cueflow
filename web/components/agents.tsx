"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import type { Agent, AgentRef, Task, TaskPriority, TaskStatus } from "@/lib/types";
import { useServerEvent, useToast } from "./providers";
import { Button, Field, Input, Modal, Pill, Select, Textarea, cx } from "./ui";

/* ------------------------------- Task status ------------------------------- */

export const TASK_STATUS_LABEL: Record<TaskStatus, string> = {
  BACKLOG: "Backlog",
  TODO: "To do",
  IN_PROGRESS: "In progress",
  BLOCKED: "Waiting on subtasks",
  WAITING_HUMAN: "Needs you",
  REVIEW: "Review",
  DONE: "Done",
  CANCELED: "Canceled",
};

const TASK_STATUS_STYLE: Record<TaskStatus, { pill: string; dot: string }> = {
  BACKLOG: { pill: "bg-zinc-100 text-zinc-700 dark:bg-zinc-800 dark:text-zinc-300", dot: "bg-zinc-400" },
  TODO: { pill: "bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300", dot: "bg-sky-500" },
  IN_PROGRESS: { pill: "bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300", dot: "bg-indigo-500" },
  BLOCKED: { pill: "bg-violet-100 text-violet-800 dark:bg-violet-950 dark:text-violet-300", dot: "bg-violet-500" },
  WAITING_HUMAN: { pill: "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300", dot: "bg-amber-500" },
  REVIEW: { pill: "bg-teal-100 text-teal-800 dark:bg-teal-950 dark:text-teal-300", dot: "bg-teal-500" },
  DONE: { pill: "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300", dot: "bg-emerald-500" },
  CANCELED: { pill: "bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-500", dot: "bg-zinc-400" },
};

export const ALL_TASK_STATUSES: TaskStatus[] = [
  "BACKLOG",
  "TODO",
  "IN_PROGRESS",
  "BLOCKED",
  "WAITING_HUMAN",
  "REVIEW",
  "DONE",
  "CANCELED",
];

export function TaskStatusBadge({ status, running }: { status: TaskStatus; running?: boolean }) {
  const s = TASK_STATUS_STYLE[status] ?? TASK_STATUS_STYLE.BACKLOG;
  return (
    <Pill className={s.pill}>
      <span className={cx("h-1.5 w-1.5 rounded-full", s.dot, (running || status === "IN_PROGRESS") && "animate-pulse")} />
      {TASK_STATUS_LABEL[status] ?? status}
    </Pill>
  );
}

/* -------------------------------- Priority --------------------------------- */

export const PRIORITIES: TaskPriority[] = ["low", "normal", "high", "urgent"];

const PRIORITY_DOT: Record<TaskPriority, string> = {
  low: "bg-zinc-300 dark:bg-zinc-600",
  normal: "bg-sky-400",
  high: "bg-orange-500",
  urgent: "bg-red-600",
};

export function PriorityDot({ priority, withLabel }: { priority: TaskPriority; withLabel?: boolean }) {
  return (
    <span className="inline-flex items-center gap-1 text-[11px] text-zinc-500" title={`Priority: ${priority}`}>
      <span className={cx("h-2 w-2 rounded-full", PRIORITY_DOT[priority] ?? PRIORITY_DOT.normal)} />
      {withLabel && priority}
    </span>
  );
}

/* --------------------------------- Agents ---------------------------------- */

export const ROLE_LABEL: Record<string, string> = {
  manager: "Manager",
  researcher: "Researcher",
  writer: "Writer",
  editor: "Editor",
  custom: "Custom",
};

export function AgentAvatar({
  agent,
  size = "md",
  className,
}: {
  agent: Pick<AgentRef, "avatar" | "name"> | null | undefined;
  size?: "xs" | "sm" | "md" | "lg";
  className?: string;
}) {
  const dims = { xs: "h-5 w-5 text-[11px]", sm: "h-6 w-6 text-sm", md: "h-8 w-8 text-base", lg: "h-12 w-12 text-2xl" }[size];
  if (!agent) {
    return (
      <span
        className={cx(
          "inline-flex shrink-0 items-center justify-center rounded-full border border-dashed border-zinc-300 text-zinc-400 dark:border-zinc-600",
          dims,
          className,
        )}
        title="Unassigned"
      >
        ?
      </span>
    );
  }
  const isEmoji = agent.avatar && !/^[a-z0-9]/i.test(agent.avatar);
  return (
    <span
      className={cx(
        "inline-flex shrink-0 items-center justify-center rounded-full bg-indigo-50 font-semibold text-indigo-700 ring-1 ring-indigo-100 dark:bg-indigo-500/10 dark:text-indigo-300 dark:ring-indigo-500/20",
        dims,
        className,
      )}
      title={agent.name}
    >
      {isEmoji ? agent.avatar : (agent.name || "?").slice(0, 1).toUpperCase()}
    </span>
  );
}

// useAgents loads all agents and refreshes on agent.updated.
export function useAgents() {
  const state = useAsync(() => api.listAgents(), []);
  useServerEvent("agent.updated", () => void state.reload());
  return state;
}

export function agentRef(agents: Agent[] | undefined, id: string | null | undefined): AgentRef | null {
  if (!id || !agents) return null;
  const a = agents.find((x) => x.id === id);
  return a ? { id: a.id, name: a.name, slug: a.slug, avatar: a.avatar } : null;
}

export function AgentSelect({
  agents,
  value,
  onChange,
  allowNone = true,
  noneLabel = "Unassigned",
  className,
}: {
  agents: Agent[] | undefined;
  value: string | null;
  onChange: (id: string | null) => void;
  allowNone?: boolean;
  noneLabel?: string;
  className?: string;
}) {
  return (
    <Select className={className} value={value ?? ""} onChange={(e) => onChange(e.target.value || null)}>
      {allowNone && <option value="">{noneLabel}</option>}
      {(agents ?? []).map((a) => (
        <option key={a.id} value={a.id} disabled={!a.is_active && a.id !== value}>
          {a.avatar} {a.name}
          {!a.is_active ? " (inactive)" : ""}
        </option>
      ))}
    </Select>
  );
}

/* ------------------------------ New task modal ----------------------------- */

export function NewTaskModal({
  open,
  onClose,
  agents,
  defaultAgentId,
  defaultStatus,
  parentId,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  agents: Agent[] | undefined;
  defaultAgentId?: string | null;
  defaultStatus?: TaskStatus;
  parentId?: string | null;
  onCreated?: (t: Task) => void;
}) {
  const toast = useToast();
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [assignee, setAssignee] = useState<string | null>(defaultAgentId ?? null);
  const [priority, setPriority] = useState<TaskPriority>("normal");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      setTitle("");
      setDescription("");
      setAssignee(defaultAgentId ?? null);
      setPriority("normal");
    }
  }, [open, defaultAgentId]);

  const submit = async () => {
    if (!title.trim()) return;
    setBusy(true);
    try {
      const t = await api.createTask({
        title: title.trim(),
        description: description.trim() || undefined,
        assignee_agent_id: assignee,
        priority,
        status: defaultStatus,
        parent_id: parentId ?? undefined,
      });
      toast.success(`Task #${t.number} created`);
      onCreated?.(t);
      onClose();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New task"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!title.trim()} onClick={submit}>
            Create task
          </Button>
        </>
      }
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <Field label="Title" required>
          <Input autoFocus value={title} onChange={(e) => setTitle(e.target.value)} placeholder="What should be done?" />
        </Field>
        <Field label="Description">
          <Textarea value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Context, links, expectations…" />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Assignee">
            <AgentSelect agents={agents} value={assignee} onChange={setAssignee} />
          </Field>
          <Field label="Priority">
            <Select value={priority} onChange={(e) => setPriority(e.target.value as TaskPriority)}>
              {PRIORITIES.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        {assignee && !defaultStatus && (
          <p className="text-xs text-zinc-500">The agent picks this up right away.</p>
        )}
      </form>
    </Modal>
  );
}

/* ------------------------------ Cron helpers ------------------------------- */

export const CRON_PRESETS: { label: string; value: string }[] = [
  { label: "Every day at 08:00", value: "0 8 * * *" },
  { label: "Weekdays at 09:00", value: "0 9 * * 1-5" },
  { label: "Twice a day (08:00, 20:00)", value: "0 8,20 * * *" },
  { label: "Every 6 hours", value: "0 */6 * * *" },
  { label: "Every hour", value: "@every 1h" },
  { label: "Mondays at 10:00", value: "0 10 * * 1" },
];

export function defaultTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

export function timezones(): string[] {
  try {
    const fn = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf;
    if (fn) return ["UTC", ...fn("timeZone").filter((z) => z !== "UTC")];
  } catch {
    /* fall through */
  }
  return ["UTC", "Asia/Taipei", "Asia/Tokyo", "America/New_York", "Europe/London"];
}
