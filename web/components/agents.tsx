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
  BACKLOG: { pill: "bg-subtle text-fg-2", dot: "bg-fg-4" },
  TODO: { pill: "bg-sky-500/10 text-sky-700 dark:bg-sky-400/10 dark:text-sky-300", dot: "bg-sky-500" },
  IN_PROGRESS: { pill: "bg-indigo-500/10 text-indigo-700 dark:bg-indigo-400/10 dark:text-indigo-300", dot: "bg-indigo-500" },
  BLOCKED: { pill: "bg-violet-500/10 text-violet-700 dark:bg-violet-400/10 dark:text-violet-300", dot: "bg-violet-500" },
  WAITING_HUMAN: { pill: "bg-amber-500/10 text-amber-700 dark:bg-amber-400/10 dark:text-amber-300", dot: "bg-amber-500" },
  REVIEW: { pill: "bg-teal-500/10 text-teal-700 dark:bg-teal-400/10 dark:text-teal-300", dot: "bg-teal-500" },
  DONE: { pill: "bg-emerald-500/10 text-emerald-700 dark:bg-emerald-400/10 dark:text-emerald-300", dot: "bg-emerald-500" },
  CANCELED: { pill: "bg-subtle text-fg-3", dot: "bg-fg-4" },
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
  low: "bg-line-strong",
  normal: "bg-sky-400",
  high: "bg-orange-500",
  urgent: "bg-red-600",
};

export function PriorityDot({ priority, withLabel }: { priority: TaskPriority; withLabel?: boolean }) {
  return (
    <span className="inline-flex items-center gap-1 text-[11px] text-fg-3" title={`Priority: ${priority}`}>
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
          "inline-flex shrink-0 items-center justify-center rounded-full border border-dashed border-line-strong text-fg-4",
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
        "inline-flex shrink-0 items-center justify-center rounded-full font-semibold ring-1",
        isEmoji ? "bg-subtle ring-line" : "bg-accent-soft text-accent ring-accent-line",
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
          <p className="text-xs text-fg-3">The agent picks this up right away.</p>
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
