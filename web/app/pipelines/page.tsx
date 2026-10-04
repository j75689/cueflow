"use client";

import Link from "next/link";
import { useState } from "react";
import { PlatformBadge } from "@/components/badges";
import { useMeta, useServerEvent, useToast } from "@/components/providers";
import { Button, Card, EmptyState, ErrorBox, Icon, Loading, PageHeader, Pill, Toggle } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative, fmtTime } from "@/lib/format";
import { useAsync, useDebouncedCallback } from "@/lib/hooks";
import type { Pipeline } from "@/lib/types";

export default function PipelinesPage() {
  const toast = useToast();
  const { meta } = useMeta();
  const pipelines = useAsync(() => api.listPipelines(), []);
  const [busy, setBusy] = useState<string | null>(null);
  const refresh = useDebouncedCallback(() => void pipelines.reload(), 400);
  useServerEvent("run.updated", refresh);

  const pm = Object.fromEntries((meta?.platforms ?? []).map((p) => [p.id, p.name]));

  const run = async (p: Pipeline) => {
    setBusy(p.id);
    try {
      const { run_id } = await api.runPipeline(p.id);
      toast.success(`Run started (#${run_id.slice(0, 8)})`);
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };

  const toggle = async (p: Pipeline, on: boolean) => {
    try {
      const updated = await api.updatePipeline(p.id, { is_active: on });
      pipelines.setData((pipelines.data ?? []).map((x) => (x.id === p.id ? updated : x)));
      toast.success(on ? "Pipeline enabled" : "Pipeline paused");
    } catch (e) {
      toast.error(e);
    }
  };

  return (
    <div>
      <PageHeader
        title="Pipelines"
        subtitle="Scheduled agent jobs: collect sources → generate drafts → send to review."
        actions={
          <Link href="/pipelines/edit/">
            <Button variant="primary">
              <Icon name="plus" /> New pipeline
            </Button>
          </Link>
        }
      />
      {pipelines.error && <ErrorBox onRetry={pipelines.reload}>{pipelines.error}</ErrorBox>}
      {pipelines.loading && !pipelines.data && <Loading />}
      {pipelines.data && pipelines.data.length === 0 && (
        <Card>
          <EmptyState
            title="No pipelines yet"
            hint="A pipeline wakes up on a cron schedule, pulls RSS / email / search results, and asks the LLM to draft posts for review. Set up an LLM connector first."
            action={
              <div className="flex gap-2">
                <Link href="/connectors/">
                  <Button size="sm">Connectors</Button>
                </Link>
                <Link href="/pipelines/edit/">
                  <Button size="sm" variant="primary">
                    New pipeline
                  </Button>
                </Link>
              </div>
            }
          />
        </Card>
      )}
      {pipelines.data && pipelines.data.length > 0 && (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {pipelines.data.map((p) => (
            <Card key={p.id} className="flex flex-col">
              <div className="flex-1 px-4 pt-3.5 pb-3">
                <div className="flex items-start justify-between gap-2">
                  <Link
                    href={`/pipelines/edit/?id=${p.id}`}
                    className="min-w-0 text-sm font-semibold text-zinc-900 hover:text-indigo-600 dark:text-zinc-100 dark:hover:text-indigo-400"
                  >
                    <span className="line-clamp-1">{p.name}</span>
                  </Link>
                  <Toggle checked={p.is_active} onChange={(on) => toggle(p, on)} />
                </div>
                {p.topic && <p className="mt-1 line-clamp-2 text-sm text-zinc-600 dark:text-zinc-400">{p.topic}</p>}
                <div className="mt-2.5 flex flex-wrap gap-1.5">
                  <Pill className="bg-zinc-100 font-mono text-zinc-700 dark:bg-zinc-800 dark:text-zinc-300">
                    {p.cron_expr}
                  </Pill>
                  <Pill>{p.timezone}</Pill>
                  {p.platforms.map((x) => (
                    <PlatformBadge key={x} id={x} name={pm[x]} />
                  ))}
                </div>
                <dl className="mt-3 grid grid-cols-2 gap-x-3 gap-y-1 text-xs">
                  <dt className="text-zinc-500">Last run</dt>
                  <dd className="text-right text-zinc-700 dark:text-zinc-300" title={fmtTime(p.last_run_at)}>
                    {fmtRelative(p.last_run_at)}
                  </dd>
                  <dt className="text-zinc-500">Next run</dt>
                  <dd className="text-right text-zinc-700 dark:text-zinc-300" title={fmtTime(p.next_run_at)}>
                    {p.is_active ? fmtRelative(p.next_run_at) : "paused"}
                  </dd>
                  <dt className="text-zinc-500">Sources</dt>
                  <dd className="text-right text-zinc-700 dark:text-zinc-300">
                    {p.source_connector_ids.length}
                    {p.search_connector_id ? " + search" : ""}
                  </dd>
                </dl>
              </div>
              <div className="flex items-center justify-between gap-2 border-t border-zinc-200 px-4 py-2.5 dark:border-zinc-800">
                <Link href={`/runs/?pipeline_id=${p.id}`} className="text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200">
                  View runs
                </Link>
                <div className="flex gap-1.5">
                  <Link href={`/pipelines/edit/?id=${p.id}`}>
                    <Button size="sm" variant="ghost">
                      Edit
                    </Button>
                  </Link>
                  <Button size="sm" onClick={() => run(p)} loading={busy === p.id}>
                    <Icon name="play" className="h-3 w-3" /> Run now
                  </Button>
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
