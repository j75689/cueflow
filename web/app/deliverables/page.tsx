"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { STATUS_LABEL, STATUS_STYLE, StatusBadge } from "@/components/badges";
import { RejectDialog } from "@/components/post";
import { useMeta, useServerEvent, useToast } from "@/components/providers";
import { Button, Checkbox, EmptyState, ErrorBox, Icon, Input, Loading, PageHeader, Pill, Select, cx } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative, plural, shortId } from "@/lib/format";
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
  // Bulk selection: ids of pending deliverables.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [bulkRejecting, setBulkRejecting] = useState(false);
  const [bulkBusy, setBulkBusy] = useState<"approve" | "reject" | null>(null);

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
  // A new scope, kind or search is a new list: what was ticked in the old one does not carry over.
  useEffect(() => setSelected(new Set()), [scope, kind, query]);

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

  const pending = items.data?.items.filter((d) => d.status === "PENDING_REVIEW") ?? [];
  const chosen = pending.filter((d) => selected.has(d.id));
  const toggleSelect = (id: string, on: boolean) =>
    setSelected((cur) => {
      const next = new Set(cur);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });

  // bulk sends the chosen deliverables to the server in one request and reports what failed and
  // why. The ones that failed stay selected, so retrying them is one click.
  const bulk = async (verb: "approve" | "reject", reason?: string) => {
    if (chosen.length === 0) {
      toast.info(`Nothing left to ${verb}`);
      return;
    }
    setBulkBusy(verb);
    try {
      const res = await api.bulkDeliverables({ action: verb, ids: chosen.map((d) => d.id), reason });
      const title = Object.fromEntries(chosen.map((d) => [d.id, d.title || shortId(d.id)]));
      setSelected(new Set(res.failed.map((f) => f.id)));
      if (res.ok.length) toast.success(`${verb === "approve" ? "Approved" : "Rejected"} ${plural(res.ok.length, "deliverable")}`);
      if (res.failed.length)
        toast.error(`Could not ${verb}: ${res.failed.map((f) => `${title[f.id] ?? shortId(f.id)}: ${f.message}`).join("; ")}`);
    } catch (e) {
      toast.error(e);
    } finally {
      setBulkBusy(null);
      refresh();
    }
  };

  const counts = stats.data?.deliverables_by_status ?? {};
  const kinds = meta?.deliverable_kinds ?? [];
  const kindName = useMemo(() => Object.fromEntries(kinds.map((k) => [k.id, k.name])), [kinds]);

  const actions = (d: Deliverable) => {
    if (d.status === "PENDING_REVIEW")
      return (
        <>
          <Button size="sm" variant="ghost" onClick={() => setRejecting(d)} disabled={busy === d.id || !!bulkBusy}>
            Reject
          </Button>
          <Button
            size="sm"
            variant="success"
            loading={busy === d.id}
            disabled={!!bulkBusy}
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
          disabled={!!bulkBusy}
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
            <div className="inline-flex rounded-md border border-line-strong p-0.5">
              {(["inbox", "all"] as Scope[]).map((v) => (
                <button
                  key={v}
                  onClick={() => setScope(v)}
                  className={cx(
                    "inline-flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium",
                    scope === v
                      ? "bg-inverse text-on-inverse"
                      : "text-fg-2 hover:text-fg",
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
            className="rounded-lg border border-line bg-surface px-3 py-2"
          >
            <div className="flex items-center gap-1.5 text-[11px] font-medium text-fg-3">
              <span className={cx("h-1.5 w-1.5 rounded-full", STATUS_STYLE[s].dot)} />
              {STATUS_LABEL[s]}
            </div>
            <div className="mt-0.5 text-lg font-semibold tabular-nums text-fg">
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
          <div className="w-44">
            <Select aria-label="Kind" value={kind} onChange={(e) => setKind(e.target.value)}>
              <option value="">All kinds</option>
              {kinds.map((k) => (
                <option key={k.id} value={k.id}>
                  {k.name}
                </option>
              ))}
            </Select>
          </div>
        )}
      </div>

      {items.error && <ErrorBox onRetry={items.reload}>{items.error}</ErrorBox>}
      {items.loading && !items.data && <Loading />}

      {/* top-12 clears the shell's mobile header (h-12), which is sticky in the same scroll flow. */}
      {pending.length > 0 && (
        <div className="sticky top-12 z-20 mb-2.5 flex min-h-11 flex-wrap items-center gap-3 rounded-lg border border-line bg-surface/95 px-3 py-1.5 backdrop-blur md:top-0">
          <Checkbox
            checked={chosen.length > 0 && chosen.length === pending.length}
            onChange={(on) => setSelected(on ? new Set(pending.map((d) => d.id)) : new Set())}
            label={chosen.length ? `${chosen.length} selected` : "Select all"}
            disabled={!!bulkBusy}
          />
          {chosen.length > 0 && (
            <div className="ml-auto flex gap-1.5">
              <Button size="sm" variant="ghost" onClick={() => setBulkRejecting(true)} disabled={!!bulkBusy}>
                Reject {chosen.length}
              </Button>
              <Button
                size="sm"
                variant="success"
                onClick={() => void bulk("approve")}
                loading={bulkBusy === "approve"}
                disabled={!!bulkBusy}
              >
                <Icon name="check" className="h-3.5 w-3.5" /> Approve {chosen.length}
              </Button>
            </div>
          )}
        </div>
      )}

      {items.data && (
        <div className="space-y-2.5">
          {items.data.items.length === 0 ? (
            <div className="rounded-lg border border-dashed border-line-strong">
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
                className={cx(
                  "rounded-xl border bg-surface p-4 shadow-card transition-colors",
                  d.status === "PENDING_REVIEW" && selected.has(d.id)
                    ? "border-accent-solid ring-1 ring-accent-solid/30"
                    : "border-line hover:border-line-strong",
                )}
              >
                <div className="flex flex-wrap items-start gap-2">
                  <div className="flex min-w-0 flex-1 basis-full gap-3 sm:basis-0">
                    {d.status === "PENDING_REVIEW" && (
                      <div className="pt-0.5">
                        <Checkbox
                          checked={selected.has(d.id)}
                          onChange={(on) => toggleSelect(d.id, on)}
                          label={<span className="sr-only">Select {d.title || "deliverable"}</span>}
                          disabled={!!bulkBusy}
                        />
                      </div>
                    )}
                    <div className="min-w-0 flex-1">
                      <Link
                        href={`/deliverables/view/?id=${d.id}`}
                        className="line-clamp-1 text-sm font-semibold text-fg hover:text-accent"
                      >
                        {d.title || <span className="text-fg-4">Untitled</span>}
                      </Link>
                      <p className="mt-1 line-clamp-2 text-sm text-fg-2">{excerpt(d)}</p>
                      <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-fg-3">
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
            <p className="text-center text-xs text-fg-3">
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
      <RejectDialog
        open={bulkRejecting}
        title={`Reject ${plural(chosen.length, "deliverable")}`}
        onClose={() => setBulkRejecting(false)}
        onConfirm={(reason) => bulk("reject", reason)}
      />
    </div>
  );
}
