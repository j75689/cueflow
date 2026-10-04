"use client";

import Link from "next/link";
import { useState } from "react";
import { charCount, fmtRelative, fmtTime, hostOf } from "@/lib/format";
import type { AuditLog, Platform, Post } from "@/lib/types";
import { PlatformBadge, StatusBadge } from "./badges";
import { useMeta } from "./providers";
import { Button, Field, Icon, Modal, Pill, Textarea, cx } from "./ui";

/* ------------------------------ Reject dialog ------------------------------ */

export function RejectDialog({
  open,
  onClose,
  onConfirm,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: (reason: string) => Promise<void> | void;
}) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    if (!reason.trim()) return;
    setBusy(true);
    try {
      await onConfirm(reason.trim());
      setReason("");
      onClose();
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Reject post"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="danger" onClick={submit} loading={busy} disabled={!reason.trim()}>
            Reject
          </Button>
        </>
      }
    >
      <Field label="Reason" required help="Archived with the post and shown in the audit trail.">
        <Textarea
          autoFocus
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder="e.g. Off-brand tone, factual error in paragraph 2…"
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void submit();
          }}
        />
      </Field>
    </Modal>
  );
}

/* --------------------------------- Helpers --------------------------------- */

export function platformMap(platforms: Platform[] | undefined): Record<string, Platform> {
  return Object.fromEntries((platforms ?? []).map((p) => [p.id, p]));
}

export function postParts(p: Pick<Post, "content" | "thread_parts">): string[] {
  return p.thread_parts && p.thread_parts.length ? p.thread_parts : [p.content];
}

export function Warnings({ warnings, compact }: { warnings: string[] | null | undefined; compact?: boolean }) {
  if (!warnings?.length) return null;
  if (compact)
    return (
      <span className="inline-flex items-center gap-1 text-xs text-amber-700 dark:text-amber-400" title={warnings.join("\n")}>
        <Icon name="warn" className="h-3.5 w-3.5" />
        {warnings.length} warning{warnings.length > 1 ? "s" : ""}
      </span>
    );
  return (
    <ul className="space-y-1 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-900/60 dark:bg-amber-950/40 dark:text-amber-300">
      {warnings.map((w) => (
        <li key={w} className="flex items-start gap-1.5">
          <Icon name="warn" className="mt-px h-3.5 w-3.5 shrink-0" />
          {w}
        </li>
      ))}
    </ul>
  );
}

export function SourceLinks({ post, limit = 3 }: { post: Post; limit?: number }) {
  const items = [...(post.source_context?.items ?? []), ...(post.source_context?.search ?? [])].filter((s) => s.url);
  if (!items.length) return null;
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-zinc-500">
      {items.slice(0, limit).map((s, i) => (
        <a
          key={i}
          href={s.url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex max-w-[16rem] items-center gap-1 truncate hover:text-indigo-600 dark:hover:text-indigo-400"
          onClick={(e) => e.stopPropagation()}
        >
          <Icon name="link" className="h-3 w-3 shrink-0" />
          <span className="truncate">{s.source || hostOf(s.url)}</span>
        </a>
      ))}
      {items.length > limit && <span>+{items.length - limit} more</span>}
    </div>
  );
}

/* -------------------------------- Post card -------------------------------- */

