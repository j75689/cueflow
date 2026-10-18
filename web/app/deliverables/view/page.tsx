"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useMemo, useState } from "react";
import { StatusBadge } from "@/components/badges";
import { useDialog } from "@/components/dialog";
import { JsonInput } from "@/components/inputs";
import { type PostDraft, PostEditor, PostPreviews, accountMode, effectivePublisher } from "@/components/post-editor";
import { useOrgFollow } from "@/components/org";
import { AuditTimeline, RejectDialog } from "@/components/post";
import { useMeta, useServerEvent, useToast } from "@/components/providers";
import {
  Button,
  Card,
  CardHeader,
  ErrorBox,
  Field,
  Icon,
  Input,
  Loading,
  PageHeader,
  Pill,
  Textarea,
} from "@/components/ui";
import { ApiError, api } from "@/lib/api";
import { fmtTime, shortId } from "@/lib/format";
import { useAsync } from "@/lib/hooks";
import type { Connector, ConnectorSpec, Deliverable, DeliverableKind, Platform } from "@/lib/types";

const EDITABLE = new Set(["DRAFT", "PENDING_REVIEW"]);

interface Draft {
  title: string;
  payload: Record<string, unknown>;
  action_connector_id: string | null;
}

function toDraft(d: Deliverable): Draft {
  return {
    title: d.title ?? "",
    payload: d.payload ?? {},
    action_connector_id: d.action_connector_id,
  };
}

