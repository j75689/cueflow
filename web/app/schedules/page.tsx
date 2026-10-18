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
import { PlatformBadge } from "@/components/badges";
import { useDialog } from "@/components/dialog";
import { PublishTarget } from "@/components/publish-target";
import { useModule, useMeta, useServerEvent, useToast } from "@/components/providers";
import {
  Button,
  Card,
  CardHeader,
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
import { useAsync, useDebouncedCallback } from "@/lib/hooks";
import type {
  Pipeline,
  Schedule,
  ScheduleInput,
  TaskPriority,
} from "@/lib/types";

const blank = (): ScheduleInput => ({
  name: "",
  cron_expr: "0 8 * * *",
  timezone: defaultTimezone(),
  is_active: true,
  assignee_agent_id: null,
  task_title: "",
  task_description: "",
  priority: "normal",
  publisher_connector_id: null,
  publish_accounts: [],
});

export default function SchedulesPage() {
  const toast = useToast();
  const dialog = useDialog();
  const router = useRouter();
  const agents = useAgents();
  const schedules = useAsync(() => api.listSchedules(), []);
  const hasContent = useModule("content");
  const pipelines = useAsync(() => (hasContent ? api.listPipelines() : Promise.resolve([])), [hasContent]);
  useServerEvent("schedule.updated", () => void schedules.reload());
  const refreshSimple = useDebouncedCallback(
    () => void pipelines.reload(),
    400,
  );
  useServerEvent("run.updated", refreshSimple);
  const [editing, setEditing] = useState<Schedule | "new" | null>(null);
  const [choosing, setChoosing] = useState(false);

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
    if (
      !(await dialog.confirm({
        title: `Delete schedule “${s.name}”?`,
        message:
          "No more tasks will be created from it. Existing tasks are kept.",
        confirmLabel: "Delete schedule",
        tone: "danger",
      }))
    )
      return;
    try {
      await api.deleteSchedule(s.id);
      void schedules.reload();
    } catch (e) {
      toast.error(e);
    }
  };

  const list = schedules.data ?? [];
  const simple = pipelines.data ?? [];

  return (
    <div>
      <PageHeader
        title="Schedules"
        subtitle={
          hasContent
            ? "Work that runs on a timer: agent tasks (an agent researches and decides) or simple schedules (a fixed collect → draft flow)."
            : "Work that runs on a timer: each activation opens a task for an agent."
        }
        actions={
          <Button variant="primary" onClick={() => setChoosing(true)}>
            <Icon name="plus" className="h-4 w-4" /> New schedule
          </Button>
        }
      />
      {schedules.error && (
        <ErrorBox onRetry={schedules.reload}>{schedules.error}</ErrorBox>
      )}
      {pipelines.error && (
        <ErrorBox onRetry={pipelines.reload}>{pipelines.error}</ErrorBox>
      )}
      {(schedules.loading && !schedules.data) ||
      (pipelines.loading && !pipelines.data) ? (
        <Loading />
      ) : list.length === 0 && simple.length === 0 ? (
        <Card>
          <EmptyState
            title="No schedules yet"
            hint="Create one with New schedule, or tell a manager agent in Command: 'Every morning at 8, summarize AI news for me'."
            action={
              <Link href="/command/">
                <Button variant="secondary">Open Command</Button>
              </Link>
            }
          />
        </Card>
      ) : (
        <div className="space-y-4">
          {list.length > 0 && (
            <Card>
              <CardHeader
                title="Agent tasks"
                subtitle="Each run opens a task for an agent, which researches, decides and drafts. Uses more tokens."
              />
              <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
                {list.map((s) => {
                  const a = agentRef(agents.data, s.assignee_agent_id);
                  return (
                    <li
                      key={s.id}
                      className="flex flex-wrap items-center gap-3 px-4 py-3"
                    >
                      <Toggle
                        checked={s.is_active}
                        onChange={(v) => void toggle(s, v)}
                      />
                      <AgentAvatar agent={a} size="sm" />
                      <div className="min-w-0 flex-1">
                        <p className="flex flex-wrap items-center gap-2 text-sm font-medium text-zinc-900 dark:text-zinc-100">
                          <button
                            onClick={() => setEditing(s)}
                            className="truncate hover:underline"
                          >
                            {s.name || s.task_title}
                          </button>
                          {s.created_by === "agent" && (
                            <Pill className="bg-violet-100 text-violet-800 dark:bg-violet-950 dark:text-violet-300">
                              created by agent
                            </Pill>
                          )}
                        </p>
                        <p className="truncate text-xs text-zinc-500">
                          <code className="font-mono">{s.cron_expr}</code> (
                          {s.timezone}) → “{s.task_title}” for{" "}
                          {a?.name ?? "unassigned"}
                        </p>
                      </div>
                      <div className="text-right text-xs text-zinc-500">
                        <p title={fmtTime(s.next_run_at)}>
                          {s.is_active && s.next_run_at
                            ? `next ${fmtRelative(s.next_run_at)}`
                            : "paused"}
                        </p>
                        <p>last {fmtRelative(s.last_run_at)}</p>
                      </div>
                      <div className="flex gap-1.5">
                        <Button
                          size="sm"
                          variant="secondary"
                          onClick={() => void runNow(s)}
                        >
                          <Icon name="play" className="h-3.5 w-3.5" /> Run now
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => setEditing(s)}
                        >
                          Edit
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => void remove(s)}
                          aria-label="Delete"
                        >
                          <Icon name="trash" className="h-3.5 w-3.5" />
                        </Button>
                      </div>
                    </li>
                  );
                })}
              </ul>
            </Card>
          )}
          {simple.length > 0 && (
            <SimpleSchedules
              items={simple}
              onChanged={(next) => pipelines.setData(next)}
            />
          )}
        </div>
      )}

      <Modal
        open={choosing}
        onClose={() => setChoosing(false)}
        title="New schedule"
      >
        <div className="grid gap-3 sm:grid-cols-2">
          <button
            className="rounded-lg border border-zinc-200 p-4 text-left hover:border-indigo-400 hover:bg-indigo-50/50 dark:border-zinc-700 dark:hover:border-indigo-500/60 dark:hover:bg-indigo-500/5"
            onClick={() => {
              setChoosing(false);
              setEditing("new");
            }}
          >
            <p className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">
              Agent task
            </p>
            <p className="mt-1 text-xs text-zinc-500">
              Opens a task for an agent each time. It can search, read
              originals, avoid repeats, add images and decide whether to post.
              More tokens.
            </p>
          </button>
          {hasContent && (
            <button
              className="rounded-lg border border-zinc-200 p-4 text-left hover:border-indigo-400 hover:bg-indigo-50/50 dark:border-zinc-700 dark:hover:border-indigo-500/60 dark:hover:bg-indigo-500/5"
              onClick={() => {
                setChoosing(false);
                router.push("/schedules/simple/");
              }}
            >
              <p className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">
                Simple schedule
              </p>
              <p className="mt-1 text-xs text-zinc-500">
                A fixed flow: collect from your sources and search, then one LLM
                call drafts posts for review. Cheaper and predictable.
              </p>
            </button>
          )}
        </div>
      </Modal>

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

