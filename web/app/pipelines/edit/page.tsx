"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useMemo, useState } from "react";
import { HealthDot, RunStatusBadge } from "@/components/badges";
import { TagInput } from "@/components/inputs";
import { useDialog } from "@/components/dialog";
import { useMeta, useServerEvent, useToast } from "@/components/providers";
import {
  Button,
  Card,
  CardHeader,
  Checkbox,
  ErrorBox,
  Field,
  Icon,
  Input,
  Loading,
  PageHeader,
  Select,
  Textarea,
  Toggle,
} from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative, fmtTime } from "@/lib/format";
import { useAsync, useDebouncedCallback } from "@/lib/hooks";
import type { Connector, PipelineInput } from "@/lib/types";

const CRON_PRESETS: { label: string; value: string }[] = [
  { label: "Every day at 08:00", value: "0 8 * * *" },
  { label: "Weekdays at 09:00", value: "0 9 * * 1-5" },
  { label: "Twice a day (08:00, 20:00)", value: "0 8,20 * * *" },
  { label: "Every 6 hours", value: "0 */6 * * *" },
  { label: "Every hour", value: "@every 1h" },
  { label: "Mondays at 10:00", value: "0 10 * * 1" },
];

const LANGUAGES = ["en", "zh-TW", "zh-CN", "ja", "ko"];

function defaultTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function timezones(): string[] {
  try {
    const fn = (
      Intl as unknown as { supportedValuesOf?: (k: string) => string[] }
    ).supportedValuesOf;
    if (fn) return ["UTC", ...fn("timeZone").filter((z) => z !== "UTC")];
  } catch {
    /* fall through */
  }
  return [
    "UTC",
    "Asia/Taipei",
    "Asia/Tokyo",
    "America/New_York",
    "Europe/London",
  ];
}

const blank = (): PipelineInput => ({
  name: "",
  description: "",
  is_active: true,
  cron_expr: "0 8 * * *",
  timezone: defaultTimezone(),
  topic: "",
  instructions: "",
  language: "en",
  platforms: ["threads", "instagram"],
  llm_connector_id: null,
  source_connector_ids: [],
  search_connector_id: null,
  search_queries: [],
  publisher_connector_id: null,
  notifier_connector_id: null,
  lookback_hours: 24,
  max_items: 20,
});

function ConnectorSelect({
  value,
  onChange,
  options,
  placeholder,
}: {
  value: string | null;
  onChange: (v: string | null) => void;
  options: Connector[];
  placeholder: string;
}) {
  return (
    <Select
      value={value ?? ""}
      onChange={(e) => onChange(e.target.value || null)}
    >
      <option value="">{placeholder}</option>
      {options.map((c) => (
        <option key={c.id} value={c.id}>
          {c.name} ({c.type}){c.is_active ? "" : " – inactive"}
        </option>
      ))}
    </Select>
  );
}