function same(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

// A post deliverable's payload has the fields of a post; the post editor edits it as one.
export function postDraftOf(title: string, payload: Record<string, unknown>): PostDraft {
  const str = (k: string) => (typeof payload[k] === "string" ? (payload[k] as string) : "");
  const list = (k: string) => (Array.isArray(payload[k]) ? (payload[k] as string[]) : []);
  return {
    title,
    content: str("content"),
    thread_parts: list("thread_parts"),
    hashtags: list("hashtags"),
    media_urls: list("media_urls"),
    image_prompt: str("image_prompt"),
    platforms: list("platforms"),
    accounts: list("accounts"),
    topic: str("topic"),
    link_url: str("link_url"),
    scheduled_at: typeof payload.scheduled_at === "string" ? (payload.scheduled_at as string) : null,
    publisher_connector_id:
      typeof payload.publisher_connector_id === "string" ? (payload.publisher_connector_id as string) : null,
  };
}

function payloadOf(d: PostDraft, previous: Record<string, unknown>): Record<string, unknown> {
  const { title: _title, ...rest } = d;
  return { ...previous, ...rest };
}

// PayloadEditor edits a post with the post editor, a document as text and any other kind as JSON.
function PayloadEditor({
  kind,
  title,
  onTitle,
  value,
  onChange,
  readOnly,
  platforms,
  publishers,
  connectorTypes,
}: {
  kind: string;
  title: string;
  onTitle: (t: string) => void;
  value: Record<string, unknown>;
  onChange: (v: Record<string, unknown>) => void;
  readOnly: boolean;
  platforms: Platform[];
  publishers: Connector[];
  connectorTypes: ConnectorSpec[];
}) {
  if (kind === "post") {
    const draft = postDraftOf(title, value);
    return (
      <PostEditor
        draft={draft}
        onChange={(d) => {
          if (d.title !== title) onTitle(d.title);
          onChange(payloadOf(d, value));
        }}
        platforms={platforms}
        publishers={publishers}
        connectorTypes={connectorTypes}
        readOnly={readOnly}
      />
    );
  }
  if (kind === "document") {
    const content = typeof value.content === "string" ? value.content : "";
    const summary = typeof value.summary === "string" ? value.summary : "";
    return (
      <div className="space-y-3 p-4">
        <Field label="Summary" help="One or two sentences for the reviewer (optional)">
          <Input
            value={summary}
            readOnly={readOnly}
            onChange={(e) => onChange({ ...value, summary: e.target.value })}
          />
        </Field>
        <Field label="Content" help="Markdown">
          <Textarea
            value={content}
            readOnly={readOnly}
            rows={22}
            className="font-mono text-sm"
            onChange={(e) => onChange({ ...value, content: e.target.value })}
          />
        </Field>
      </div>
    );
  }
  return (
    <div className="p-4">
      <Field label="Payload" help="JSON, as the kind expects it">
        <JsonInput value={value} disabled={readOnly} onChange={(v) => onChange((v as Record<string, unknown>) ?? {})} />
      </Field>
    </div>
  );
}

function ActionPicker({
  value,
  onChange,
  actions,
  readOnly,
}: {
  value: string | null;
  onChange: (v: string | null) => void;
  actions: Connector[];
  readOnly: boolean;
}) {
  const active = actions.filter((c) => c.is_active);
  return (
    <Field
      label="Action after approval"
      help={
        active.length === 0
          ? "No action connector is configured: approving marks the item done with nothing run."
          : "Which action connector runs once this is approved (default: the first active one)."
      }
    >
      <select
        value={value ?? ""}
        disabled={readOnly || active.length === 0}
        onChange={(e) => onChange(e.target.value || null)}
        className="w-full rounded-md border border-zinc-300 bg-white px-2 py-1.5 text-sm dark:border-zinc-700 dark:bg-zinc-900"
      >
        <option value="">{active.length ? `Default (${active[0].name})` : "None"}</option>
        {active.map((c) => (
          <option key={c.id} value={c.id}>
            {c.name}
          </option>
        ))}
      </select>
    </Field>
  );
}

// NewDeliverable is the page without an id: write a document and submit it.
function NewDeliverable({ kinds, actions }: { kinds: DeliverableKind[]; actions: Connector[] }) {
  const router = useRouter();
  const toast = useToast();
  const { meta } = useMeta();
  const publishers = useAsync(() => api.listConnectors("publisher"), []);
  const [kind, setKind] = useState(kinds[0]?.id ?? "document");
  const [draft, setDraft] = useState<Draft>({
    title: "",
    payload: {},
    action_connector_id: null,
  });
  const [busy, setBusy] = useState<"draft" | "submit" | null>(null);
  const create = async (submit: boolean) => {
    setBusy(submit ? "submit" : "draft");
    try {
      const d = await api.createDeliverable({
        kind,
        title: draft.title,
        payload: draft.payload,
        action_connector_id: draft.action_connector_id,
        submit,
      });
      toast.success(submit ? "Submitted for review" : "Saved as a draft");
      router.push(`/deliverables/view/?id=${d.id}`);
    } catch (e) {
      toast.error(e);
      setBusy(null);
    }
  };
  const chosen = kinds.find((k) => k.id === kind);
  return (
    <div>
      <div className="mb-2">
        <Link href="/deliverables/" className="text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200">
          ← Deliverables
        </Link>
      </div>
      <PageHeader
        title="New deliverable"
        subtitle="Written by you; it goes through the same review as what agents produce."
        actions={
          <>
            <Button onClick={() => void create(false)} loading={busy === "draft"} disabled={!!busy}>
              Save draft
            </Button>
            <Button variant="primary" onClick={() => void create(true)} loading={busy === "submit"} disabled={!!busy}>
              Submit for review
            </Button>
          </>
        }
      />
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_380px]">
        <Card>
          <div className="space-y-3 p-4 pb-0">
            {kind !== "post" && (
              <Field label="Title">
                <Input value={draft.title} onChange={(e) => setDraft({ ...draft, title: e.target.value })} />
              </Field>
            )}
            {kinds.length > 1 && (
              <Field label="Kind">
                <select
                  value={kind}
                  onChange={(e) => {
                    setKind(e.target.value);
                    setDraft({ ...draft, payload: {} });
                  }}
                  className="w-full rounded-md border border-zinc-300 bg-white px-2 py-1.5 text-sm dark:border-zinc-700 dark:bg-zinc-900"
                >
                  {kinds.map((k) => (
                    <option key={k.id} value={k.id}>
                      {k.name}
                    </option>
                  ))}
                </select>
              </Field>
            )}
          </div>
          <PayloadEditor
            kind={kind}
            title={draft.title}
            onTitle={(title) => setDraft((cur) => ({ ...cur, title }))}
            value={draft.payload}
            onChange={(payload) => setDraft((cur) => ({ ...cur, payload }))}
            readOnly={false}
            platforms={meta?.platforms ?? []}
            publishers={publishers.data ?? []}
            connectorTypes={meta?.connector_types ?? []}
          />
        </Card>
        <Card>
          <div className="p-4">
            <ActionPicker
              value={draft.action_connector_id}
              onChange={(v) => setDraft({ ...draft, action_connector_id: v })}
              actions={actions}
              readOnly={false}
            />
            {chosen?.description && <p className="mt-3 text-xs text-zinc-500">{chosen.description}</p>}
          </div>
        </Card>
      </div>
    </div>
  );
}

