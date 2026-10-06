"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { RunStatusBadge } from "@/components/badges";
import { useServerEvent } from "@/components/providers";
import { Card, EmptyState, ErrorBox, Loading, PageHeader, Select } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative, fmtTime } from "@/lib/format";
import { useAsync, useDebouncedCallback } from "@/lib/hooks";

function duration(a: string, b: string | null): string {
  if (!b) return "…";
  const s = Math.max(0, (new Date(b).getTime() - new Date(a).getTime()) / 1000);
  return s < 60 ? `${s.toFixed(1)}s` : `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
}

function RunsList() {
  const params = useSearchParams();
  const router = useRouter();
  const pipelineId = params.get("pipeline_id") ?? "";
  const pipelines = useAsync(() => api.listPipelines(), []);
  const runs = useAsync(() => api.listRuns({ pipeline_id: pipelineId || undefined, limit: 100 }), [pipelineId]);
  const refresh = useDebouncedCallback(() => void runs.reload(), 300);
  useServerEvent("run.updated", refresh);

  return (
    <div>
      <PageHeader
        title="Runs"
        subtitle="History of simple schedule runs. Agent tasks keep their history on the board."
        actions={
          <Select
            className="w-56"
            value={pipelineId}
            onChange={(e) => router.replace(e.target.value ? `/runs/?pipeline_id=${e.target.value}` : "/runs/")}
          >
            <option value="">All simple schedules</option>
            {(pipelines.data ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </Select>
        }
      />
      {runs.error && <ErrorBox onRetry={runs.reload}>{runs.error}</ErrorBox>}
      {runs.loading && !runs.data && <Loading />}
      {runs.data && (
        <Card className="overflow-hidden">
          {runs.data.length === 0 ? (
            <EmptyState title="No runs yet" hint="Runs appear here when a simple schedule fires or you press Run now." />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[720px] text-sm">
                <thead className="bg-zinc-50 text-left text-xs text-zinc-500 dark:bg-zinc-900/60">
                  <tr>
                    <th className="px-4 py-2 font-medium">Schedule</th>
                    <th className="px-4 py-2 font-medium">Status</th>
                    <th className="px-4 py-2 font-medium">Trigger</th>
                    <th className="px-4 py-2 font-medium">Started</th>
                    <th className="px-4 py-2 font-medium">Duration</th>
                    <th className="px-4 py-2 text-right font-medium">Items</th>
                    <th className="px-4 py-2 text-right font-medium">Posts</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
                  {runs.data.map((r) => (
                    <tr
                      key={r.id}
                      className="cursor-pointer hover:bg-zinc-50 dark:hover:bg-zinc-800/40"
                      onClick={() => router.push(`/runs/view/?id=${r.id}`)}
                    >
                      <td className="px-4 py-2.5">
                        <Link
                          href={`/runs/view/?id=${r.id}`}
                          className="font-medium text-zinc-900 hover:text-indigo-600 dark:text-zinc-100"
                          onClick={(e) => e.stopPropagation()}
                        >
                          {r.pipeline_name || r.pipeline_id.slice(0, 8)}
                        </Link>
                        {r.error && <div className="line-clamp-1 max-w-xs text-xs text-red-600 dark:text-red-400">{r.error}</div>}
                      </td>
                      <td className="px-4 py-2.5">
                        <RunStatusBadge status={r.status} />
                      </td>
                      <td className="px-4 py-2.5 text-zinc-600 dark:text-zinc-400">{r.trigger}</td>
                      <td className="px-4 py-2.5 text-zinc-600 dark:text-zinc-400" title={fmtTime(r.started_at)}>
                        {fmtRelative(r.started_at)}
                      </td>
                      <td className="px-4 py-2.5 tabular-nums text-zinc-600 dark:text-zinc-400">
                        {duration(r.started_at, r.finished_at)}
                      </td>
                      <td className="px-4 py-2.5 text-right tabular-nums">
                        {r.stats?.items_used ?? 0}
                        <span className="text-zinc-400">/{r.stats?.items_fetched ?? 0}</span>
                      </td>
                      <td className="px-4 py-2.5 text-right font-medium tabular-nums">{r.stats?.posts_created ?? 0}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}
    </div>
  );
}

export default function Page() {
  return (
    <Suspense fallback={<Loading />}>
      <RunsList />
    </Suspense>
  );
}
