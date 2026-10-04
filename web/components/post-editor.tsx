"use client";

import { useMemo, useState } from "react";
import { charCount, fromLocalInput, toLocalInput } from "@/lib/format";
import type { Connector, Platform } from "@/lib/types";
import { StringListInput, TagInput } from "./inputs";
import { GenericPreview, InstagramPreview, ThreadsPreview } from "./post";
import { Button, Card, CardHeader, Checkbox, Field, Icon, Input, Select, Textarea, cx } from "./ui";

export interface PostDraft {
  title: string;
  content: string;
  thread_parts: string[];
  hashtags: string[];
  media_urls: string[];
  image_prompt: string;
  platforms: string[];
  scheduled_at: string | null;
  publisher_connector_id: string | null;
}

export const emptyDraft: PostDraft = {
  title: "",
  content: "",
  thread_parts: [],
  hashtags: [],
  media_urls: [],
  image_prompt: "",
  platforms: [],
  scheduled_at: null,
  publisher_connector_id: null,
};

// splitThread breaks text into parts under `limit` code points, preferring
// paragraph, then sentence, then word boundaries.
export function splitThread(text: string, limit: number): string[] {
  const parts: string[] = [];
  const push = (s: string) => {
    const t = s.trim();
    if (t) parts.push(t);
  };
  const units = text
    .split(/\n{2,}/)
    .flatMap((para) => (charCount(para) <= limit ? [para] : para.split(/(?<=[.!?\u3002\uff01\uff1f])\s*/)));
  let cur = "";
  for (const u of units) {
    const candidate = cur ? `${cur}\n\n${u}` : u;
    if (charCount(candidate) <= limit) {
      cur = candidate;
      continue;
    }
    push(cur);
    if (charCount(u) <= limit) {
      cur = u;
      continue;
    }
    // hard split by words / characters
    let buf = "";
    for (const w of u.split(/(\s+)/)) {
      if (charCount(buf + w) > limit) {
        push(buf);
        buf = "";
        let rest = w;
        while (charCount(rest) > limit) {
          const cps = Array.from(rest);
          push(cps.slice(0, limit).join(""));
          rest = cps.slice(limit).join("");
        }
        buf = rest;
      } else buf += w;
    }
    cur = buf;
  }
  push(cur);
  return parts;
}