function DeliverableView() {
  const id = useSearchParams().get("id") ?? "";
  const router = useRouter();
  const toast = useToast();
  const dialog = useDialog();
  const { meta } = useMeta();
  const actions = useAsync(() => api.listConnectors("action"), []);
  const publishers = useAsync(() => api.listConnectors("publisher"), []);

  const item = useAsync(() => (id ? api.getDeliverable(id) : Promise.resolve(null)), [id]);
  const audit = useAsync(() => (id ? api.deliverableAudit(id) : Promise.resolve([])), [id]);
  useOrgFollow(item.data?.org_id);

  const [draft, setDraft] = useState<Draft | null>(null);
  const [stale, setStale] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [rejectOpen, setRejectOpen] = useState(false);

  const base = useMemo(() => (item.data ? toDraft(item.data) : null), [item.data]);
  useEffect(() => {
    if (base) {
      setDraft(base);
      setStale(false);
    }
  }, [base]);
  const dirty = !!(base && draft && !same(base, draft));

  useServerEvent("deliverable.updated", (d) => {
    if (!d.resync && d.id !== id) return;
    void audit.reload();
    if (dirty) setStale(true);
    else void item.reload();
  });
  useServerEvent("deliverable.deleted", (d) => {
    if (d.id === id) {
      toast.info("This deliverable was removed");
      router.push("/deliverables/");
    }
  });
  useEffect(() => {
    if (!dirty) return;
    const h = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", h);
    return () => window.removeEventListener("beforeunload", h);
  }, [dirty]);

  const kinds = meta?.deliverable_kinds ?? [];
  if (!id) return <NewDeliverable kinds={kinds} actions={actions.data ?? []} />;
  if (item.error && !item.data) return <ErrorBox onRetry={item.reload}>{item.error}</ErrorBox>;
  if (!item.data || !draft || !meta) return <Loading />;

  const d = item.data;
  const allowed = new Set(meta.deliverable_transitions?.[d.status] ?? []);
  const editable = EDITABLE.has(d.status);
  const kind = kinds.find((k) => k.id === d.kind);

  const save = async (): Promise<Deliverable | null> => {
    if (!dirty || !base) return d;
    setBusy("save");
    try {
      const body: Parameters<typeof api.updateDeliverable>[1] = {
        version: d.version,
      };
      if (draft.title !== base.title) body.title = draft.title;
      if (!same(draft.payload, base.payload)) body.payload = draft.payload;
      if (draft.action_connector_id !== base.action_connector_id) body.action_connector_id = draft.action_connector_id;
      const updated = await api.updateDeliverable(id, body);
      item.setData(updated);
      toast.success("Saved");
      return updated;
    } catch (e) {
      if (e instanceof ApiError && e.code === "version_conflict") {
        setStale(true);
        toast.error("Someone else changed this item. Reload to see the latest version.");
      } else toast.error(e);
      return null;
    } finally {
      setBusy(null);
    }
  };

  const act = async (name: string, fn: () => Promise<Deliverable | void>, msg: string) => {
    setBusy(name);
    try {
      const res = await fn();
      if (res) item.setData(res);
      else await item.reload();
      void audit.reload();
      toast.success(msg);
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };

  const external = Object.entries(d.external_ids ?? {});
  const resultEntries = Object.entries(d.result ?? {});

  return (
    <div>
      <div className="mb-2">
        <Link href="/deliverables/" className="text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200">
          ← Deliverables
        </Link>
      </div>
      <PageHeader
        title={
          <span className="flex flex-wrap items-center gap-2">
            {d.title || <span className="text-zinc-400">Untitled</span>}
            <StatusBadge status={d.status} />
          </span>
        }
        subtitle={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <Pill>{kind?.name ?? d.kind}</Pill>
            <span className="font-mono text-xs">#{shortId(d.id)}</span>
            <span>Created {fmtTime(d.created_at)}</span>
            <span>{d.created_by === "system_agent" ? "by an agent" : "by a person"}</span>
            {d.task_id && (
              <Link
                className="text-indigo-600 hover:underline dark:text-indigo-400"
                href={`/tasks/view/?id=${d.task_id}`}
              >
                Task
              </Link>
            )}
            <span>v{d.version}</span>
          </span>
        }
        actions={
          <>
            {editable && (
              <Button onClick={() => void save()} disabled={!dirty} loading={busy === "save"}>
                Save
              </Button>
            )}
            {allowed.has("PENDING_REVIEW") && (
              <Button
                variant="primary"
                loading={busy === "submit"}
                onClick={async () => {
                  if (dirty && !(await save())) return;
                  await act("submit", () => api.submitDeliverable(id), "Submitted for review");
                }}
              >
                Submit for review
              </Button>
            )}
            {allowed.has("REJECTED") && (
              <Button variant="danger" onClick={() => setRejectOpen(true)} disabled={!!busy}>
                Reject
              </Button>
            )}
            {allowed.has("APPROVED") && (
              <Button
                variant="success"
                loading={busy === "approve"}
                onClick={async () => {
                  if (d.kind === "post") {
                    const pd = postDraftOf(draft.title, draft.payload);
                    if (
                      accountMode(effectivePublisher(pd, publishers.data ?? []), meta.connector_types) === "required" &&
                      pd.accounts.length === 0
                    ) {
                      toast.error("Choose at least one account to publish to.");
                      return;
                    }
                  }
                  const saved = dirty ? await save() : d;
                  if (!saved) return;
                  await act(
                    "approve",
                    () => api.approveDeliverable(id),
                    d.kind === "post" ? "Approved — handed to the publisher" : "Approved",
                  );
                }}
              >
                <Icon name="check" /> Approve
              </Button>
            )}
            {d.status === "FAILED" && allowed.has("QUEUED") && (
              <Button
                variant="primary"
                loading={busy === "retry"}
                onClick={() => act("retry", () => api.retryDeliverable(id), "Re-queued")}
              >
                <Icon name="refresh" /> Retry
              </Button>
            )}
            {(d.status === "DRAFT" || d.status === "REJECTED") && (
              <Button
                variant="ghost"
                loading={busy === "remove"}
                onClick={async () => {
                  if (
                    !(await dialog.confirm({
                      title: "Remove this deliverable?",
                      message: "The item and its history are removed permanently.",
                      confirmLabel: "Remove",
                      tone: "danger",
                    }))
                  )
                    return;
                  setBusy("remove");
                  try {
                    await api.deleteDeliverable(id);
                    toast.success("Removed");
                    router.push("/deliverables/");
                  } catch (e) {
                    toast.error(e);
                    setBusy(null);
                  }
                }}
              >
                <Icon name="trash" /> Remove
              </Button>
            )}
          </>
        }
      />

      <div className="space-y-3">
        {stale && (
          <ErrorBox onRetry={() => void item.reload()}>
            This item was changed elsewhere. Reload to get the latest version
            {dirty ? " — your unsaved edits will be discarded." : "."}
          </ErrorBox>
        )}
        {d.status === "FAILED" && (
          <div className="rounded-md border border-red-200 bg-red-50 px-4 py-3 dark:border-red-900/60 dark:bg-red-950/40">
            <div className="text-sm font-semibold text-red-800 dark:text-red-300">
              Action failed
              {d.attempts ? ` after ${d.attempts} attempt${d.attempts > 1 ? "s" : ""}` : ""}
            </div>
            <pre className="mt-1 font-mono text-xs break-words whitespace-pre-wrap text-red-700 dark:text-red-300">
              {d.error_message || "No error message returned."}
            </pre>
          </div>
        )}
        {d.status === "REJECTED" && d.review_note && (
          <div className="rounded-md border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-300">
            <span className="font-semibold">Rejected:</span> {d.review_note}
          </div>
        )}
        {d.status === "DONE" && (
          <div className="rounded-md border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800 dark:border-emerald-900/60 dark:bg-emerald-950/40 dark:text-emerald-300">
            Done {fmtTime(d.acted_at)}
            {external.length > 0 ? (
              <span className="ml-2 font-mono text-xs">{external.map(([k, v]) => `${k}: ${v}`).join(" · ")}</span>
            ) : (
              <span className="ml-2 text-xs">(approved; no action connector was configured)</span>
            )}
          </div>
        )}
        {!editable && d.status !== "FAILED" && (
          <p className="text-xs text-zinc-500">Content is read-only once an item leaves review.</p>
        )}
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-[minmax(0,1fr)_380px]">
        <Card>
          <div className="p-4 pb-0">
            {d.kind !== "post" && (
              <Field label="Title">
                <Input
                  value={draft.title}
                  readOnly={!editable}
                  onChange={(e) => setDraft({ ...draft, title: e.target.value })}
                />
              </Field>
            )}
          </div>
          <PayloadEditor
            kind={d.kind}
            title={draft.title}
            onTitle={(title) => setDraft((cur) => (cur ? { ...cur, title } : cur))}
            value={draft.payload}
            onChange={(payload) => setDraft((cur) => (cur ? { ...cur, payload } : cur))}
            readOnly={!editable}
            platforms={meta.platforms}
            publishers={publishers.data ?? []}
            connectorTypes={meta.connector_types}
          />
        </Card>

        <div className="space-y-4">
          {d.kind === "post" && (
            <Card>
              <CardHeader title="Preview" />
              <PostPreviews draft={postDraftOf(draft.title, draft.payload)} platforms={meta.platforms} />
            </Card>
          )}
          <Card>
            <CardHeader title={d.kind === "post" ? "Publishing" : "Delivery"} />
            <div className="p-4">
              <ActionPicker
                value={draft.action_connector_id}
                onChange={(v) => setDraft({ ...draft, action_connector_id: v })}
                actions={actions.data ?? []}
                readOnly={!editable}
              />
              {resultEntries.length > 0 && (
                <div className="mt-3">
                  <div className="mb-1 text-xs font-medium text-zinc-500">What the action reported</div>
                  <pre className="overflow-x-auto rounded bg-zinc-50 px-2 py-1 font-mono text-xs whitespace-pre-wrap dark:bg-zinc-800/60">
                    {JSON.stringify(d.result, null, 2)}
                  </pre>
                </div>
              )}
            </div>
          </Card>
          {kind?.description && (
            <Card>
              <CardHeader title={kind.name} />
              <p className="px-4 pb-4 text-sm text-zinc-600 dark:text-zinc-400">{kind.description}</p>
            </Card>
          )}
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
        title="Reject deliverable"
        onClose={() => setRejectOpen(false)}
        onConfirm={async (reason) => {
          await act("reject", () => api.rejectDeliverable(id, reason), "Rejected");
        }}
      />
    </div>
  );
}

export default function Page() {
  return (
    <Suspense fallback={<Loading />}>
      <DeliverableView />
    </Suspense>
  );
}
