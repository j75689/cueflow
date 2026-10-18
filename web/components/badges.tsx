"use client";

import type { DeliverableStatus, PostStatus } from "@/lib/types";

export type AnyStatus = PostStatus | DeliverableStatus;
import { Pill, cx } from "./ui";

export const STATUS_LABEL: Record<AnyStatus, string> = {
  DRAFT: "Draft",
  PENDING_REVIEW: "Pending review",
  APPROVED: "Approved",
  REJECTED: "Rejected",
  QUEUED: "Queued",
  PUBLISHED: "Published",
  DONE: "Done",
  FAILED: "Failed",
};

export const STATUS_STYLE: Record<AnyStatus, { pill: string; dot: string }> = {
  DRAFT: { pill: "bg-zinc-100 text-zinc-700 dark:bg-zinc-800 dark:text-zinc-300", dot: "bg-zinc-400" },
  PENDING_REVIEW: {
    pill: "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300",
    dot: "bg-amber-500",
  },
  APPROVED: { pill: "bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300", dot: "bg-sky-500" },
  QUEUED: { pill: "bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300", dot: "bg-indigo-500" },
  PUBLISHED: {
    pill: "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300",
    dot: "bg-emerald-500",
  },
  DONE: {
    pill: "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300",
    dot: "bg-emerald-500",
  },
  FAILED: { pill: "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300", dot: "bg-red-500" },
  REJECTED: { pill: "bg-rose-100 text-rose-800 dark:bg-rose-950 dark:text-rose-300", dot: "bg-rose-400" },
};

export function StatusBadge({ status, className }: { status: AnyStatus; className?: string }) {
  const s = STATUS_STYLE[status] ?? STATUS_STYLE.DRAFT;
  return (
    <Pill className={cx(s.pill, className)}>
      <span className={cx("h-1.5 w-1.5 rounded-full", s.dot, status === "QUEUED" && "animate-pulse")} />
      {STATUS_LABEL[status] ?? status}
    </Pill>
  );
}

const PLATFORM_STYLE: Record<string, string> = {
  threads: "bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-900",
  instagram: "bg-pink-100 text-pink-800 dark:bg-pink-950 dark:text-pink-300",
  youtube: "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300",
  x: "bg-zinc-200 text-zinc-900 dark:bg-zinc-700 dark:text-zinc-100",
  linkedin: "bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300",
};

export function PlatformBadge({ id, name }: { id: string; name?: string }) {
  return <Pill className={PLATFORM_STYLE[id]}>{name ?? id}</Pill>;
}

export function RunStatusBadge({ status }: { status: string }) {
  const cls =
    status === "SUCCEEDED"
      ? "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300"
      : status === "FAILED"
        ? "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300"
        : status === "RUNNING"
          ? "bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300"
          : undefined;
  return (
    <Pill className={cls}>
      {status === "RUNNING" && <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-indigo-500" />}
      {status.toLowerCase()}
    </Pill>
  );
}

export function HealthDot({ status }: { status: "ok" | "error" | null }) {
  return (
    <span
      title={status ?? "not checked"}
      className={cx(
        "inline-block h-2 w-2 shrink-0 rounded-full",
        status === "ok" ? "bg-emerald-500" : status === "error" ? "bg-red-500" : "bg-zinc-300 dark:bg-zinc-600",
      )}
    />
  );
}

export const KIND_LABEL: Record<string, string> = {
  publisher: "Publishers",
  action: "Actions",
  source: "Sources",
  search: "Search",
  llm: "LLM providers",
  notifier: "Notifiers",
  runtime: "Agent runtimes",
  image: "Image generation",
  tools: "MCP tool servers",
};

export const KIND_ORDER = ["llm", "runtime", "image", "tools", "source", "search", "publisher", "action", "notifier"];
