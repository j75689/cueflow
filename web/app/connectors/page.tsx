"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { HealthDot, KIND_LABEL, KIND_ORDER } from "@/components/badges";
import { useMeta, useServerEvent, useToast } from "@/components/providers";
import { Button, Card, CardHeader, EmptyState, ErrorBox, Icon, Loading, PageHeader, Pill, Toggle } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative, fmtTime } from "@/lib/format";
import { useAsync, useDebouncedCallback } from "@/lib/hooks";
import type { Connector } from "@/lib/types";

const KIND_HINT: Record<string, string> = {
  llm: "Models that write the drafts (OpenAI, Claude, DeepSeek, Ollama…).",
  source: "Where material comes from: RSS feeds, IMAP mailboxes.",
  search: "Optional live web search the agent can call (SearXNG, Tavily).",
  publisher: "Where approved posts go. Only the publish worker can call these.",
  notifier: "Email reviewers when drafts are waiting.",
  runtime: "External agents (Claude Code, Codex, Gemini CLI…) that can work an agent's tasks over ACP, using CueFlow's tools.",
};

export default function ConnectorsPage() {
  const toast = useToast();
  const { meta } = useMeta();
  const connectors = useAsync(() => api.listConnectors(), []);
  const [testing, setTesting] = useState<string | null>(null);
  const refresh = useDebouncedCallback(() => void connectors.reload(), 300);
  useServerEvent("connector.updated", refresh);

  const specName = useMemo(
    () => Object.fromEntries((meta?.connector_types ?? []).map((s) => [s.type, s.name])),
    [meta],
  );

  const grouped = useMemo(() => {
    const g: Record<string, Connector[]> = {};
    for (const c of connectors.data ?? []) (g[c.kind] ??= []).push(c);
    return g;
  }, [connectors.data]);

  const kinds = useMemo(() => {
    const fromSpecs = new Set((meta?.connector_types ?? []).map((s) => s.kind));
    const all = new Set([...KIND_ORDER.filter((k) => fromSpecs.has(k as never) || grouped[k]), ...Object.keys(grouped)]);
    return [...all];
  }, [meta, grouped]);

  const test = async (c: Connector) => {
    setTesting(c.id);
    try {
      const r = await api.testConnector(c.id);
      if (r.ok) toast.success(`${c.name}: ${r.message || "OK"}`);
      else toast.error(`${c.name}: ${r.message || "failed"}`);
      void connectors.reload();
    } catch (e) {
      toast.error(e);
    } finally {
      setTesting(null);
    }
  };

  const toggle = async (c: Connector, on: boolean) => {
    try {
      const u = await api.updateConnector(c.id, { is_active: on });
      connectors.setData((connectors.data ?? []).map((x) => (x.id === c.id ? u : x)));
    } catch (e) {
      toast.error(e);
    }
  };

  return (
    <div>
      <PageHeader
        title="Connectors"
        subtitle="Plugins for models, sources, search, publishing and notifications. Secrets are encrypted at rest."
        actions={
          <Link href="/connectors/edit/">
            <Button variant="primary">
              <Icon name="plus" /> Add connector
            </Button>
          </Link>
        }
      />
      {connectors.error && <ErrorBox onRetry={connectors.reload}>{connectors.error}</ErrorBox>}
      {connectors.loading && !connectors.data && <Loading />}
      {connectors.data && (
        <div className="space-y-4">
          {kinds.map((kind) => (
            <Card key={kind}>
              <CardHeader
                title={KIND_LABEL[kind] ?? kind}
                subtitle={KIND_HINT[kind]}
                actions={
                  <Link href={`/connectors/edit/?kind=${kind}`}>
                    <Button size="sm" variant="ghost">
                      <Icon name="plus" className="h-3.5 w-3.5" /> Add
                    </Button>
                  </Link>
                }
              />
              {!grouped[kind]?.length ? (
                <EmptyState title="None configured" />
              ) : (
                <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
                  {grouped[kind].map((c) => (
                    <li key={c.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                      <HealthDot status={c.last_status} />
                      <div className="min-w-0 flex-1">
                        <Link
                          href={`/connectors/edit/?id=${c.id}`}
                          className="text-sm font-medium text-zinc-900 hover:text-indigo-600 dark:text-zinc-100 dark:hover:text-indigo-400"
                        >
                          {c.name}
                        </Link>
                        <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-zinc-500">
                          <Pill>{specName[c.type] ?? c.type}</Pill>
                          <span title={fmtTime(c.last_checked_at)}>
                            {c.last_checked_at ? `checked ${fmtRelative(c.last_checked_at)}` : "never checked"}
                          </span>
                          {c.last_status === "error" && c.last_error && (
                            <span className="line-clamp-1 max-w-md text-red-600 dark:text-red-400">{c.last_error}</span>
                          )}
                        </div>
                      </div>
                      <div className="flex items-center gap-2">
                        <Toggle checked={c.is_active} onChange={(on) => toggle(c, on)} />
                        <Button size="sm" onClick={() => test(c)} loading={testing === c.id}>
                          Test
                        </Button>
                        <Link href={`/connectors/edit/?id=${c.id}`}>
                          <Button size="sm" variant="ghost">
                            Edit
                          </Button>
                        </Link>
                      </div>
                    </li>
                  ))}
                </ul>
              )}
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
