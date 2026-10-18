"use client";

import Link from "next/link";
import { useState } from "react";
import { AgentAvatar, NewTaskModal, ROLE_LABEL, useAgents } from "@/components/agents";
import { useMeta, useToast } from "@/components/providers";
import { Button, Card, EmptyState, ErrorBox, Icon, Loading, Modal, PageHeader, Pill, Toggle } from "@/components/ui";
import { api } from "@/lib/api";
import type { Agent, AgentTeam } from "@/lib/types";

export default function AgentsPage() {
  const agents = useAgents();
  const { meta } = useMeta();
  const toast = useToast();
  const [assignTo, setAssignTo] = useState<Agent | null>(null);
  const [teamsOpen, setTeamsOpen] = useState(false);
  const teams = meta?.agent_teams ?? [];

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
          <div className="flex gap-2">
            {teams.length > 0 && (
              <Button variant="secondary" onClick={() => setTeamsOpen(true)}>
                <Icon name="users" className="h-4 w-4" /> Add a team
              </Button>
            )}
            <Link href="/agents/edit/">
              <Button variant="primary">
                <Icon name="plus" className="h-4 w-4" /> New agent
              </Button>
            </Link>
          </div>
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
              <div className="flex gap-2">
                {teams.length > 0 && (
                  <Button variant="secondary" onClick={() => setTeamsOpen(true)}>
                    Add a team
                  </Button>
                )}
                <Link href="/agents/edit/">
                  <Button variant="primary">Create an agent</Button>
                </Link>
              </div>
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
      <TeamsModal
        open={teamsOpen}
        onClose={() => setTeamsOpen(false)}
        teams={teams}
        agents={agents.data ?? []}
        onApplied={() => void agents.reload()}
      />
      <NewTaskModal
        open={assignTo !== null}
        onClose={() => setAssignTo(null)}
        agents={agents.data?.filter((a) => a.is_active)}
        defaultAgentId={assignTo?.id}
      />
    </div>
  );
}

/** TeamsModal lists the team templates modules offer; applying one adds the roles the org is missing. */
function TeamsModal({
  open,
  onClose,
  teams,
  agents,
  onApplied,
}: {
  open: boolean;
  onClose: () => void;
  teams: AgentTeam[];
  agents: Agent[];
  onApplied: () => void;
}) {
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const have = new Set(agents.map((a) => a.slug));

  const apply = async (t: AgentTeam) => {
    setBusy(t.id);
    try {
      const res = await api.applyTeam(t.id);
      onApplied();
      if (res.created.length === 0) toast.success(`Nothing to add: ${t.name} is already on the team`);
      else toast.success(`Added ${res.created.map((a) => a.name).join(", ")}`);
      onClose();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(null);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title="Add a team">
      <div className="space-y-4">
        <p className="text-sm text-zinc-600 dark:text-zinc-400">
          A template adds the roles your org does not have yet (matched by slug) and leaves existing agents alone. Every
          agent it creates is editable afterwards.
        </p>
        {teams.map((t) => {
          const missing = t.agents.filter((a) => !have.has(a.slug));
          return (
            <div key={t.id} className="rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
              <div className="flex items-start gap-3">
                <div className="min-w-0 flex-1">
                  <div className="font-medium text-zinc-900 dark:text-zinc-100">{t.name}</div>
                  <p className="mt-0.5 text-sm text-zinc-600 dark:text-zinc-400">{t.description}</p>
                </div>
                <Button
                  size="sm"
                  variant="primary"
                  loading={busy === t.id}
                  disabled={missing.length === 0}
                  onClick={() => void apply(t)}
                >
                  {missing.length === 0 ? "Already added" : `Add ${missing.length}`}
                </Button>
              </div>
              <ul className="mt-3 space-y-1.5">
                {t.agents.map((a) => (
                  <li key={a.slug} className="flex items-start gap-2 text-sm">
                    <span className="w-6 shrink-0 text-center">{a.avatar}</span>
                    <span className="min-w-0 flex-1">
                      <span className={have.has(a.slug) ? "text-zinc-400 line-through" : "text-zinc-900 dark:text-zinc-100"}>
                        {a.name}
                      </span>
                      <span className="ml-2 font-mono text-xs text-zinc-500">@{a.slug}</span>
                      <span className="ml-2 text-xs text-zinc-500">{ROLE_LABEL[a.role] ?? a.role}</span>
                      <span className="block text-xs text-zinc-500">{a.description}</span>
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          );
        })}
      </div>
    </Modal>
  );
}
