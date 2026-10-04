"use client";

import Link from "next/link";
import { useState } from "react";
import { AgentAvatar, NewTaskModal, ROLE_LABEL, useAgents } from "@/components/agents";
import { useMeta, useToast } from "@/components/providers";
import { Button, Card, EmptyState, ErrorBox, Icon, Loading, PageHeader, Pill, Toggle } from "@/components/ui";
import { api } from "@/lib/api";
import type { Agent } from "@/lib/types";

export default function AgentsPage() {
  const agents = useAgents();
  const { meta } = useMeta();
  const toast = useToast();
  const [assignTo, setAssignTo] = useState<Agent | null>(null);

  const capName = (id: string) => meta?.agent_capabilities?.find((c) => c.id === id)?.name ?? id;

  const toggle = async (a: Agent, v: boolean) => {
    try {
      const updated = await api.updateAgent(a.id, { is_active: v });
      agents.setData((agents.data ?? []).map((x) => (x.id === a.id ? updated : x)));
    } catch (e) {
      toast.error(e);
    }
  };

  return (
    <div>
      <PageHeader
        title="Agents"
        subtitle="Your team of AI roles. Each has a persona, a set of capabilities and its own memory. None of them can publish: drafts always wait for your review."
        actions={
          <Link href="/agents/edit/">
            <Button variant="primary">
              <Icon name="plus" className="h-4 w-4" /> New agent
            </Button>
          </Link>
        }
      />
      {agents.error && <ErrorBox onRetry={agents.reload}>{agents.error}</ErrorBox>}
      {agents.loading && !agents.data ? (
        <Loading />
      ) : (agents.data ?? []).length === 0 ? (
        <Card>
          <EmptyState
            title="No agents yet"
            hint="Create a manager to receive your instructions, plus specialists (researcher, writer…) it can delegate to."
            action={
              <Link href="/agents/edit/">
                <Button variant="primary">Create an agent</Button>
              </Link>
            }
          />
        </Card>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {(agents.data ?? []).map((a) => (
            <Card key={a.id} className={a.is_active ? "" : "opacity-70"}>
              <div className="flex h-full flex-col p-4">
                <div className="flex items-start gap-3">
                  <AgentAvatar agent={a} size="lg" />
                  <div className="min-w-0 flex-1">
                    <Link
                      href={`/agents/edit/?id=${a.id}`}
                      className="block truncate text-base font-semibold text-zinc-900 hover:underline dark:text-zinc-100"
                    >
                      {a.name}
                    </Link>
                    <p className="text-xs text-zinc-500">
                      <span className="font-mono">@{a.slug}</span> · {ROLE_LABEL[a.role] ?? a.role}
                    </p>
                  </div>
                  <Toggle checked={a.is_active} onChange={(v) => void toggle(a, v)} />
                </div>
                <p className="mt-3 line-clamp-3 min-h-[3.75rem] text-sm text-zinc-600 dark:text-zinc-400">
                  {a.description || <span className="italic text-zinc-400">No description</span>}
                </p>
                <div className="mt-3 flex flex-wrap gap-1">
                  {a.capabilities.map((c) => (
                    <Pill key={c} className="bg-indigo-50 text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300">
                      {capName(c)}
                    </Pill>
                  ))}
                </div>
                <div className="mt-auto flex items-center justify-between gap-2 pt-4 text-xs text-zinc-500">
                  <span>
                    {a.stats?.open_tasks ?? 0} open
                    {(a.stats?.running ?? 0) > 0 && (
                      <span className="ml-2 inline-flex items-center gap-1 text-indigo-600 dark:text-indigo-400">
                        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-indigo-500" />
                        {a.stats!.running} running
                      </span>
                    )}
                  </span>
                  <div className="flex gap-1.5">
                    <Link href={`/board/`}>
                      <Button size="sm" variant="ghost">
                        Board
                      </Button>
                    </Link>
                    <Button size="sm" variant="secondary" disabled={!a.is_active} onClick={() => setAssignTo(a)}>
                      Assign task
                    </Button>
                  </div>
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}
      <NewTaskModal
        open={assignTo !== null}
        onClose={() => setAssignTo(null)}
        agents={agents.data?.filter((a) => a.is_active)}
        defaultAgentId={assignTo?.id}
      />
    </div>
  );
}