export function PostEditor({
  draft,
  onChange,
  platforms,
  publishers,
  readOnly,
}: {
  draft: PostDraft;
  onChange: (d: PostDraft) => void;
  platforms: Platform[];
  publishers: Connector[];
  readOnly?: boolean;
}) {
  const set = <K extends keyof PostDraft>(k: K, v: PostDraft[K]) => onChange({ ...draft, [k]: v });
  const selected = platforms.filter((p) => draft.platforms.includes(p.id));
  const threadPlatforms = selected.filter((p) => p.supports_thread);
  // Strictest limit among selected platforms that will receive the thread.
  const partLimit = threadPlatforms.length ? Math.min(...threadPlatforms.map((p) => p.max_chars)) : 500;
  const contentLimit = selected.length ? Math.min(...selected.map((p) => p.max_chars)) : 0;
  const contentCount = charCount(draft.content);
  const usingThread = draft.thread_parts.length > 0;

  const movePart = (i: number, d: -1 | 1) => {
    const j = i + d;
    if (j < 0 || j >= draft.thread_parts.length) return;
    const next = [...draft.thread_parts];
    [next[i], next[j]] = [next[j], next[i]];
    set("thread_parts", next);
  };

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader title="Content" />
        <div className="space-y-4 p-4">
          <Field label="Title" help="Internal label; not published.">
            <Input value={draft.title} disabled={readOnly} onChange={(e) => set("title", e.target.value)} />
          </Field>

          <Field label="Platforms" required>
            <div className="flex flex-wrap gap-x-4 gap-y-2 pt-1">
              {platforms.map((p) => (
                <Checkbox
                  key={p.id}
                  label={p.name}
                  disabled={readOnly}
                  checked={draft.platforms.includes(p.id)}
                  onChange={(on) =>
                    set("platforms", on ? [...draft.platforms, p.id] : draft.platforms.filter((x) => x !== p.id))
                  }
                />
              ))}
            </div>
          </Field>

          <Field
            label={usingThread ? "Main content / caption" : "Content"}
            required
            right={
              contentLimit > 0 && (
                <span
                  className={cx(
                    "tabular-nums",
                    contentCount > contentLimit && !usingThread ? "text-red-600" : "text-zinc-400",
                  )}
                >
                  {contentCount}/{contentLimit}
                </span>
              )
            }
            help={
              usingThread
                ? "Thread platforms publish the parts below; single-post platforms (e.g. Instagram) use this content."
                : undefined
            }
          >
            <Textarea
              rows={8}
              value={draft.content}
              disabled={readOnly}
              onChange={(e) => set("content", e.target.value)}
            />
          </Field>

          <div>
            <div className="mb-1.5 flex flex-wrap items-center justify-between gap-2">
              <span className="text-xs font-medium text-zinc-700 dark:text-zinc-300">
                Thread parts{" "}
                <span className="font-normal text-zinc-500">
                  ({draft.thread_parts.length || "none"} · limit {partLimit} per part)
                </span>
              </span>
              {!readOnly && (
                <div className="flex gap-1.5">
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={!draft.content.trim()}
                    onClick={() => set("thread_parts", splitThread(draft.content, partLimit))}
                    title="Split the content into parts that fit the limit"
                  >
                    Split from content
                  </Button>
                  {usingThread && (
                    <Button size="sm" variant="ghost" onClick={() => set("thread_parts", [])}>
                      Clear
                    </Button>
                  )}
                  <Button size="sm" variant="ghost" onClick={() => set("thread_parts", [...draft.thread_parts, ""])}>
                    <Icon name="plus" className="h-3.5 w-3.5" /> Part
                  </Button>
                </div>
              )}
            </div>
            <div className="space-y-2">
              {draft.thread_parts.map((part, i) => {
                const n = charCount(part);
                return (
                  <div key={i} className="flex gap-2">
                    <div className="flex w-7 shrink-0 flex-col items-center gap-0.5 pt-1.5">
                      <span className="text-xs font-semibold text-zinc-500 tabular-nums">{i + 1}</span>
                      {!readOnly && (
                        <>
                          <button
                            className="rounded p-0.5 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700 disabled:opacity-30 dark:hover:bg-zinc-800"
                            disabled={i === 0}
                            onClick={() => movePart(i, -1)}
                            aria-label="Move up"
                          >
                            <Icon name="up" className="h-3.5 w-3.5" />
                          </button>
                          <button
                            className="rounded p-0.5 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700 disabled:opacity-30 dark:hover:bg-zinc-800"
                            disabled={i === draft.thread_parts.length - 1}
                            onClick={() => movePart(i, 1)}
                            aria-label="Move down"
                          >
                            <Icon name="down" className="h-3.5 w-3.5" />
                          </button>
                        </>
                      )}
                    </div>
                    <div className="min-w-0 flex-1">
                      <Textarea
                        rows={3}
                        value={part}
                        disabled={readOnly}
                        className={n > partLimit ? "border-red-400 dark:border-red-700" : undefined}
                        onChange={(e) =>
                          set(
                            "thread_parts",
                            draft.thread_parts.map((x, j) => (j === i ? e.target.value : x)),
                          )
                        }
                      />
                      <div className="mt-0.5 flex justify-between text-[11px]">
                        {!readOnly ? (
                          <button
                            className="text-zinc-400 hover:text-red-600"
                            onClick={() =>
                              set(
                                "thread_parts",
                                draft.thread_parts.filter((_, j) => j !== i),
                              )
                            }
                          >
                            Remove
                          </button>
                        ) : (
                          <span />
                        )}
                        <span className={cx("tabular-nums", n > partLimit ? "text-red-600" : "text-zinc-400")}>
                          {n}/{partLimit}
                        </span>
                      </div>
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        </div>
      </Card>

      <Card>
        <CardHeader title="Media & tags" />
        <div className="grid gap-4 p-4 md:grid-cols-2">
          <Field label="Hashtags" help="Without the #. Press Enter or comma to add.">
            <TagInput value={draft.hashtags} prefix="#" disabled={readOnly} onChange={(v) => set("hashtags", v)} />
          </Field>
          <Field label="Image prompt" help="Suggested by the agent for image generation.">
            <Textarea
              rows={3}
              value={draft.image_prompt}
              disabled={readOnly}
              onChange={(e) => set("image_prompt", e.target.value)}
            />
          </Field>
          <div className="md:col-span-2">
            <Field label="Media URLs">
              <StringListInput
                value={draft.media_urls}
                disabled={readOnly}
                placeholder="https://…/image.jpg"
                onChange={(v) => set("media_urls", v)}
              />
            </Field>
            {draft.media_urls.some((u) => u.trim()) && (
              <div className="mt-2 flex flex-wrap gap-2">
                {draft.media_urls
                  .filter((u) => u.trim())
                  .map((u, i) => (
                    <a key={i} href={u} target="_blank" rel="noreferrer" className="block">
                      {/* eslint-disable-next-line @next/next/no-img-element */}
                      <img
                        src={u}
                        alt=""
                        className="h-20 w-20 rounded-md border border-zinc-200 bg-zinc-100 object-cover dark:border-zinc-800 dark:bg-zinc-800"
                        onError={(e) => ((e.target as HTMLImageElement).style.opacity = "0.3")}
                      />
                    </a>
                  ))}
              </div>
            )}
          </div>
        </div>
      </Card>

      <Card>
        <CardHeader title="Delivery" />
        <div className="grid gap-4 p-4 md:grid-cols-2">
          <Field label="Scheduled at" help="Leave empty to publish as soon as it is approved.">
            <Input
              type="datetime-local"
              disabled={readOnly}
              value={toLocalInput(draft.scheduled_at)}
              onChange={(e) => set("scheduled_at", fromLocalInput(e.target.value))}
            />
          </Field>
          <Field label="Publisher" help="Defaults to the pipeline's publisher or the first active one.">
            <Select
              disabled={readOnly}
              value={draft.publisher_connector_id ?? ""}
              onChange={(e) => set("publisher_connector_id", e.target.value || null)}
            >
              <option value="">Default</option>
              {publishers.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                  {!c.is_active ? " (inactive)" : ""}
                </option>
              ))}
            </Select>
          </Field>
        </div>
      </Card>
    </div>
  );
}

export function PostPreviews({ draft, platforms }: { draft: PostDraft; platforms: Platform[] }) {
  const selected = useMemo(
    () => platforms.filter((p) => draft.platforms.includes(p.id)),
    [platforms, draft.platforms],
  );
  const [tab, setTab] = useState<string | null>(null);
  const active = selected.find((p) => p.id === tab) ?? selected[0];
  if (!active) return <p className="px-4 py-6 text-sm text-zinc-500">Select a platform to preview.</p>;

  const parts = active.supports_thread && draft.thread_parts.length ? draft.thread_parts : [draft.content];
  return (
    <div>
      {selected.length > 1 && (
        <div className="flex gap-1 border-b border-zinc-200 px-3 pt-2 dark:border-zinc-800">
          {selected.map((p) => (
            <button
              key={p.id}
              onClick={() => setTab(p.id)}
              className={cx(
                "-mb-px border-b-2 px-2.5 py-1.5 text-xs font-medium",
                p.id === active.id
                  ? "border-indigo-500 text-indigo-700 dark:text-indigo-300"
                  : "border-transparent text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200",
              )}
            >
              {p.name}
            </button>
          ))}
        </div>
      )}
      <div className="p-4">
        {active.id === "instagram" ? (
          <InstagramPreview
            caption={draft.content}
            hashtags={draft.hashtags}
            media={draft.media_urls.filter((u) => u.trim())}
            platform={active}
          />
        ) : active.id === "threads" ? (
          <ThreadsPreview parts={parts} platform={active} />
        ) : (
          <GenericPreview parts={parts} platform={active} />
        )}
      </div>
    </div>
  );
}