export function PostCard({
  post,
  actions,
  compact,
}: {
  post: Post;
  actions?: React.ReactNode;
  compact?: boolean;
}) {
  const { meta } = useMeta();
  const pm = platformMap(meta?.platforms);
  const parts = postParts(post);
  return (
    <div className="group rounded-lg border border-zinc-200 bg-white transition-shadow hover:shadow-sm dark:border-zinc-800 dark:bg-zinc-900">
      <Link href={`/posts/view/?id=${post.id}`} className="block px-3.5 pt-3 pb-2">
        <div className="mb-1.5 flex flex-wrap items-center gap-1.5">
          {post.platforms.map((p) => (
            <PlatformBadge key={p} id={p} name={pm[p]?.name} />
          ))}
          <StatusBadge status={post.status} />
          {parts.length > 1 && <Pill>{parts.length}-part thread</Pill>}
          <span className="ml-auto text-[11px] text-zinc-400" title={fmtTime(post.created_at)}>
            {fmtRelative(post.created_at)}
          </span>
        </div>
        {post.title && (
          <h3 className="mb-0.5 line-clamp-1 text-sm font-semibold text-zinc-900 dark:text-zinc-100">{post.title}</h3>
        )}
        <p
          className={cx(
            "text-sm whitespace-pre-line text-zinc-600 dark:text-zinc-400",
            compact ? "line-clamp-3" : "line-clamp-4",
          )}
        >
          {parts[0]}
        </p>
        {post.status === "FAILED" && post.error_message && (
          <p className="mt-1.5 line-clamp-2 rounded bg-red-50 px-2 py-1 text-xs text-red-700 dark:bg-red-950/50 dark:text-red-300">
            {post.error_message}
          </p>
        )}
        {post.status === "REJECTED" && post.review_note && (
          <p className="mt-1.5 line-clamp-2 text-xs text-rose-700 italic dark:text-rose-300">“{post.review_note}”</p>
        )}
      </Link>
      <div className="flex flex-wrap items-center gap-2 px-3.5 pb-3">
        <Warnings warnings={post.warnings} compact />
        {!compact && <SourceLinks post={post} />}
        {actions && <div className="ml-auto flex items-center gap-1.5">{actions}</div>}
      </div>
    </div>
  );
}

/* ----------------------------- Platform preview ---------------------------- */

function Avatar() {
  return (
    <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-gradient-to-br from-indigo-500 to-pink-500 text-xs font-bold text-white">
      CF
    </span>
  );
}

export function ThreadsPreview({ parts, platform }: { parts: string[]; platform?: Platform }) {
  const max = platform?.max_chars ?? 500;
  return (
    <div className="space-y-0">
      {parts.map((p, i) => {
        const n = charCount(p);
        return (
          <div key={i} className="flex gap-3">
            <div className="flex flex-col items-center">
              <Avatar />
              {i < parts.length - 1 && <span className="my-1 w-0.5 flex-1 bg-zinc-200 dark:bg-zinc-700" />}
            </div>
            <div className="min-w-0 flex-1 pb-4">
              <div className="flex items-center gap-2 text-xs">
                <span className="font-semibold text-zinc-900 dark:text-zinc-100">cueflow</span>
                <span className="text-zinc-400">
                  {i + 1}/{parts.length}
                </span>
                <span className={cx("ml-auto tabular-nums", n > max ? "text-red-600" : "text-zinc-400")}>
                  {n}/{max}
                </span>
              </div>
              <p className="mt-0.5 text-sm break-words whitespace-pre-wrap text-zinc-800 dark:text-zinc-200">{p || " "}</p>
            </div>
          </div>
        );
      })}
    </div>
  );
}

export function InstagramPreview({
  caption,
  hashtags,
  media,
  platform,
}: {
  caption: string;
  hashtags: string[];
  media: string[];
  platform?: Platform;
}) {
  const full = hashtags.length ? `${caption}\n\n${hashtags.map((h) => `#${h}`).join(" ")}` : caption;
  const n = charCount(full);
  const max = platform?.max_chars ?? 2200;
  return (
    <div className="overflow-hidden rounded-lg border border-zinc-200 dark:border-zinc-800">
      <div className="flex items-center gap-2 px-3 py-2">
        <Avatar />
        <span className="text-xs font-semibold">cueflow</span>
      </div>
      <div className="flex aspect-square items-center justify-center bg-zinc-100 dark:bg-zinc-800">
        {media[0] ? (
          // eslint-disable-next-line @next/next/no-img-element
          <img src={media[0]} alt="" className="h-full w-full object-cover" />
        ) : (
          <span className="px-6 text-center text-xs text-zinc-400">
            No media yet{platform?.requires_media ? " — Instagram requires an image" : ""}
          </span>
        )}
      </div>
      <div className="px-3 py-2.5">
        <p className="text-sm break-words whitespace-pre-wrap text-zinc-800 dark:text-zinc-200">
          <span className="mr-1 font-semibold">cueflow</span>
          {caption}
        </p>
        {hashtags.length > 0 && (
          <p className="mt-2 text-sm break-words text-indigo-700 dark:text-indigo-400">
            {hashtags.map((h) => `#${h}`).join(" ")}
          </p>
        )}
        <p className={cx("mt-2 text-right text-[11px] tabular-nums", n > max ? "text-red-600" : "text-zinc-400")}>
          {n}/{max}
        </p>
      </div>
    </div>
  );
}

