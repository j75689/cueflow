"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { RunStatusBadge } from "@/components/badges";
import { PostCard } from "@/components/post";
import { useServerEvent } from "@/components/providers";
import { Card, CardHeader, ErrorBox, Loading, PageHeader, cx } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtTime } from "@/lib/format";
import { useAsync, useDebouncedCallback } from "@/lib/hooks";

const STAT_LABEL: Record<string, string> = {
  items_fetched: "Items fetched",
  items_used: "Items used",
  posts_created: "Posts created",
  tool_calls: "Tool calls",
};

function RunView() {
  const id = useSearchParams().get("id") ?? "";
  const run = useAsync(() => api.getRun(id), [id]);
  const posts = useAsync(async () => {
    // run_id filter is server-side; the client-side filter guards older servers.
    const all = await api.listPosts({ run_id: id, limit: 200 });
    return all.items.filter((p) => p.run_id === id);
  }, [id]);

  const refresh = useDebouncedCallback(() => {
    void run.reload();
    void posts.reload();
  }, 300);
  useServerEvent("run.updated", (d) => {
    if (d.id === id) refresh();
  });
  useServerEvent("post.updated", refresh);

  if (!id) return <ErrorBox>Missing run id.</ErrorBox>;
  if (run.error && !run.data) return <ErrorBox onRetry={run.reload}>{run.error}</ErrorBox>;
  if (!run.data) return <Loading />;
  const r = run.data;

  return (
    <div>
      <div className="mb-2">
        <Link href="/runs/" className="text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200">
          ← Runs
        </Link>
      </div>
      <PageHeader
        title={
          <span className="flex flex-wrap items-center gap-2">
            {r.pipeline_name || "Run"} <RunStatusBadge status={r.status} />
          </span>
        }
        subtitle={
          <span className="flex flex-wrap gap-x-3">
            <span>Started {fmtTime(r.started_at)}</span>
            {r.finished_at && <span>Finished {fmtTime(r.finished_at)}</span>}
            <span>Trigger: {r.trigger}</span>
            <Link href={`/schedules/simple/?id=${r.pipeline_id}`} className="hover:underline">
              Pipeline settings
            </Link>
          </span>
        }
      />

      {r.error && (
        <div className="mb-4">
          <ErrorBox>
            <pre className="font-mono text-xs whitespace-pre-wrap">{r.error}</pre>
          </ErrorBox>
        </div>
      )}

      <div className="mb-4 grid grid-cols-2 gap-2 sm:grid-cols-4">
        {Object.entries({ items_fetched: 0, items_used: 0, posts_created: 0, tool_calls: 0, ...(r.stats ?? {}) }).map(
          ([k, v]) => (
            <div key={k} className="rounded-lg border border-zinc-200 bg-white px-3 py-2 dark:border-zinc-800 dark:bg-zinc-900">
              <div className="text-[11px] font-medium text-zinc-500">{STAT_LABEL[k] ?? k}</div>
              <div className="text-lg font-semibold tabular-nums">{v}</div>
            </div>
          ),
        )}
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card className="overflow-hidden">
          <CardHeader title="Log" />
          {!r.log?.length ? (
            <p className="px-4 py-6 text-sm text-zinc-500">{r.status === "RUNNING" ? "Waiting for output…" : "No log lines."}</p>
          ) : (
            <div className="max-h-[60vh] overflow-auto bg-zinc-950 px-4 py-3 font-mono text-xs leading-relaxed text-zinc-300">
              {r.log.map((l, i) => (
                <div key={i} className="flex gap-3">
                  <span className="shrink-0 text-zinc-500">{new Date(l.at).toLocaleTimeString()}</span>
                  <span
                    className={cx(
                      "w-10 shrink-0 uppercase",
                      l.level === "error" ? "text-red-400" : l.level === "warn" ? "text-amber-400" : "text-sky-400",
                    )}
                  >
                    {l.level}
                  </span>
                  <span className="break-words whitespace-pre-wrap">{l.msg}</span>
                </div>
              ))}
            </div>
          )}
        </Card>

        <Card>
          <CardHeader title="Posts from this run" />
          <div className="space-y-2 p-3">
            {posts.data?.length === 0 && <p className="px-1 py-4 text-sm text-zinc-500">No posts created.</p>}
            {(posts.data ?? []).map((p) => (
              <PostCard key={p.id} post={p} compact />
            ))}
          </div>
        </Card>
      </div>
    </div>
  );
}

export default function Page() {
  return (
    <Suspense fallback={<Loading />}>
      <RunView />
    </Suspense>
  );
}
