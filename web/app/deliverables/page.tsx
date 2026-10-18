"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { STATUS_LABEL, STATUS_STYLE, StatusBadge } from "@/components/badges";
import { RejectDialog } from "@/components/post";
import { useMeta, useServerEvent, useToast } from "@/components/providers";
import { Button, EmptyState, ErrorBox, Icon, Input, Loading, PageHeader, Pill, cx } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative, shortId } from "@/lib/format";
import { useAsync, useDebouncedCallback } from "@/lib/hooks";
import type { Deliverable, DeliverableStatus } from "@/lib/types";

const STRIP: DeliverableStatus[] = ["PENDING_REVIEW", "APPROVED", "QUEUED", "DONE", "FAILED", "REJECTED", "DRAFT"];

type Scope = "inbox" | "all";

// excerpt is the first lines of whatever the payload holds, for the card.
function excerpt(d: Deliverable): string {
  const p = d.payload ?? {};
  const text =
    typeof p.summary === "string" && p.summary.trim()
      ? p.summary
      : typeof p.content === "string"
        ? p.content
        : JSON.stringify(p);
  return text.replace(/\s+/g, " ").trim().slice(0, 240);
}

export default function DeliverablesPage() {
  const toast = useToast();
  const { meta } = useMeta();
  const [scope, setScope] = useState<Scope>("inbox");
  const [kind, setKind] = useState("");
  const [q, setQ] = useState("");
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [rejecting, setRejecting] = useState<Deliverable | null>(null);

  const debounceQuery = useDebouncedCallback(() => setQuery(q.trim()), 300);
  useEffect(debounceQuery, [q, debounceQuery]);

  const stats = useAsync(() => api.stats(), []);
  const items = useAsync(
    () =>
      api.listDeliverables({
        status: scope === "inbox" ? ["PENDING_REVIEW"] : undefined,
        kind: kind || undefined,
        q: query,
        limit: 200,
      }),
    [scope, kind, query],
  );
  const refresh = useDebouncedCallback(() => {
    void items.reload();
    void stats.reload();
  }, 250);
  useServerEvent(["deliverable.updated", "deliverable.deleted"], refresh);

  const run = async (id: string, fn: () => Promise<unknown>, msg: string) => {
    setBusy(id);
    try {
      await fn();
      toast.success(msg);
      refresh();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };

  const counts = stats.data?.deliverables_by_status ?? {};
  const kinds = meta?.deliverable_kinds ?? [];
  const kindName = useMemo(() => Object.fromEntries(kinds.map((k) => [k.id, k.name])), [kinds]);

  const actions = (d: Deliverable) => {
    if (d.status === "PENDING_REVIEW")
      return (
        <>
          <Button size="sm" variant="ghost" onClick={() => setRejecting(d)} disabled={busy === d.id}>
            Reject
          </Button>
          <Button
            size="sm"
            variant="success"
            loading={busy === d.id}
            onClick={() => run(d.id, () => api.approveDeliverable(d.id), "Approved")}
          >
            <Icon name="check" className="h-3.5 w-3.5" /> Approve
          </Button>
        </>
      );
    if (d.status === "FAILED")
      return (
        <Button
          size="sm"
          loading={busy === d.id}
          onClick={() => run(d.id, () => api.retryDeliverable(d.id), "Re-queued")}
        >
          <Icon name="refresh" className="h-3.5 w-3.5" /> Retry
        </Button>
      );
    return null;
  };

  return (
    <div>
      <PageHeader
        title="Deliverables"
        subtitle="Documents and other items agents produce wait here for a human decision; approved items are handed to the action connector."
        actions={
          <>
            <div className="inline-flex rounded-md border border-zinc-300 p-0.5 dark:border-zinc-700">
              {(["inbox", "all"] as Scope[]).map((v) => (
                <button
                  key={v}
                  onClick={() => setScope(v)}
                  className={cx(
                    "inline-flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium",
                    scope === v
                      ? "bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-900"
                      : "text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100",
                  )}
                >
                  {v === "inbox" ? "Pending" : "All"}
                </button>
              ))}
            </div>
            <Link href="/deliverables/view/">
              <Button variant="primary">
                <Icon name="plus" /> New document
              </Button>
            </Link>
          </>
        }
      />

      <div className="mb-4 grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-7">
        {STRIP.map((s) => (
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
        ))}
      </div>

      <div className="mb-4 flex flex-wrap gap-2">
        <div className="max-w-md flex-1">
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search title or content…" />
        </div>
        {kinds.length > 1 && (
          <select
            value={kind}
            onChange={(e) => setKind(e.target.value)}
            className="rounded-md border border-zinc-300 bg-white px-2 text-sm dark:border-zinc-700 dark:bg-zinc-900"
          >
            <option value="">All kinds</option>
            {kinds.map((k) => (
              <option key={k.id} value={k.id}>
                {k.name}
              </option>
            ))}
          </select>
        )}
      </div>

      {items.error && <ErrorBox onRetry={items.reload}>{items.error}</ErrorBox>}
      {items.loading && !items.data && <Loading />}

      {items.data && (
        <div className="space-y-2.5">
          {items.data.items.length === 0 ? (
            <div className="rounded-lg border border-dashed border-zinc-300 dark:border-zinc-700">
              <EmptyState
                title={scope === "inbox" ? "Nothing to review" : "No deliverables yet"}
                hint="Agents with the Deliverables capability file documents here; you can also write one yourself."
                action={
                  <Link href="/deliverables/view/">
                    <Button size="sm">New document</Button>
                  </Link>
                }
              />
            </div>
          ) : (
            items.data.items.map((d) => (
              <article
                key={d.id}
                className="rounded-lg border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900"
              >
                <div className="flex flex-wrap items-start gap-2">
                  <div className="min-w-0 flex-1">
                    <Link
                      href={`/deliverables/view/?id=${d.id}`}
                      className="line-clamp-1 text-sm font-semibold text-zinc-900 hover:text-indigo-600 dark:text-zinc-100 dark:hover:text-indigo-400"
                    >
                      {d.title || <span className="text-zinc-400">Untitled</span>}
                    </Link>
                    <p className="mt-1 line-clamp-2 text-sm text-zinc-600 dark:text-zinc-400">{excerpt(d)}</p>
                    <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-zinc-500">
                      <Pill>{kindName[d.kind] ?? d.kind}</Pill>
                      <span className="font-mono">#{shortId(d.id)}</span>
                      <span>{d.created_by === "system_agent" ? "by an agent" : "by a person"}</span>
                      <span title={d.updated_at}>{fmtRelative(d.updated_at)}</span>
                      {d.task_id && (
                        <Link className="hover:underline" href={`/tasks/view/?id=${d.task_id}`}>
                          task
                        </Link>
                      )}
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1.5">
                    <StatusBadge status={d.status} />
                    {actions(d)}
                  </div>
                </div>
                {d.status === "FAILED" && d.error_message && (
                  <pre className="mt-2 rounded bg-red-50 px-2 py-1 font-mono text-xs whitespace-pre-wrap text-red-700 dark:bg-red-950/40 dark:text-red-300">
                    {d.error_message}
                  </pre>
                )}
              </article>
            ))
          )}
          {items.data.total > items.data.items.length && (
            <p className="text-center text-xs text-zinc-500">
              Showing {items.data.items.length} of {items.data.total}
            </p>
          )}
        </div>
      )}

      <RejectDialog
        open={!!rejecting}
        title="Reject deliverable"
        onClose={() => setRejecting(null)}
        onConfirm={async (reason) => {
          if (!rejecting) return;
          await run(rejecting.id, () => api.rejectDeliverable(rejecting.id, reason), "Rejected");
        }}
      />
    </div>
  );
}
