"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { PlatformBadge, StatusBadge } from "@/components/badges";
import {
  type PostDraft,
  PostEditor,
  PostPreviews,
  effectivePublisher,
  accountMode,
} from "@/components/post-editor";
import { AuditTimeline, RejectDialog, Warnings } from "@/components/post";
import { useDialog } from "@/components/dialog";
import { useMeta, useServerEvent, useToast } from "@/components/providers";
import {
  Button,
  Card,
  CardHeader,
  ErrorBox,
  Icon,
  Loading,
  PageHeader,
} from "@/components/ui";
import { ApiError, type PostPatch, api } from "@/lib/api";
import { fmtTime, hostOf, shortId } from "@/lib/format";
import { useAsync } from "@/lib/hooks";
import type { Post } from "@/lib/types";

function toDraft(p: Post): PostDraft {
  return {
    title: p.title ?? "",
    content: p.content ?? "",
    thread_parts: p.thread_parts ?? [],
    hashtags: p.hashtags ?? [],
    media_urls: p.media_urls ?? [],
    image_prompt: p.image_prompt ?? "",
    platforms: p.platforms ?? [],
    accounts: p.accounts ?? [],
    topic: p.topic ?? "",
    link_url: p.link_url ?? "",
    scheduled_at: p.scheduled_at,
    publisher_connector_id: p.publisher_connector_id,
  };
}

function diffDraft(before: PostDraft, after: PostDraft): Partial<PostDraft> {
  const out: Partial<PostDraft> = {};
  for (const k of Object.keys(after) as (keyof PostDraft)[]) {
    if (JSON.stringify(before[k]) !== JSON.stringify(after[k])) {
      (out as Record<string, unknown>)[k] = after[k];
    }
  }
  return out;
}

function clean(d: Partial<PostDraft>): Partial<PostDraft> {
  const out = { ...d };
  if (out.media_urls)
    out.media_urls = out.media_urls.map((u) => u.trim()).filter(Boolean);
  if (out.thread_parts)
    out.thread_parts = out.thread_parts.filter((p) => p.trim());
  return out;
}

const EDITABLE = new Set(["DRAFT", "PENDING_REVIEW"]);

