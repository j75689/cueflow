"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import {
  AgentAvatar,
  AgentSelect,
  CRON_PRESETS,
  PRIORITIES,
  agentRef,
  defaultTimezone,
  timezones,
  useAgents,
} from "@/components/agents";
import { useServerEvent, useToast } from "@/components/providers";
import {
  Button,
  Card,
  EmptyState,
  ErrorBox,
  Field,
  Icon,
  Input,
  Loading,
  Modal,
  PageHeader,
  Pill,
  Select,
  Textarea,
  Toggle,
} from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative, fmtTime } from "@/lib/format";
import { useAsync } from "@/lib/hooks";
import type { Schedule, ScheduleInput, TaskPriority } from "@/lib/types";

const blank = (): ScheduleInput => ({
  name: "",
  cron_expr: "0 8 * * *",
  timezone: defaultTimezone(),
  is_active: true,
  assignee_agent_id: null,
  task_title: "",
  task_description: "",
  priority: "normal",
});

export default function SchedulesPage() {
  const toast = useToast();
  const router = useRouter();
  const agents = useAgents();
  const schedules = useAsync(() => api.listSchedules(), []);
  useServerEvent("schedule.updated", () => void schedules.reload());
  const [editing, setEditing] = useState<Schedule | "new" | null>(null);

  const toggle = async (s: Schedule, v: boolean) => {
    try {
      await api.updateSchedule(s.id, { is_active: v });
      void schedules.reload();
    } catch (e) {
      toast.error(e);
    }
  };
  const runNow = async (s: Schedule) => {
    try {
      const { task_id } = await api.runSchedule(s.id);
      toast.success("Task created");
      router.push(`/tasks/view/?id=${task_id}`);
    } catch (e) {
      toast.error(e);
    }
  };
  const remove = async (s: Schedule) => {
    if (!window.confirm(`Delete schedule “${s.name}”?`)) return;
    try {
      await api.deleteSchedule(s.id);
      void schedules.reload();
    } catch (e) {
      toast.error(e);
    }
  };

  const list = schedules.data ?? [];

  return (
    <div>
      <PageHeader
        title="Schedules"
        subtitle="Recurring tasks for your agents. Manager agents create these when you ask for something “every day”."
        actions={
          <Button variant="primary" onClick={() => setEditing("new")}>
            <Icon name="plus" className="h-4 w-4" /> New schedule
          </Button>
        }
      />
      {schedules.error && <ErrorBox onRetry={schedules.reload}>{schedules.error}</ErrorBox>}
      {schedules.loading && !schedules.data ? (
        <Loading />
      ) : list.length === 0 ? (
        <Card>
          <EmptyState
            title="No schedules yet"
            hint="Create one here, or tell a manager agent in Command: 'Every morning at 8, summarize AI news for me'."
            action={
              <Link href="/command/">
                <Button variant="secondary">Open Command</Button>
              </Link>
            }
          />
        </Card>
      ) : (
        <Card>
          <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {list.map((s) => {
              const a = agentRef(agents.data, s.assignee_agent_id);
              return (
                <li key={s.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                  <Toggle checked={s.is_active} onChange={(v) => void toggle(s, v)} />
                  <AgentAvatar agent={a} size="sm" />
                  <div className="min-w-0 flex-1">
                    <p className="flex flex-wrap items-center gap-2 text-sm font-medium text-zinc-900 dark:text-zinc-100">
                      <button onClick={() => setEditing(s)} className="truncate hover:underline">
                        {s.name || s.task_title}
                      </button>
                      {s.created_by === "agent" && (
                        <Pill className="bg-violet-100 text-violet-800 dark:bg-violet-950 dark:text-violet-300">created by agent</Pill>
                      )}
                    </p>
                    <p className="truncate text-xs text-zinc-500">
                      <code className="font-mono">{s.cron_expr}</code> ({s.timezone}) → “{s.task_title}” for {a?.name ?? "unassigned"}
                    </p>
                  </div>
                  <div className="text-right text-xs text-zinc-500">
                    <p title={fmtTime(s.next_run_at)}>{s.is_active && s.next_run_at ? `next ${fmtRelative(s.next_run_at)}` : "paused"}</p>
                    <p>last {fmtRelative(s.last_run_at)}</p>
                  </div>
                  <div className="flex gap-1.5">
                    <Button size="sm" variant="secondary" onClick={() => void runNow(s)}>
                      <Icon name="play" className="h-3.5 w-3.5" /> Run now
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setEditing(s)}>
                      Edit
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => void remove(s)} aria-label="Delete">
                      <Icon name="trash" className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </li>
              );
            })}
          </ul>
        </Card>
      )}

      <ScheduleModal
        open={editing !== null}
        schedule={editing === "new" ? null : editing}
        agents={agents.data}
        onClose={() => setEditing(null)}
        onSaved={() => {
          setEditing(null);
          void schedules.reload();
        }}
      />
    </div>
  );
}