/** Simple schedules (stored as pipelines): collect → one LLM call → drafts, on a cron. */
function SimpleSchedules({
  items,
  onChanged,
}: {
  items: Pipeline[];
  onChanged: (next: Pipeline[]) => void;
}) {
  const toast = useToast();
  const dialog = useDialog();
  const { meta } = useMeta();
  const [busy, setBusy] = useState<string | null>(null);
  const pm = Object.fromEntries(
    (meta?.platforms ?? []).map((p) => [p.id, p.name]),
  );

  const toggle = async (p: Pipeline, on: boolean) => {
    try {
      const updated = await api.updatePipeline(p.id, { is_active: on });
      onChanged(items.map((x) => (x.id === p.id ? updated : x)));
    } catch (e) {
      toast.error(e);
    }
  };
  const run = async (p: Pipeline) => {
    setBusy(p.id);
    try {
      await api.runPipeline(p.id);
      toast.success("Run started; drafts will appear in Review");
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };
  const remove = async (p: Pipeline) => {
    if (
      !(await dialog.confirm({
        title: `Delete schedule “${p.name}”?`,
        message: "It stops running. Posts it already generated are kept.",
        confirmLabel: "Delete schedule",
        tone: "danger",
      }))
    )
      return;
    try {
      await api.deletePipeline(p.id);
      onChanged(items.filter((x) => x.id !== p.id));
    } catch (e) {
      toast.error(e);
    }
  };

  return (
    <Card>
      <CardHeader
        title="Simple schedules"
        subtitle="Each run collects from the sources and search, then one LLM call drafts posts for review. Fewer tokens."
      />
      <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
        {items.map((p) => (
          <li
            key={p.id}
            className="flex flex-wrap items-center gap-3 px-4 py-3"
          >
            <Toggle checked={p.is_active} onChange={(v) => void toggle(p, v)} />
            <div className="min-w-0 flex-1">
              <p className="flex flex-wrap items-center gap-2 text-sm font-medium text-zinc-900 dark:text-zinc-100">
                <Link
                  href={`/schedules/simple/?id=${p.id}`}
                  className="truncate hover:underline"
                >
                  {p.name}
                </Link>
                {p.platforms.map((x) => (
                  <PlatformBadge key={x} id={x} name={pm[x]} />
                ))}
              </p>
              <p className="truncate text-xs text-zinc-500">
                <code className="font-mono">{p.cron_expr}</code> ({p.timezone})
                {p.topic ? ` → ${p.topic}` : ""} ·{" "}
                {p.source_connector_ids.length} source
                {p.source_connector_ids.length === 1 ? "" : "s"}
                {p.search_connector_id ? " + search" : ""}
              </p>
            </div>
            <div className="text-right text-xs text-zinc-500">
              <p title={fmtTime(p.next_run_at)}>
                {p.is_active && p.next_run_at
                  ? `next ${fmtRelative(p.next_run_at)}`
                  : "paused"}
              </p>
              <p>
                <Link
                  href={`/runs/?pipeline_id=${p.id}`}
                  className="hover:underline"
                >
                  last {fmtRelative(p.last_run_at)}
                </Link>
              </p>
            </div>
            <div className="flex gap-1.5">
              <Button
                size="sm"
                variant="secondary"
                onClick={() => void run(p)}
                loading={busy === p.id}
              >
                <Icon name="play" className="h-3.5 w-3.5" /> Run now
              </Button>
              <Link href={`/schedules/simple/?id=${p.id}`}>
                <Button size="sm" variant="ghost">
                  Edit
                </Button>
              </Link>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => void remove(p)}
                aria-label="Delete"
              >
                <Icon name="trash" className="h-3.5 w-3.5" />
              </Button>
            </div>
          </li>
        ))}
      </ul>
    </Card>
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
              publisher_connector_id: schedule.publisher_connector_id ?? null,
              publish_accounts: schedule.publish_accounts ?? [],
            }
          : blank(),
      );
    }
  }
  const set = <K extends keyof ScheduleInput>(k: K, v: ScheduleInput[K]) =>
    setForm((f) => ({ ...f, [k]: v }));
  const valid =
    form.cron_expr.trim() && form.task_title.trim() && form.assignee_agent_id;

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
          <Button
            variant="primary"
            loading={busy}
            disabled={!valid}
            onClick={save}
          >
            Save
          </Button>
        </>
      }
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Name" className="sm:col-span-2">
          <Input
            value={form.name}
            onChange={(e) => set("name", e.target.value)}
            placeholder="Morning AI digest"
          />
        </Field>
        <Field
          label="Cron"
          required
          help="5 fields, or @daily / @every 2h. At most every 5 minutes."
        >
          <Input
            value={form.cron_expr}
            onChange={(e) => set("cron_expr", e.target.value)}
            className="font-mono"
          />
        </Field>
        <Field label="Preset">
          <Select
            value=""
            onChange={(e) => e.target.value && set("cron_expr", e.target.value)}
          >
            <option value="">Choose a preset…</option>
            {CRON_PRESETS.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Timezone">
          <Select
            value={form.timezone}
            onChange={(e) => set("timezone", e.target.value)}
          >
            {[...new Set([form.timezone, ...tzs])].map((z) => (
              <option key={z} value={z}>
                {z}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Assign to" required>
          <AgentSelect
            agents={agents}
            value={form.assignee_agent_id}
            onChange={(v) => set("assignee_agent_id", v)}
            noneLabel="Choose an agent…"
          />
        </Field>
        <Field label="Task title" required className="sm:col-span-2">
          <Input
            value={form.task_title}
            onChange={(e) => set("task_title", e.target.value)}
            placeholder="Summarize today's AI news and draft a Threads post"
          />
        </Field>
        <Field label="Task description" className="sm:col-span-2">
          <Textarea
            rows={4}
            value={form.task_description}
            onChange={(e) => set("task_description", e.target.value)}
          />
        </Field>
        <Field label="Priority">
          <Select
            value={form.priority}
            onChange={(e) => set("priority", e.target.value as TaskPriority)}
          >
            {PRIORITIES.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </Select>
        </Field>
        <div className="sm:col-span-2">
          <PublishTarget
            publisherId={form.publisher_connector_id}
            accounts={form.publish_accounts}
            onChange={(pub, accts) =>
              setForm((f) => ({
                ...f,
                publisher_connector_id: pub,
                publish_accounts: accts,
              }))
            }
          />
        </div>
        <div className="flex items-end">
          <Toggle
            checked={form.is_active}
            onChange={(v) => set("is_active", v)}
            label="Active"
          />
        </div>
      </div>
    </Modal>
  );
}