function PostView() {
  const id = useSearchParams().get("id") ?? "";
  const router = useRouter();
  const toast = useToast();
  const dialog = useDialog();
  const { meta } = useMeta();

  const post = useAsync(() => api.getPost(id), [id]);
  const audit = useAsync(() => api.postAudit(id), [id]);
  const publishers = useAsync(() => api.listConnectors("publisher"), []);

  const [draft, setDraft] = useState<PostDraft | null>(null);
  const [stale, setStale] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [rejectOpen, setRejectOpen] = useState(false);

  const base = useMemo(
    () => (post.data ? toDraft(post.data) : null),
    [post.data],
  );
  useEffect(() => {
    if (base) {
      setDraft(base);
      setStale(false);
    }
  }, [base]);

  const changes = base && draft ? clean(diffDraft(base, draft)) : {};
  const dirty = Object.keys(changes).length > 0;

  useServerEvent("post.updated", (d) => {
    if (d.id !== id) return;
    void audit.reload();
    if (dirty) setStale(true);
    else void post.reload();
  });
  useServerEvent("post.deleted", (d) => {
    if (d.id === id) {
      toast.info("This post was deleted");
      router.push("/");
    }
  });

  // Warn before leaving with unsaved edits.
  useEffect(() => {
    if (!dirty) return;
    const h = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", h);
    return () => window.removeEventListener("beforeunload", h);
  }, [dirty]);

  const save = useCallback(async (): Promise<Post | null> => {
    if (!post.data || !dirty) return post.data ?? null;
    setBusy("save");
    try {
      const updated = await api.updatePost(id, {
        ...changes,
        version: post.data.version,
      } as PostPatch);
      post.setData(updated);
      toast.success("Saved");
      return updated;
    } catch (e) {
      if (e instanceof ApiError && e.code === "version_conflict") {
        setStale(true);
        toast.error(
          "Someone else changed this post. Reload to see the latest version.",
        );
      } else toast.error(e);
      return null;
    } finally {
      setBusy(null);
    }
  }, [post, dirty, changes, id, toast]);

  const act = async (
    name: string,
    fn: () => Promise<Post | void>,
    msg: string,
  ) => {
    setBusy(name);
    try {
      const res = await fn();
      if (res) post.setData(res);
      else await post.reload();
      void audit.reload();
      toast.success(msg);
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };

  if (!id) return <ErrorBox>Missing post id.</ErrorBox>;
  if (post.error && !post.data)
    return <ErrorBox onRetry={post.reload}>{post.error}</ErrorBox>;
  if (!post.data || !draft || !meta) return <Loading />;

  const p = post.data;
  const allowed = new Set(meta.transitions[p.status] ?? []);
  const editable = EDITABLE.has(p.status);
  const pm = Object.fromEntries(meta.platforms.map((x) => [x.id, x]));

  const approve = async () => {
    if (
      accountMode(
        effectivePublisher(draft, publishers.data ?? []),
        meta.connector_types,
      ) === "required" &&
      draft.accounts.length === 0
    ) {
      toast.error("Choose at least one account to publish to.");
      return;
    }
    const saved = dirty ? await save() : p;
    if (!saved) return;
    await act(
      "approve",
      () =>
        api.approvePost(id, {
          scheduled_at: saved.scheduled_at,
          publisher_connector_id: saved.publisher_connector_id,
        }),
      "Approved — handed to the publish queue",
    );
  };

  const sources = [
    ...(p.source_context?.items ?? []).map((s) => ({ ...s, kind: "item" })),
    ...(p.source_context?.search ?? []).map((s) => ({ ...s, kind: "search" })),
  ];

  return (
    <div>
      <div className="mb-2">
        <Link
          href="/"
          className="text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200"
        >
          ← Review
        </Link>
      </div>
      <PageHeader
        title={
          <span className="flex flex-wrap items-center gap-2">
            {p.title || <span className="text-zinc-400">Untitled post</span>}
            <StatusBadge status={p.status} />
          </span>
        }
        subtitle={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="font-mono text-xs">#{shortId(p.id)}</span>
            <span>Created {fmtTime(p.created_at)}</span>
            {p.pipeline_id && (
              <Link
                className="hover:underline"
                href={`/schedules/simple/?id=${p.pipeline_id}`}
              >
                Pipeline
              </Link>
            )}
            {p.run_id && (
              <Link
                className="hover:underline"
                href={`/runs/view/?id=${p.run_id}`}
              >
                Run #{shortId(p.run_id)}
              </Link>
            )}
            {p.task_id && <TaskLink id={p.task_id} />}
            <span>v{p.version}</span>
          </span>
        }
        actions={
          <>
            {editable && (
              <Button
                onClick={() => void save()}
                disabled={!dirty}
                loading={busy === "save"}
              >
                Save
              </Button>
            )}
            {allowed.has("PENDING_REVIEW") && (
              <Button
                variant="primary"
                loading={busy === "submit"}
                onClick={async () => {
                  if (dirty && !(await save())) return;
                  await act(
                    "submit",
                    () => api.submitPost(id),
                    "Submitted for review",
                  );
                }}
              >
                Submit for review
              </Button>
            )}
            {allowed.has("REJECTED") && (
              <Button
                variant="danger"
                onClick={() => setRejectOpen(true)}
                disabled={!!busy}
              >
                Reject
              </Button>
            )}
            {allowed.has("APPROVED") && (
              <Button
                variant="success"
                onClick={approve}
                loading={busy === "approve"}
              >
                <Icon name="check" /> Approve &amp; publish
              </Button>
            )}
            {p.status === "FAILED" && allowed.has("QUEUED") && (
              <Button
                variant="primary"
                loading={busy === "retry"}
                onClick={() =>
                  act(
                    "retry",
                    () => api.retryPost(id),
                    "Re-queued for publishing",
                  )
                }
              >
                <Icon name="refresh" /> Retry
              </Button>
            )}
            {(p.status === "DRAFT" || p.status === "REJECTED") && (
              <Button
                variant="ghost"
                loading={busy === "delete"}
                onClick={async () => {
                  if (
                    !(await dialog.confirm({
                      title: "Delete this post?",
                      message:
                        "The draft and its audit history are deleted permanently.",
                      confirmLabel: "Delete post",
                      tone: "danger",
                    }))
                  )
                    return;
                  setBusy("delete");
                  try {
                    await api.deletePost(id);
                    toast.success("Deleted");
                    router.push("/");
                  } catch (e) {
                    toast.error(e);
                    setBusy(null);
                  }
                }}
              >
                <Icon name="trash" /> Delete
              </Button>
            )}
          </>
        }
      />

      <div className="space-y-3">
        {stale && (
          <ErrorBox onRetry={() => void post.reload()}>
            This post was changed elsewhere (status or content). Reload to get
            the latest version
            {dirty ? " — your unsaved edits will be discarded." : "."}
          </ErrorBox>
        )}
        {p.status === "FAILED" && (
          <div className="rounded-md border border-red-200 bg-red-50 px-4 py-3 dark:border-red-900/60 dark:bg-red-950/40">
            <div className="text-sm font-semibold text-red-800 dark:text-red-300">
              Publishing failed
              {p.attempts
                ? ` after ${p.attempts} attempt${p.attempts > 1 ? "s" : ""}`
                : ""}
            </div>
            <pre className="mt-1 font-mono text-xs break-words whitespace-pre-wrap text-red-700 dark:text-red-300">
              {p.error_message || "No error message returned."}
            </pre>
          </div>
        )}
        {p.status === "REJECTED" && p.review_note && (
          <div className="rounded-md border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-300">
            <span className="font-semibold">Rejected:</span> {p.review_note}
          </div>
        )}
        {p.status === "PUBLISHED" && (
          <div className="rounded-md border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800 dark:border-emerald-900/60 dark:bg-emerald-950/40 dark:text-emerald-300">
            Published {fmtTime(p.published_at)}
            {p.external_post_ids &&
              Object.keys(p.external_post_ids).length > 0 && (
                <span className="ml-2 font-mono text-xs">
                  {Object.entries(p.external_post_ids)
                    .map(([k, v]) => `${k}: ${v}`)
                    .join(" · ")}
                </span>
              )}
          </div>
        )}
        {!editable && p.status !== "FAILED" && (
          <p className="text-xs text-zinc-500">
            Content is read-only once a post leaves review.
          </p>
        )}
        <Warnings warnings={p.warnings} />
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-[minmax(0,1fr)_380px]">
        <PostEditor
          draft={draft}
          onChange={setDraft}
          platforms={meta.platforms}
          publishers={publishers.data ?? []}
          connectorTypes={meta.connector_types}
          readOnly={!editable}
        />

        <div className="space-y-4">
          <Card>
            <CardHeader
              title="Preview"
              actions={draft.platforms.map((x) => (
                <PlatformBadge key={x} id={x} name={pm[x]?.name} />
              ))}
            />
            <PostPreviews draft={draft} platforms={meta.platforms} />
          </Card>

          <Card>
            <CardHeader
              title="Source context"
              subtitle={p.source_context?.topic || undefined}
            />
            {sources.length === 0 ? (
              <p className="px-4 py-4 text-sm text-zinc-500">
                No sources recorded (manual post).
              </p>
            ) : (
              <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
                {sources.map((s, i) => (
                  <li key={i} className="px-4 py-2.5">
                    <a
                      href={s.url}
                      target="_blank"
                      rel="noreferrer"
                      className="line-clamp-2 text-sm font-medium text-zinc-800 hover:text-indigo-600 dark:text-zinc-200 dark:hover:text-indigo-400"
                    >
                      {s.title || s.url}
                    </a>
                    <div className="mt-0.5 flex items-center gap-2 text-xs text-zinc-500">
                      <span>
                        {s.kind === "search"
                          ? "web search"
                          : s.source || "source"}
                      </span>
                      {s.url && (
                        <span className="truncate">{hostOf(s.url)}</span>
                      )}
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card>
            <CardHeader title="Audit trail" />
            {audit.error ? (
              <div className="p-3">
                <ErrorBox onRetry={audit.reload}>{audit.error}</ErrorBox>
              </div>
            ) : audit.data ? (
              <AuditTimeline logs={audit.data} />
            ) : (
              <Loading />
            )}
          </Card>
        </div>
      </div>

      <RejectDialog
        open={rejectOpen}
        onClose={() => setRejectOpen(false)}
        onConfirm={async (reason) => {
          await act(
            "reject",
            () => api.rejectPost(id, reason),
            "Rejected and archived",
          );
        }}
      />
    </div>
  );
}

export default function Page() {
  return (
    <Suspense fallback={<Loading />}>
      <PostView />
    </Suspense>
  );
}

// TaskLink shows which board task's agent drafted this post.
function TaskLink({ id }: { id: string }) {
  const task = useAsync(() => api.getTask(id), [id]);
  return (
    <Link
      className="text-indigo-600 hover:underline dark:text-indigo-400"
      href={`/tasks/view/?id=${id}`}
    >
      Drafted for task {task.data ? `#${task.data.number}` : "…"}
      {task.data?.assignee
        ? ` by ${task.data.assignee.avatar} ${task.data.assignee.name}`
        : ""}
    </Link>
  );
}