function ScheduleModal({
  open,
  schedule,
  agents,
  onClose,
  onSaved,
}: {
  open: boolean;
  schedule: Schedule | null;
  agents: Parameters<typeof AgentSelect>[0]["agents"];
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [form, setForm] = useState<ScheduleInput>(blank);
  const [busy, setBusy] = useState(false);
  const tzs = useMemo(() => timezones(), []);
  const key = open ? (schedule?.id ?? "new") : "closed";
  const [loadedKey, setLoadedKey] = useState("");
  if (key !== loadedKey) {
    setLoadedKey(key);
    if (open) {
      setForm(
        schedule
          ? {
              name: schedule.name,
              cron_expr: schedule.cron_expr,
              timezone: schedule.timezone,
              is_active: schedule.is_active,
              assignee_agent_id: schedule.assignee_agent_id,
              task_title: schedule.task_title,
              task_description: schedule.task_description,
              priority: schedule.priority,
            }
          : blank(),
      );
    }
  }
  const set = <K extends keyof ScheduleInput>(k: K, v: ScheduleInput[K]) => setForm((f) => ({ ...f, [k]: v }));
  const valid = form.cron_expr.trim() && form.task_title.trim() && form.assignee_agent_id;

  const save = async () => {
    if (!valid) return;
    setBusy(true);
    const body = { ...form, name: form.name.trim() || form.task_title.trim() };
    try {
      if (schedule) await api.updateSchedule(schedule.id, body);
      else await api.createSchedule(body);
      toast.success("Schedule saved");
      onSaved();
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
      wide
      title={schedule ? "Edit schedule" : "New schedule"}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!valid} onClick={save}>
            Save
          </Button>
        </>
      }
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Name" className="sm:col-span-2">
          <Input value={form.name} onChange={(e) => set("name", e.target.value)} placeholder="Morning AI digest" />
        </Field>
        <Field label="Cron" required help="5 fields, or @daily / @every 2h. At most every 5 minutes.">
          <Input value={form.cron_expr} onChange={(e) => set("cron_expr", e.target.value)} className="font-mono" />
        </Field>
        <Field label="Preset">
          <Select value="" onChange={(e) => e.target.value && set("cron_expr", e.target.value)}>
            <option value="">Choose a preset…</option>
            {CRON_PRESETS.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Timezone">
          <Select value={form.timezone} onChange={(e) => set("timezone", e.target.value)}>
            {[...new Set([form.timezone, ...tzs])].map((z) => (
              <option key={z} value={z}>
                {z}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Assign to" required>
          <AgentSelect agents={agents} value={form.assignee_agent_id} onChange={(v) => set("assignee_agent_id", v)} noneLabel="Choose an agent…" />
        </Field>
        <Field label="Task title" required className="sm:col-span-2">
          <Input value={form.task_title} onChange={(e) => set("task_title", e.target.value)} placeholder="Summarize today's AI news and draft a Threads post" />
        </Field>
        <Field label="Task description" className="sm:col-span-2">
          <Textarea rows={4} value={form.task_description} onChange={(e) => set("task_description", e.target.value)} />
        </Field>
        <Field label="Priority">
          <Select value={form.priority} onChange={(e) => set("priority", e.target.value as TaskPriority)}>
            {PRIORITIES.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </Select>
        </Field>
        <div className="flex items-end">
          <Toggle checked={form.is_active} onChange={(v) => set("is_active", v)} label="Active" />
        </div>
      </div>
    </Modal>
  );
}