function PipelineEdit() {
  const id = useSearchParams().get("id") ?? "";
  const isNew = !id;
  const router = useRouter();
  const toast = useToast();
  const dialog = useDialog();
  const { meta } = useMeta();

  const connectors = useAsync(() => api.listConnectors(), []);
  const pipeline = useAsync(
    async () => (id ? api.getPipeline(id) : null),
    [id],
  );
  const runs = useAsync(
    async () => (id ? api.listRuns({ pipeline_id: id, limit: 10 }) : []),
    [id],
  );
  const refreshRuns = useDebouncedCallback(() => void runs.reload(), 300);
  useServerEvent("run.updated", (d) => {
    if (d.pipeline_id === id) refreshRuns();
  });

  const [form, setForm] = useState<PipelineInput | null>(
    isNew ? blank() : null,
  );
  const [busy, setBusy] = useState<string | null>(null);
  const tzs = useMemo(timezones, []);

  useEffect(() => {
    if (pipeline.data) {
      const p = pipeline.data;
      setForm({
        name: p.name,
        description: p.description ?? "",
        is_active: p.is_active,
        cron_expr: p.cron_expr,
        timezone: p.timezone,
        topic: p.topic ?? "",
        instructions: p.instructions ?? "",
        language: p.language ?? "en",
        platforms: p.platforms ?? [],
        llm_connector_id: p.llm_connector_id,
        source_connector_ids: p.source_connector_ids ?? [],
        search_connector_id: p.search_connector_id,
        search_queries: p.search_queries ?? [],
        publisher_connector_id: p.publisher_connector_id,
        notifier_connector_id: p.notifier_connector_id,
        lookback_hours: p.lookback_hours,
        max_items: p.max_items,
      });
    }
  }, [pipeline.data]);

  const byKind = useMemo(() => {
    const g: Record<string, Connector[]> = {
      llm: [],
      source: [],
      search: [],
      publisher: [],
      notifier: [],
    };
    for (const c of connectors.data ?? []) (g[c.kind] ??= []).push(c);
    return g;
  }, [connectors.data]);

  // Pick the only LLM automatically for new pipelines.
  useEffect(() => {
    if (isNew && form && !form.llm_connector_id && byKind.llm.length === 1) {
      setForm({ ...form, llm_connector_id: byKind.llm[0].id });
    }
  }, [isNew, form, byKind.llm]);

  if (!isNew && pipeline.error)
    return <ErrorBox onRetry={pipeline.reload}>{pipeline.error}</ErrorBox>;
  if (!form || !meta) return <Loading />;

  const set = <K extends keyof PipelineInput>(k: K, v: PipelineInput[K]) =>
    setForm({ ...form, [k]: v });
  const valid =
    form.name.trim() &&
    form.cron_expr.trim() &&
    form.platforms.length > 0 &&
    form.llm_connector_id;

  const save = async () => {
    setBusy("save");
    try {
      const body = { ...form, name: form.name.trim() };
      if (isNew) {
        const p = await api.createPipeline(body);
        toast.success("Pipeline created");
        router.replace(`/pipelines/edit/?id=${p.id}`);
      } else {
        const p = await api.updatePipeline(id, body);
        pipeline.setData(p);
        toast.success("Saved");
      }
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };

  const runNow = async () => {
    setBusy("run");
    try {
      const { run_id } = await api.runPipeline(id);
      toast.success("Run started");
      router.push(`/runs/view/?id=${run_id}`);
    } catch (e) {
      toast.error(e);
      setBusy(null);
    }
  };

  const remove = async () => {
    if (
      !(await dialog.confirm({
        title: "Delete pipeline?",
        message: "Its schedule stops. Posts it already generated are kept.",
        confirmLabel: "Delete pipeline",
        tone: "danger",
      }))
    )
      return;
    setBusy("delete");
    try {
      await api.deletePipeline(id);
      toast.success("Pipeline deleted");
      router.push("/pipelines/");
    } catch (e) {
      toast.error(e);
      setBusy(null);
    }
  };

  const p = pipeline.data;

  return (
    <div>
      <div className="mb-2">
        <Link
          href="/pipelines/"
          className="text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200"
        >
          ← Pipelines
        </Link>
      </div>
      <PageHeader
        title={isNew ? "New pipeline" : form.name || "Pipeline"}
        subtitle={
          p
            ? `Last run ${fmtRelative(p.last_run_at)} · next ${p.is_active ? fmtTime(p.next_run_at) : "paused"}`
            : "Collect → generate → review, on a schedule."
        }
        actions={
          <>
            {!isNew && (
              <>
                <Button
                  variant="ghost"
                  onClick={remove}
                  loading={busy === "delete"}
                >
                  <Icon name="trash" /> Delete
                </Button>
                <Button onClick={runNow} loading={busy === "run"}>
                  <Icon name="play" className="h-3.5 w-3.5" /> Run now
                </Button>
              </>
            )}
            <Button
              variant="primary"
              onClick={save}
              disabled={!valid}
              loading={busy === "save"}
            >
              {isNew ? "Create pipeline" : "Save"}
            </Button>
          </>
        }
      />

      {connectors.error && (
        <ErrorBox onRetry={connectors.reload}>{connectors.error}</ErrorBox>
      )}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
        <div className="space-y-4">
          <Card>
            <CardHeader
              title="General"
              actions={
                <Toggle
                  checked={form.is_active}
                  onChange={(v) => set("is_active", v)}
                  label="Active"
                />
              }
            />
            <div className="grid gap-4 p-4 md:grid-cols-2">
              <Field label="Name" required className="md:col-span-2">
                <Input
                  value={form.name}
                  onChange={(e) => set("name", e.target.value)}
                  placeholder="Daily AI digest"
                />
              </Field>
              <Field label="Description" className="md:col-span-2">
                <Input
                  value={form.description}
                  onChange={(e) => set("description", e.target.value)}
                />
              </Field>
              <Field
                label="Schedule (cron)"
                required
                help="5-field cron, or @daily / @every 1h. Evaluated in the timezone."
              >
                <div className="flex gap-1.5">
                  <Input
                    className="font-mono"
                    value={form.cron_expr}
                    onChange={(e) => set("cron_expr", e.target.value)}
                  />
                  <Select
                    className="w-10 shrink-0 px-1"
                    value=""
                    aria-label="Cron presets"
                    onChange={(e) =>
                      e.target.value && set("cron_expr", e.target.value)
                    }
                  >
                    <option value="">…</option>
                    {CRON_PRESETS.map((c) => (
                      <option key={c.value} value={c.value}>
                        {c.label} — {c.value}
                      </option>
                    ))}
                  </Select>
                </div>
              </Field>
              <Field label="Timezone">
                <Select
                  value={form.timezone}
                  onChange={(e) => set("timezone", e.target.value)}
                >
                  {!tzs.includes(form.timezone) && (
                    <option value={form.timezone}>{form.timezone}</option>
                  )}
                  {tzs.map((z) => (
                    <option key={z} value={z}>
                      {z}
                    </option>
                  ))}
                </Select>
              </Field>
            </div>
          </Card>

          <Card>
            <CardHeader title="Agent brief" />
            <div className="grid gap-4 p-4 md:grid-cols-2">
              <Field
                label="Topic"
                className="md:col-span-2"
                help="What these posts are about."
              >
                <Input
                  value={form.topic}
                  onChange={(e) => set("topic", e.target.value)}
                  placeholder="AI tooling news for developers"
                />
              </Field>
              <Field
                label="Instructions"
                className="md:col-span-2"
                help="Voice, audience, do's and don'ts. Added to the system prompt."
              >
                <Textarea
                  rows={6}
                  value={form.instructions}
                  onChange={(e) => set("instructions", e.target.value)}
                  placeholder="Write as a senior engineer. Be opinionated but factual. Always cite the source link…"
                />
              </Field>
              <Field label="Language">
                <Select
                  value={form.language}
                  onChange={(e) => set("language", e.target.value)}
                >
                  {!LANGUAGES.includes(form.language) && (
                    <option value={form.language}>{form.language}</option>
                  )}
                  {LANGUAGES.map((l) => (
                    <option key={l} value={l}>
                      {l}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field label="Platforms" required>
                <div className="flex flex-wrap gap-x-4 gap-y-2 pt-1.5">
                  {meta.platforms.map((pl) => (
                    <Checkbox
                      key={pl.id}
                      label={pl.name}
                      checked={form.platforms.includes(pl.id)}
                      onChange={(on) =>
                        set(
                          "platforms",
                          on
                            ? [...form.platforms, pl.id]
                            : form.platforms.filter((x) => x !== pl.id),
                        )
                      }
                    />
                  ))}
                </div>
              </Field>
              <Field label="LLM" required>
                <ConnectorSelect
                  value={form.llm_connector_id}
                  onChange={(v) => set("llm_connector_id", v)}
                  options={byKind.llm}
                  placeholder={
                    byKind.llm.length
                      ? "Select an LLM…"
                      : "No LLM connectors — add one first"
                  }
                />
              </Field>
            </div>
          </Card>

          <Card>
            <CardHeader
              title="Inputs"
              subtitle="Material the agent reads before writing."
            />
            <div className="grid gap-4 p-4 md:grid-cols-2">
              <Field label="Sources" className="md:col-span-2">
                {byKind.source.length === 0 ? (
                  <p className="text-sm text-zinc-500">
                    No source connectors.{" "}
                    <Link
                      href="/connectors/edit/"
                      className="text-indigo-600 hover:underline dark:text-indigo-400"
                    >
                      Add RSS or IMAP
                    </Link>
                  </p>
                ) : (
                  <div className="grid gap-1.5 pt-1 sm:grid-cols-2">
                    {byKind.source.map((c) => (
                      <div key={c.id} className="flex items-center gap-2">
                        <Checkbox
                          label={
                            <span className="flex items-center gap-1.5">
                              {c.name}{" "}
                              <span className="text-xs text-zinc-400">
                                {c.type}
                              </span>
                            </span>
                          }
                          checked={form.source_connector_ids.includes(c.id)}
                          onChange={(on) =>
                            set(
                              "source_connector_ids",
                              on
                                ? [...form.source_connector_ids, c.id]
                                : form.source_connector_ids.filter(
                                    (x) => x !== c.id,
                                  ),
                            )
                          }
                        />
                        <HealthDot status={c.last_status} />
                      </div>
                    ))}
                  </div>
                )}
              </Field>
              <Field
                label="Look-back (hours)"
                help="Only items newer than this are considered."
              >
                <Input
                  type="number"
                  min={1}
                  value={form.lookback_hours}
                  onChange={(e) =>
                    set("lookback_hours", Number(e.target.value) || 1)
                  }
                />
              </Field>
              <Field
                label="Max items"
                help="Cap on items passed to the LLM per run."
              >
                <Input
                  type="number"
                  min={1}
                  value={form.max_items}
                  onChange={(e) =>
                    set("max_items", Number(e.target.value) || 1)
                  }
                />
              </Field>
              <Field label="Web search">
                <ConnectorSelect
                  value={form.search_connector_id}
                  onChange={(v) => set("search_connector_id", v)}
                  options={byKind.search}
                  placeholder="None"
                />
              </Field>
              <Field
                label="Search queries"
                help="The agent may also search on its own."
              >
                <TagInput
                  value={form.search_queries}
                  onChange={(v) => set("search_queries", v)}
                  placeholder="LLM agents, MCP…"
                  disabled={!form.search_connector_id}
                />
              </Field>
            </div>
          </Card>

          <Card>
            <CardHeader title="Outputs" />
            <div className="grid gap-4 p-4 md:grid-cols-2">
              <Field
                label="Publisher"
                help="Used after approval. Empty = first active publisher."
              >
                <ConnectorSelect
                  value={form.publisher_connector_id}
                  onChange={(v) => set("publisher_connector_id", v)}
                  options={byKind.publisher}
                  placeholder="Default publisher"
                />
              </Field>
              <Field
                label="Notify reviewers"
                help="Send an email when new drafts await review."
              >
                <ConnectorSelect
                  value={form.notifier_connector_id}
                  onChange={(v) => set("notifier_connector_id", v)}
                  options={byKind.notifier}
                  placeholder="Don't notify"
                />
              </Field>
            </div>
          </Card>
        </div>

        <div>
          {!isNew && (
            <Card className="lg:sticky lg:top-4">
              <CardHeader
                title="Recent runs"
                actions={
                  <Link
                    href={`/runs/?pipeline_id=${id}`}
                    className="text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200"
                  >
                    All
                  </Link>
                }
              />
              {runs.data && runs.data.length === 0 && (
                <p className="px-4 py-6 text-sm text-zinc-500">No runs yet.</p>
              )}
              <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
                {(runs.data ?? []).map((r) => (
                  <li key={r.id}>
                    <Link
                      href={`/runs/view/?id=${r.id}`}
                      className="flex items-center justify-between gap-2 px-4 py-2.5 hover:bg-zinc-50 dark:hover:bg-zinc-800/40"
                    >
                      <div className="min-w-0">
                        <div
                          className="text-xs text-zinc-700 dark:text-zinc-300"
                          title={fmtTime(r.started_at)}
                        >
                          {fmtRelative(r.started_at)} · {r.trigger}
                        </div>
                        <div className="text-[11px] text-zinc-500">
                          {r.stats?.posts_created ?? 0} posts ·{" "}
                          {r.stats?.items_used ?? 0} items
                        </div>
                      </div>
                      <RunStatusBadge status={r.status} />
                    </Link>
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </div>
      </div>
    </div>
  );
}

export default function Page() {
  return (
    <Suspense fallback={<Loading />}>
      <PipelineEdit />
    </Suspense>
  );
}
