"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { STATUS_LABEL, STATUS_STYLE } from "@/components/badges";
import { PostCard, RejectDialog } from "@/components/post";
import { useServerEvent, useToast } from "@/components/providers";
import { Button, EmptyState, ErrorBox, Icon, Input, Loading, PageHeader, cx } from "@/components/ui";
import { api } from "@/lib/api";
import { useAsync, useDebouncedCallback } from "@/lib/hooks";
import type { Post, PostStatus } from "@/lib/types";

type View = "inbox" | "kanban";
const VIEW_KEY = "cueflow.reviewView";

const COLUMNS: { key: string; title: string; statuses: PostStatus[] }[] = [
  { key: "review", title: "Pending review", statuses: ["PENDING_REVIEW"] },
  { key: "queue", title: "Approved / Queued", statuses: ["APPROVED", "QUEUED"] },
  { key: "published", title: "Published", statuses: ["PUBLISHED"] },
  { key: "failed", title: "Failed", statuses: ["FAILED"] },
  { key: "rejected", title: "Rejected", statuses: ["REJECTED"] },
  { key: "draft", title: "Draft", statuses: ["DRAFT"] },
];

export default function ReviewPage() {
  const toast = useToast();
  const [view, setView] = useState<View>("inbox");
  const [q, setQ] = useState("");
  const [query, setQuery] = useState("");
  const [rejecting, setRejecting] = useState<Post | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  useEffect(() => {
    try {
      const v = window.localStorage.getItem(VIEW_KEY);
      if (v === "inbox" || v === "kanban") setView(v);
    } catch {
      /* ignore */
    }
  }, []);
  const changeView = (v: View) => {
    setView(v);
    try {
      window.localStorage.setItem(VIEW_KEY, v);
    } catch {
      /* ignore */
    }
  };

  const debounceQuery = useDebouncedCallback(() => setQuery(q.trim()), 300);
  useEffect(debounceQuery, [q, debounceQuery]);

  const stats = useAsync(() => api.stats(), []);
  const posts = useAsync(
    () =>
      view === "inbox"
        ? api.listPosts({ status: ["PENDING_REVIEW"], q: query, limit: 100 })
        : api.listPosts({ q: query, limit: 300 }),
    [view, query],
  );

  const refresh = useDebouncedCallback(() => {
    void posts.reload();
    void stats.reload();
  }, 250);
  useServerEvent(["post.updated", "post.deleted"], refresh);

  const approve = async (p: Post) => {
    setBusy(p.id);
    try {
      await api.approvePost(p.id);
      toast.success("Approved — handed to the publish queue");
      refresh();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };

  const reject = async (reason: string) => {
    if (!rejecting) return;
    try {
      await api.rejectPost(rejecting.id, reason);
      toast.success("Rejected and archived");
      refresh();
    } catch (e) {
      toast.error(e);
      throw e;
    }
  };

  const retry = async (p: Post) => {
    setBusy(p.id);
    try {
      await api.retryPost(p.id);
      toast.success("Re-queued");
      refresh();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };

  const counts = stats.data?.posts_by_status ?? {};
  const grouped = useMemo(() => {
    const g: Record<string, Post[]> = {};
    for (const c of COLUMNS) g[c.key] = [];
    for (const p of posts.data?.items ?? []) {
      const col = COLUMNS.find((c) => c.statuses.includes(p.status));
      if (col) g[col.key].push(p);
    }
    return g;
  }, [posts.data]);

  const quickActions = (p: Post) => {
    if (p.status === "PENDING_REVIEW")
      return (
        <>
          <Button size="sm" variant="ghost" onClick={() => setRejecting(p)} disabled={busy === p.id}>
            Reject
          </Button>
          <Button size="sm" variant="success" onClick={() => approve(p)} loading={busy === p.id}>
            <Icon name="check" className="h-3.5 w-3.5" /> Approve
          </Button>
        </>
      );
    if (p.status === "FAILED")
      return (
        <Button size="sm" onClick={() => retry(p)} loading={busy === p.id}>
          <Icon name="refresh" className="h-3.5 w-3.5" /> Retry
        </Button>
      );
    return null;
  };

  return (
    <div>
      <PageHeader
        title="Review"
        subtitle="Every generated post waits here for a human decision before it reaches any social platform."
        actions={
          <>
            <div className="inline-flex rounded-md border border-zinc-300 p-0.5 dark:border-zinc-700">
              {(["inbox", "kanban"] as View[]).map((v) => (
                <button
                  key={v}
                  onClick={() => changeView(v)}
                  className={cx(
                    "inline-flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium",
                    view === v
                      ? "bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-900"
                      : "text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100",
                  )}
                >
                  <Icon name={v === "inbox" ? "list" : "kanban"} className="h-3.5 w-3.5" />
                  {v === "inbox" ? "Inbox" : "Kanban"}
                </button>
              ))}
            </div>
            <Link href="/posts/new/">
              <Button variant="primary">
                <Icon name="plus" /> New draft
              </Button>
            </Link>
          </>
        }
      />

      {/* status strip */}
      <div className="mb-4 grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-7">
        {(["PENDING_REVIEW", "APPROVED", "QUEUED", "PUBLISHED", "FAILED", "REJECTED", "DRAFT"] as PostStatus[]).map(
          (s) => (
            <div
              key={s}
              className="rounded-lg border border-zinc-200 bg-white px-3 py-2 dark:border-zinc-800 dark:bg-zinc-900"
            >
              <div className="flex items-center gap-1.5 text-[11px] font-medium text-zinc-500">
                <span className={cx("h-1.5 w-1.5 rounded-full", STATUS_STYLE[s].dot)} />
                {STATUS_LABEL[s]}
              </div>
              <div className="mt-0.5 text-lg font-semibold tabular-nums text-zinc-900 dark:text-zinc-100">
                {stats.data ? (counts[s] ?? 0) : "–"}
              </div>
            </div>
          ),
        )}
      </div>

      <div className="mb-4 max-w-md">
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search title or content…" />
      </div>

      {posts.error && <ErrorBox onRetry={posts.reload}>{posts.error}</ErrorBox>}
      {posts.loading && !posts.data && <Loading />}

      {posts.data && view === "inbox" && (
        <div className="space-y-2.5">
          {posts.data.items.length === 0 ? (
            <div className="rounded-lg border border-dashed border-zinc-300 dark:border-zinc-700">
              <EmptyState
                title="Inbox zero"
                hint="Nothing is waiting for review. Schedules drop new drafts here, or you can run one now."
                action={
                  <Link href="/schedules/">
                    <Button size="sm">Go to schedules</Button>
                  </Link>
                }
              />
            </div>
          ) : (
            posts.data.items.map((p) => <PostCard key={p.id} post={p} actions={quickActions(p)} />)
          )}
          {posts.data.total > posts.data.items.length && (
            <p className="text-center text-xs text-zinc-500">
              Showing {posts.data.items.length} of {posts.data.total}
            </p>
          )}
        </div>
      )}

      {posts.data && view === "kanban" && (
        <div className="-mx-4 overflow-x-auto px-4 pb-2 sm:-mx-6 sm:px-6 lg:-mx-8 lg:px-8">
          <div className="grid min-w-[1100px] grid-cols-6 gap-3">
            {COLUMNS.map((c) => (
              <section key={c.key} className="flex min-w-0 flex-col rounded-lg bg-zinc-100/70 dark:bg-zinc-900/50">
                <header className="flex items-center justify-between px-3 py-2">
                  <h2 className="flex items-center gap-1.5 text-xs font-semibold text-zinc-700 dark:text-zinc-300">
                    <span className={cx("h-1.5 w-1.5 rounded-full", STATUS_STYLE[c.statuses[0]].dot)} />
                    {c.title}
                  </h2>
                  <span className="text-xs tabular-nums text-zinc-500">{grouped[c.key].length}</span>
                </header>
                <div className="flex max-h-[calc(100vh-320px)] min-h-24 flex-col gap-2 overflow-y-auto px-2 pb-2">
                  {grouped[c.key].map((p) => (
                    <PostCard key={p.id} post={p} compact actions={quickActions(p)} />
                  ))}
                  {grouped[c.key].length === 0 && <p className="px-1 py-4 text-center text-xs text-zinc-400">Empty</p>}
                </div>
              </section>
            ))}
          </div>
        </div>
      )}

      <RejectDialog open={!!rejecting} onClose={() => setRejecting(null)} onConfirm={reject} />
    </div>
  );
}
