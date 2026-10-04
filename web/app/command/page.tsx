"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useMemo, useRef, useState } from "react";
import { AgentAvatar, AgentSelect, PRIORITIES, TaskStatusBadge, useAgents } from "@/components/agents";
import { useServerEvent, useToast } from "@/components/providers";
import { Button, Card, CardHeader, EmptyState, ErrorBox, Icon, Loading, PageHeader, Select, cx } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative } from "@/lib/format";
import { useAsync } from "@/lib/hooks";
import type { TaskPriority } from "@/lib/types";

const EXAMPLES = [
  "每天早上 8 點整理 AI 新聞並寫成 Threads 草稿",
  "研究一下 MCP 最近一週的新聞，給我重點摘要",
  "幫我規劃下週 5 篇 Instagram 貼文的主題，分派給文案寫草稿",
  "看看待審核的草稿，挑出語氣太硬的幫我改寫",
];

// Short title from a free-form instruction: first line, trimmed to ~60 chars.
function titleFrom(prompt: string): string {
  const first = prompt.trim().split("\n")[0].trim();
  const chars = Array.from(first);
  return chars.length > 60 ? chars.slice(0, 60).join("") + "…" : first;
}

export default function CommandPage() {
  const router = useRouter();
  const toast = useToast();
  const agents = useAgents();
  const [prompt, setPrompt] = useState("");
  const [agentId, setAgentId] = useState<string | null>(null);
  const [priority, setPriority] = useState<TaskPriority>("normal");
  const [busy, setBusy] = useState(false);
  const ref = useRef<HTMLTextAreaElement>(null);

  const active = useMemo(() => (agents.data ?? []).filter((a) => a.is_active), [agents.data]);

  // Default to the first active manager, else the first active agent.
  useEffect(() => {
    if (agentId || active.length === 0) return;
    setAgentId((active.find((a) => a.role === "manager") ?? active[0]).id);
  }, [active, agentId]);

  const recent = useAsync(() => api.listTasks({ all: true }), []);
  useServerEvent(["task.updated", "task.deleted"], () => void recent.reload());
  const conversations = useMemo(
    () =>
      (recent.data?.items ?? [])
        .filter((t) => t.created_by === "human" && !t.parent_id)
        .sort((a, b) => b.updated_at.localeCompare(a.updated_at))
        .slice(0, 12),
    [recent.data],
  );

  const submit = async () => {
    const text = prompt.trim();
    if (!text) return;
    if (!agentId) {
      toast.error("Create an agent first (Agents → New agent).");
      return;
    }
    setBusy(true);
    try {
      const t = await api.createTask({
        title: titleFrom(text),
        description: text,
        assignee_agent_id: agentId,
        priority,
        status: "TODO",
      });
      router.push(`/tasks/view/?id=${t.id}`);
    } catch (e) {
      toast.error(e);
      setBusy(false);
    }
  };

  const picked = active.find((a) => a.id === agentId);

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader
        title="Command"
        subtitle="Tell your agents what to do in plain language. They plan, delegate and report back; anything they write waits in Review for you."
      />

      <Card className="overflow-hidden">
        <div className="flex items-start gap-3 p-4">
          <AgentAvatar agent={picked} size="lg" />
          <div className="min-w-0 flex-1">
            <textarea
              ref={ref}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  void submit();
                }
              }}
              rows={4}
              placeholder="Tell your agents what to do…"
              className="w-full resize-y border-0 bg-transparent p-0 text-[15px] leading-relaxed text-zinc-900 placeholder:text-zinc-400 focus:outline-none focus:ring-0 dark:text-zinc-100"
            />
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2 border-t border-zinc-200 bg-zinc-50 px-4 py-2.5 dark:border-zinc-800 dark:bg-zinc-950/40">
          <span className="text-xs text-zinc-500">To</span>
          <AgentSelect
            agents={active}
            value={agentId}
            onChange={setAgentId}
            allowNone={false}
            className="h-8 w-auto min-w-[10rem] py-1 text-xs"
          />
          <Select
            value={priority}
            onChange={(e) => setPriority(e.target.value as TaskPriority)}
            className="h-8 w-auto py-1 text-xs"
            aria-label="Priority"
          >
            {PRIORITIES.map((p) => (
              <option key={p} value={p}>
                {p} priority
              </option>
            ))}
          </Select>
          <span className="ml-auto hidden text-[11px] text-zinc-400 sm:inline">⌘/Ctrl + Enter</span>
          <Button variant="primary" loading={busy} disabled={!prompt.trim() || !agentId} onClick={submit}>
            <Icon name="send" className="h-4 w-4" /> Send
          </Button>
        </div>
      </Card>

      {agents.error && (
        <div className="mt-3">
          <ErrorBox onRetry={agents.reload}>{agents.error}</ErrorBox>
        </div>
      )}
      {!agents.loading && active.length === 0 && !agents.error && (
        <div className="mt-3 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-900/60 dark:bg-amber-950/40 dark:text-amber-200">
          No active agents yet. <Link href="/agents/edit/" className="font-medium underline">Create one</Link> to start
          delegating.
        </div>
      )}

      <div className="mt-4 flex flex-wrap gap-2">
        {EXAMPLES.map((ex) => (
          <button
            key={ex}
            onClick={() => {
              setPrompt(ex);
              ref.current?.focus();
            }}
            className="rounded-full border border-zinc-200 bg-white px-3 py-1.5 text-xs text-zinc-700 transition-colors hover:border-indigo-300 hover:bg-indigo-50 hover:text-indigo-800 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:border-indigo-500/50 dark:hover:bg-indigo-500/10 dark:hover:text-indigo-200"
          >
            {ex}
          </button>
        ))}
      </div>

      <Card className="mt-6">
        <CardHeader
          title="Recent conversations"
          actions={
            <Link href="/board/" className="text-xs font-medium text-indigo-600 hover:underline dark:text-indigo-400">
              Open board →
            </Link>
          }
        />
        {recent.loading && !recent.data ? (
          <Loading />
        ) : recent.error ? (
          <div className="p-4">
            <ErrorBox onRetry={recent.reload}>{recent.error}</ErrorBox>
          </div>
        ) : conversations.length === 0 ? (
          <EmptyState title="No conversations yet" hint="Send your first instruction above." />
        ) : (
          <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {conversations.map((t) => (
              <li key={t.id}>
                <Link
                  href={`/tasks/view/?id=${t.id}`}
                  className={cx(
                    "flex items-center gap-3 px-4 py-2.5 hover:bg-zinc-50 dark:hover:bg-zinc-800/40",
                    t.status === "WAITING_HUMAN" && "bg-amber-50/60 dark:bg-amber-950/20",
                  )}
                >
                  <AgentAvatar agent={t.assignee} size="sm" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm text-zinc-900 dark:text-zinc-100">
                      <span className="mr-1.5 text-zinc-400">#{t.number}</span>
                      {t.title}
                    </p>
                    <p className="text-xs text-zinc-500">
                      {t.assignee?.name ?? "Unassigned"} · {fmtRelative(t.updated_at)}
                    </p>
                  </div>
                  <TaskStatusBadge status={t.status} running={t.running} />
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}