export function GenericPreview({ parts, platform }: { parts: string[]; platform?: Platform }) {
  return <ThreadsPreview parts={parts} platform={platform} />;
}

/* ---------------------------------- Audit ---------------------------------- */

const ACTION_COLOR: Record<string, string> = {
  GENERATED: "bg-violet-500",
  CREATED: "bg-zinc-400",
  EDITED: "bg-sky-500",
  SUBMITTED: "bg-amber-500",
  APPROVED: "bg-emerald-500",
  REJECTED: "bg-rose-500",
  QUEUED: "bg-indigo-500",
  DISPATCHED: "bg-indigo-500",
  PUBLISHED: "bg-emerald-600",
  FAILED: "bg-red-600",
  RETRIED: "bg-amber-600",
};

function show(v: unknown): string {
  if (v === null || v === undefined) return "∅";
  if (typeof v === "string") return v;
  return JSON.stringify(v, null, 2);
}

export function AuditTimeline({ logs }: { logs: AuditLog[] }) {
  if (!logs.length) return <p className="px-4 py-6 text-sm text-zinc-500">No history yet.</p>;
  return (
    <ol className="relative space-y-4 px-4 py-4">
      {logs.map((l, i) => (
        <li key={l.id} className="relative flex gap-3">
          <div className="flex flex-col items-center">
            <span className={cx("mt-1 h-2.5 w-2.5 rounded-full", ACTION_COLOR[l.action] ?? "bg-zinc-400")} />
            {i < logs.length - 1 && <span className="mt-1 w-px flex-1 bg-zinc-200 dark:bg-zinc-800" />}
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs">
              <span className="font-semibold text-zinc-800 dark:text-zinc-200">{l.action}</span>
              <span className="text-zinc-500">by {l.actor}</span>
              {l.from_status && l.to_status && l.from_status !== l.to_status && (
                <span className="font-mono text-[11px] text-zinc-400">
                  {l.from_status} → {l.to_status}
                </span>
              )}
              <span className="ml-auto text-zinc-400" title={fmtTime(l.created_at)}>
                {fmtRelative(l.created_at)}
              </span>
            </div>
            {l.note && <p className="mt-1 text-xs whitespace-pre-wrap text-zinc-600 dark:text-zinc-400">{l.note}</p>}
            {l.payload_diff && Object.keys(l.payload_diff).length > 0 && (
              <details className="mt-1.5">
                <summary className="cursor-pointer text-xs text-zinc-500 hover:text-zinc-700 dark:hover:text-zinc-300">
                  Changed: {Object.keys(l.payload_diff).join(", ")}
                </summary>
                <div className="mt-1.5 space-y-2">
                  {Object.entries(l.payload_diff).map(([field, d]) => (
                    <div key={field} className="text-xs">
                      <div className="mb-0.5 font-mono text-[11px] text-zinc-500">{field}</div>
                      <pre className="overflow-x-auto rounded bg-red-50 px-2 py-1 whitespace-pre-wrap text-red-800 line-through decoration-red-300 dark:bg-red-950/40 dark:text-red-300">
                        {show(d?.before)}
                      </pre>
                      <pre className="mt-0.5 overflow-x-auto rounded bg-emerald-50 px-2 py-1 whitespace-pre-wrap text-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-300">
                        {show(d?.after)}
                      </pre>
                    </div>
                  ))}
                </div>
              </details>
            )}
          </div>
        </li>
      ))}
    </ol>
  );
}
