"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useMemo, useState } from "react";
import { HealthDot, KIND_LABEL, KIND_ORDER } from "@/components/badges";
import {
  type ConfigValues,
  ConnectorFields,
  buildPayload,
  initialValues,
  missingRequired,
} from "@/components/connector-form";
import { useDialog } from "@/components/dialog";
import { useMeta, useToast } from "@/components/providers";
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
  Toggle,
  cx,
} from "@/components/ui";
import { Select } from "@/components/select";
import { api, errorMessage } from "@/lib/api";
import { fmtRelative, fmtTime } from "@/lib/format";
import { useAsync } from "@/lib/hooks";
import type {
  ConnectorSpec,
  ConnectorTestResult,
  ConnectorTool,
  SourceItem,
  ToolPermission,
  ToolPermissionValue,
} from "@/lib/types";

function TypePicker({
  specs,
  onPick,
}: {
  specs: ConnectorSpec[];
  onPick: (s: ConnectorSpec) => void;
}) {
  const kinds = [
    ...new Set([...KIND_ORDER, ...specs.map((s) => s.kind)]),
  ].filter((k) => specs.some((s) => s.kind === k));
  return (
    <div className="space-y-5">
      {kinds.map((k) => (
        <section key={k}>
          <h2 className="mb-2 text-xs font-semibold tracking-wide text-zinc-500 uppercase">
            {KIND_LABEL[k] ?? k}
          </h2>
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {specs
              .filter((s) => s.kind === k)
              .map((s) => (
                <button
                  key={s.type}
                  onClick={() => onPick(s)}
                  className="rounded-lg border border-zinc-200 bg-white p-3.5 text-left transition hover:border-indigo-400 hover:shadow-sm dark:border-zinc-800 dark:bg-zinc-900 dark:hover:border-indigo-500"
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">
                      {s.name}
                    </span>
                    <span className="font-mono text-[10px] text-zinc-400">
                      {s.type}
                    </span>
                  </div>
                  <p className="mt-1 line-clamp-2 text-xs text-zinc-500">
                    {s.description}
                  </p>
                </button>
              ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function TestPanel({ result }: { result: ConnectorTestResult | null }) {
  if (!result) return null;
  return (
    <div
      className={cx(
        "rounded-md border px-3 py-2 text-sm",
        result.ok
          ? "border-emerald-200 bg-emerald-50 text-emerald-900 dark:border-emerald-900/60 dark:bg-emerald-950/40 dark:text-emerald-300"
          : "border-red-200 bg-red-50 text-red-900 dark:border-red-900/60 dark:bg-red-950/40 dark:text-red-300",
      )}
    >
      <div className="font-medium">
        {result.ok ? "Connection OK" : "Connection failed"}
      </div>
      {result.message && (
        <div className="mt-0.5 break-words">{result.message}</div>
      )}
      {result.details && Object.keys(result.details).length > 0 && (
        <pre className="mt-2 max-h-48 overflow-auto rounded bg-white/60 p-2 font-mono text-[11px] dark:bg-black/30">
          {JSON.stringify(result.details, null, 2)}
        </pre>
      )}
    </div>
  );
}

function ToolsPanel({ id }: { id: string }) {
  const [tools, setTools] = useState<ConnectorTool[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const load = async () => {
    setBusy(true);
    setErr(null);
    try {
      setTools(await api.connectorTools(id));
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card>
      <CardHeader
        title="MCP tools"
        subtitle="Tools discovered via tools/list. Publishing tools are never exposed to the agent."
        actions={
          <Button size="sm" onClick={load} loading={busy}>
            <Icon name="tool" className="h-3.5 w-3.5" />{" "}
            {tools ? "Refresh" : "List tools"}
          </Button>
        }
      />
      {err && (
        <div className="p-3">
          <ErrorBox>{err}</ErrorBox>
        </div>
      )}
      {tools && tools.length === 0 && (
        <p className="px-4 py-4 text-sm text-zinc-500">
          The server exposes no tools.
        </p>
      )}
      {tools && tools.length > 0 && (
        <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
          {tools.map((t) => (
            <li key={t.name} className="px-4 py-2.5">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-xs font-semibold text-zinc-800 dark:text-zinc-200">
                  {t.name}
                </span>
                {t.agent_allowed ? (
                  <Pill className="bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300">
                    agent allowed
                  </Pill>
                ) : (
                  <Pill className="bg-zinc-200 text-zinc-700 dark:bg-zinc-800 dark:text-zinc-300">
                    worker only{t.reason ? ` · ${t.reason}` : ""}
                  </Pill>
                )}
              </div>
              {t.description && (
                <p className="mt-0.5 line-clamp-2 text-xs text-zinc-500">
                  {t.description}
                </p>
              )}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

const PERMISSION_LABEL: Record<ToolPermissionValue, string> = {
  allow: "Allow",
  ask: "Ask first",
  deny: "Deny",
};

/** Per-tool permissions for an MCP tool server: allow, ask (approve each call) or deny. */
function ToolPermissionsPanel({ id }: { id: string }) {
  const toast = useToast();
  const dialog = useDialog();
  const data = useAsync(() => api.toolPermissions(id), [id]);
  const [perms, setPerms] = useState<Record<string, ToolPermissionValue>>({});
  const [saving, setSaving] = useState(false);
  const tools: ToolPermission[] = data.data?.tools ?? [];

  useEffect(() => {
    if (data.data)
      setPerms(
        Object.fromEntries(data.data.tools.map((t) => [t.name, t.permission])),
      );
  }, [data.data]);

  const dirty = tools.some(
    (t) => perms[t.name] && perms[t.name] !== t.permission,
  );

  const change = async (t: ToolPermission, p: ToolPermissionValue) => {
    if (p === "allow" && t.risky) {
      const ok = await dialog.confirm({
        title: `Allow ${t.name} without asking?`,
        message:
          "This tool may publish, message people, delete data or move money. Agents will call it on their own, outside CueFlow's review. You can keep it on “Ask first” to approve each call instead.",
        confirmLabel: "Allow anyway",
        tone: "danger",
      });
      if (!ok) return;
    }
    setPerms((cur) => ({ ...cur, [t.name]: p }));
  };

  const save = async () => {
    setSaving(true);
    try {
      await api.setToolPermissions(id, perms);
      toast.success("Tool permissions saved");
      void data.reload();
    } catch (e) {
      toast.error(e);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <CardHeader
        title="Tool permissions"
        subtitle="What agents may do with each tool. New tools start as Allow when read-only, otherwise Ask first."
        actions={
          <>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => void data.reload()}
              loading={data.loading}
            >
              Refresh
            </Button>
            <Button
              size="sm"
              variant="primary"
              onClick={save}
              loading={saving}
              disabled={!dirty}
            >
              Save
            </Button>
          </>
        }
      />
      {data.error && (
        <div className="p-3">
          <ErrorBox onRetry={data.reload}>{data.error}</ErrorBox>
        </div>
      )}
      {data.loading && !data.data && <Loading />}
      {data.data && tools.length === 0 && (
        <p className="px-4 py-4 text-sm text-zinc-500">
          The server exposes no tools.
        </p>
      )}
      {tools.length > 0 && (
        <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
          {tools.map((t) => {
            const p = perms[t.name] ?? t.permission;
            return (
              <li key={t.name} className="flex items-start gap-3 px-4 py-2.5">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="font-mono text-xs font-semibold text-zinc-800 dark:text-zinc-200">
                      {t.name}
                    </span>
                    {t.read_only && (
                      <Pill className="bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300">
                        read-only
                      </Pill>
                    )}
                    {t.risky && (
                      <Pill className="bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300">
                        risky
                      </Pill>
                    )}
                    {p === "allow" && t.risky && (
                      <span className="text-[11px] text-red-600 dark:text-red-400">
                        runs without your approval
                      </span>
                    )}
                  </div>
                  {t.description && (
                    <p className="mt-0.5 line-clamp-2 text-xs text-zinc-500">
                      {t.description}
                    </p>
                  )}
                </div>
                <div className="w-44 shrink-0">
                  <Select
                    aria-label={`Permission for ${t.name}`}
                    value={p}
                    onChange={(e) =>
                      void change(t, e.target.value as ToolPermissionValue)
                    }
                  >
                    {(["allow", "ask", "deny"] as const).map((v) => (
                      <option key={v} value={v}>
                        {PERMISSION_LABEL[v]}
                        {v === t.default ? " (default)" : ""}
                      </option>
                    ))}
                  </Select>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}

function ItemsPanel({ id }: { id: string }) {
  const toast = useToast();
  const items = useAsync<SourceItem[]>(() => api.connectorItems(id, 50), [id]);
  const [busy, setBusy] = useState(false);
  const fetchNow = async () => {
    setBusy(true);
    try {
      const r = await api.fetchConnector(id);
      toast.success(
        `Fetched ${r.new_items} new item${r.new_items === 1 ? "" : "s"}`,
      );
      await items.reload();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card>
      <CardHeader
        title="Collected items"
        subtitle="Newest first. Consumed items were already used by a pipeline run."
        actions={
          <Button size="sm" onClick={fetchNow} loading={busy}>
            <Icon name="refresh" className="h-3.5 w-3.5" /> Fetch now
          </Button>
        }
      />
      {items.error && (
        <div className="p-3">
          <ErrorBox onRetry={items.reload}>{items.error}</ErrorBox>
        </div>
      )}
      {items.loading && !items.data && <Loading />}
      {items.data?.length === 0 && (
        <p className="px-4 py-4 text-sm text-zinc-500">
          Nothing collected yet.
        </p>
      )}
      <ul className="max-h-[520px] divide-y divide-zinc-100 overflow-y-auto dark:divide-zinc-800">
        {(items.data ?? []).map((it) => (
          <li key={it.id} className="px-4 py-2.5">
            <div className="flex items-start justify-between gap-2">
              {it.url ? (
                <a
                  href={it.url}
                  target="_blank"
                  rel="noreferrer"
                  className="line-clamp-2 text-sm font-medium text-zinc-800 hover:text-indigo-600 dark:text-zinc-200 dark:hover:text-indigo-400"
                >
                  {it.title || it.url}
                </a>
              ) : (
                <span className="line-clamp-2 text-sm font-medium text-zinc-800 dark:text-zinc-200">
                  {it.title || it.external_id}
                </span>
              )}
              {it.consumed_at ? (
                <Pill>used</Pill>
              ) : (
                <Pill className="bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300">
                  new
                </Pill>
              )}
            </div>
            <div className="mt-0.5 flex flex-wrap gap-x-2 text-xs text-zinc-500">
              {it.author && <span>{it.author}</span>}
              <span title={fmtTime(it.published_at ?? it.fetched_at)}>
                {fmtRelative(it.published_at ?? it.fetched_at)}
              </span>
            </div>
            {it.content && (
              <p className="mt-1 line-clamp-2 text-xs text-zinc-600 dark:text-zinc-400">
                {it.content}
              </p>
            )}
          </li>
        ))}
      </ul>
    </Card>
  );
}

function ConnectorEdit() {
  const params = useSearchParams();
  const id = params.get("id") ?? "";
  const kindFilter = params.get("kind") ?? "";
  const isNew = !id;
  const router = useRouter();
  const toast = useToast();
  const dialog = useDialog();
  const { meta } = useMeta();

  const connector = useAsync(
    async () => (id ? api.getConnector(id) : null),
    [id],
  );
  const [spec, setSpec] = useState<ConnectorSpec | null>(null);
  const [name, setName] = useState("");
  const [active, setActive] = useState(true);
  const [values, setValues] = useState<ConfigValues>({});
  const [cleared, setCleared] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<ConnectorTestResult | null>(
    null,
  );

  // Load existing connector into the form.
  useEffect(() => {
    if (!meta || !connector.data) return;
    const c = connector.data;
    const s = meta.connector_types.find((t) => t.type === c.type) ?? null;
    setSpec(s);
    setName(c.name);
    setActive(c.is_active);
    setValues(initialValues(s?.fields ?? [], c.config));
    setCleared(new Set());
  }, [meta, connector.data]);

  const specs = useMemo(
    () =>
      (meta?.connector_types ?? []).filter(
        (s) => !kindFilter || s.kind === kindFilter,
      ),
    [meta, kindFilter],
  );

  if (!meta) return <Loading />;
  if (!isNew && connector.error)
    return <ErrorBox onRetry={connector.reload}>{connector.error}</ErrorBox>;
  if (!isNew && !connector.data) return <Loading />;

  const c = connector.data ?? null;
  const secretsSet = c?.secrets_set ?? [];
  const fields = spec?.fields ?? [];
  const missing = spec
    ? missingRequired(fields, values, secretsSet, cleared)
    : [];
  const isMCP =
    !!spec &&
    (spec.type.includes("mcp") || fields.some((f) => f.key === "transport"));

  const pick = (s: ConnectorSpec) => {
    setSpec(s);
    setName(""); // blank = the server names it from the settings (e.g. "Claude Code (ACP)")
    setValues(initialValues(s.fields));
    setCleared(new Set());
  };

  const save = async () => {
    if (!spec) return;
    setBusy("save");
    try {
      const config = buildPayload(fields, values, cleared, isNew);
      if (isNew) {
        const created = await api.createConnector({
          name: name.trim(),
          type: spec.type,
          is_active: active,
          config,
        });
        toast.success("Connector created");
        router.replace(`/connectors/edit/?id=${created.id}`);
      } else {
        const updated = await api.updateConnector(id, {
          name: name.trim(),
          is_active: active,
          config,
        });
        connector.setData(updated);
        toast.success("Saved");
      }
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };

  const test = async () => {
    setBusy("test");
    setTestResult(null);
    try {
      const r = await api.testConnector(id);
      setTestResult(r);
      void connector.reload();
    } catch (e) {
      setTestResult({ ok: false, message: errorMessage(e) });
    } finally {
      setBusy(null);
    }
  };

  const remove = async () => {
    if (
      !(await dialog.confirm({
        title: "Delete connector?",
        message:
          "Pipelines and agents using it will lose the reference. This cannot be undone.",
        confirmLabel: "Delete connector",
        tone: "danger",
      }))
    )
      return;
    setBusy("delete");
    try {
      await api.deleteConnector(id);
      toast.success("Connector deleted");
      router.push("/connectors/");
    } catch (e) {
      toast.error(e);
      setBusy(null);
    }
  };

  return (
    <div>
      <div className="mb-2">
        <Link
          href="/connectors/"
          className="text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200"
        >
          ← Connectors
        </Link>
      </div>
      <PageHeader
        title={
          isNew ? (
            spec ? (
              `New ${spec.name} connector`
            ) : (
              "Add connector"
            )
          ) : (
            <span className="flex items-center gap-2">
              <HealthDot status={c?.last_status ?? null} /> {c?.name}
            </span>
          )
        }
        subtitle={
          spec
            ? `${KIND_LABEL[spec.kind] ?? spec.kind} · ${spec.type}${c?.last_checked_at ? ` · checked ${fmtRelative(c.last_checked_at)}` : ""}`
            : "Choose a connector type."
        }
        actions={
          spec && (
            <>
              {isNew && (
                <Button variant="ghost" onClick={() => setSpec(null)}>
                  Change type
                </Button>
              )}
              {!isNew && (
                <>
                  <Button
                    variant="ghost"
                    onClick={remove}
                    loading={busy === "delete"}
                  >
                    <Icon name="trash" /> Delete
                  </Button>
                  <Button onClick={test} loading={busy === "test"}>
                    Test connection
                  </Button>
                </>
              )}
              <Button
                variant="primary"
                onClick={save}
                loading={busy === "save"}
                disabled={(!isNew && !name.trim()) || missing.length > 0}
                title={
                  missing.length ? `Missing: ${missing.join(", ")}` : undefined
                }
              >
                {isNew ? "Create" : "Save"}
              </Button>
            </>
          )
        }
      />

      {!spec && isNew && <TypePicker specs={specs} onPick={pick} />}
      {!spec && !isNew && c && (
        <ErrorBox>
          Unknown connector type <code>{c.type}</code> — the server no longer
          registers this plugin.
        </ErrorBox>
      )}

      {spec && (
        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_420px]">
          <div className="space-y-4">
            <Card>
              <CardHeader title="Settings" subtitle={spec.description} />
              <div className="space-y-4 p-4">
                <div className="grid gap-4 md:grid-cols-[1fr_auto] md:items-end">
                  <Field label="Name" required={!isNew}>
                    <Input
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      placeholder={
                        isNew
                          ? "Leave blank to name it from the settings"
                          : undefined
                      }
                    />
                  </Field>
                  <div className="pb-1.5">
                    <Toggle
                      checked={active}
                      onChange={setActive}
                      label="Active"
                    />
                  </div>
                </div>
                <ConnectorFields
                  fields={fields}
                  values={values}
                  onChange={setValues}
                  secretsSet={secretsSet}
                  cleared={cleared}
                  onToggleClear={(k) =>
                    setCleared((prev) => {
                      const n = new Set(prev);
                      if (n.has(k)) n.delete(k);
                      else n.add(k);
                      return n;
                    })
                  }
                />
                {missing.length > 0 && (
                  <p className="text-xs text-zinc-500">
                    Required: {missing.join(", ")}
                  </p>
                )}
              </div>
            </Card>
            {testResult && <TestPanel result={testResult} />}
            {!testResult && c?.last_status === "error" && c.last_error && (
              <ErrorBox>
                Last check failed {fmtTime(c.last_checked_at)}: {c.last_error}
              </ErrorBox>
            )}
          </div>
          <div className="space-y-4">
            {!isNew && isMCP && spec.kind !== "tools" && <ToolsPanel id={id} />}
            {!isNew && spec.kind === "tools" && (
              <ToolPermissionsPanel id={id} />
            )}
            {!isNew && spec.kind === "source" && <ItemsPanel id={id} />}
            {isNew && (
              <Card>
                <CardHeader title="About secrets" />
                <p className="px-4 py-3 text-sm text-zinc-600 dark:text-zinc-400">
                  Fields marked <em>encrypted</em> are sealed with AES-256-GCM
                  before they reach the database and are never sent back to the
                  browser. You can test the connection after creating it.
                </p>
              </Card>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

export default function Page() {
  return (
    <Suspense fallback={<Loading />}>
      <ConnectorEdit />
    </Suspense>
  );
}
