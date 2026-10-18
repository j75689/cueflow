"use client";

import Link from "next/link";
import { useState } from "react";
import { charCount, fmtRelative, fmtTime, hostOf } from "@/lib/format";
import type { AuditLog, Platform, Post } from "@/lib/types";
import { PlatformBadge, StatusBadge } from "./badges";
import { useMeta } from "./providers";
import { Button, Checkbox, Field, Icon, Modal, Pill, Textarea, cx } from "./ui";

/* ------------------------------ Reject dialog ------------------------------ */

export function RejectDialog({
  open,
  onClose,
  onConfirm,
  title = "Reject post",
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: (reason: string) => Promise<void> | void;
  title?: string;
}) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async () => {
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
      title={title}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="danger" onClick={submit} loading={busy}>
            Reject
          </Button>
        </>
      }
    >
      <Field label="Reason" help="Optional. Archived with the post and shown in the audit trail.">
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

/** Whether a media URL points at a video (stored videos are served as /media/{id}.mp4). */
export function isVideoURL(u: string): boolean {
  return /\.(mp4|mov|webm)(\?|#|$)/i.test(u);
}

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
      <span
        className="inline-flex items-center gap-1 text-xs text-amber-700 dark:text-amber-400"
        title={warnings.join("\n")}
      >
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
              {i < parts.length - 1 && <span className="my-1 w-0.5 flex-1 bg-muted" />}
            </div>
            <div className="min-w-0 flex-1 pb-4">
              <div className="flex items-center gap-2 text-xs">
                <span className="font-semibold text-fg">cueflow</span>
                <span className="text-fg-4">
                  {i + 1}/{parts.length}
                </span>
                <span className={cx("ml-auto tabular-nums", n > max ? "text-red-600" : "text-fg-4")}>
                  {n}/{max}
                </span>
              </div>
              <p className="mt-0.5 text-sm break-words whitespace-pre-wrap text-fg">
                {p || " "}
              </p>
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
    <div className="overflow-hidden rounded-lg border border-line">
      <div className="flex items-center gap-2 px-3 py-2">
        <Avatar />
        <span className="text-xs font-semibold">cueflow</span>
      </div>
      <div
        className={cx(
          "flex items-center justify-center bg-subtle",
          media[0] && isVideoURL(media[0]) ? "aspect-[9/16]" : "aspect-square",
        )}
      >
        {media[0] && isVideoURL(media[0]) ? (
          <video src={media[0]} controls playsInline className="h-full w-full bg-black object-contain" />
        ) : media[0] ? (
          // eslint-disable-next-line @next/next/no-img-element
          <img src={media[0]} alt="" className="h-full w-full object-cover" />
        ) : (
          <span className="px-6 text-center text-xs text-fg-4">
            No media yet{platform?.requires_media ? " — Instagram requires an image" : ""}
          </span>
        )}
      </div>
      <div className="px-3 py-2.5">
        <p className="text-sm break-words whitespace-pre-wrap text-fg">
          <span className="mr-1 font-semibold">cueflow</span>
          {caption}
        </p>
        {hashtags.length > 0 && (
          <p className="mt-2 text-sm break-words text-accent">
            {hashtags.map((h) => `#${h}`).join(" ")}
          </p>
        )}
        <p className={cx("mt-2 text-right text-[11px] tabular-nums", n > max ? "text-red-600" : "text-fg-4")}>
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

export type AuditEntry = Pick<
  AuditLog,
  "id" | "action" | "actor" | "from_status" | "to_status" | "note" | "payload_diff" | "created_at"
>;

export function AuditTimeline({ logs }: { logs: AuditEntry[] }) {
  if (!logs.length) return <p className="px-4 py-6 text-sm text-fg-3">No history yet.</p>;
  return (
    <ol className="relative space-y-4 px-4 py-4">
      {logs.map((l, i) => (
        <li key={l.id} className="relative flex gap-3">
          <div className="flex flex-col items-center">
            <span className={cx("mt-1 h-2.5 w-2.5 rounded-full", ACTION_COLOR[l.action] ?? "bg-zinc-400")} />
            {i < logs.length - 1 && <span className="mt-1 w-px flex-1 bg-muted" />}
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs">
              <span className="font-semibold text-fg">{l.action}</span>
              <span className="text-fg-3">by {l.actor}</span>
              {l.from_status && l.to_status && l.from_status !== l.to_status && (
                <span className="font-mono text-[11px] text-fg-4">
                  {l.from_status} → {l.to_status}
                </span>
              )}
              <span className="ml-auto text-fg-4" title={fmtTime(l.created_at)}>
                {fmtRelative(l.created_at)}
              </span>
            </div>
            {l.note && <p className="mt-1 text-xs whitespace-pre-wrap text-fg-2">{l.note}</p>}
            {l.payload_diff && Object.keys(l.payload_diff).length > 0 && (
              <details className="mt-1.5">
                <summary className="cursor-pointer text-xs text-fg-3 hover:text-fg-2">
                  Changed: {Object.keys(l.payload_diff).join(", ")}
                </summary>
                <div className="mt-1.5 space-y-2">
                  {Object.entries(l.payload_diff).map(([field, d]) => (
                    <div key={field} className="text-xs">
                      <div className="mb-0.5 font-mono text-[11px] text-fg-3">{field}</div>
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

function Avatar() {
  return (
    <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-gradient-to-br from-indigo-500 to-pink-500 text-xs font-bold text-white">
      CF
    </span>
  );
}
