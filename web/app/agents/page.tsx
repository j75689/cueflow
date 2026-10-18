"use client";

import Link from "next/link";
import { useState } from "react";
import { AgentAvatar, NewTaskModal, ROLE_LABEL, useAgents } from "@/components/agents";
import { useMeta, useToast } from "@/components/providers";
import { useOrg } from "@/components/org";
import {
  Button,
  Card,
  EmptyState,
  ErrorBox,
  Field,
  Icon,
  Loading,
  Modal,
  PageHeader,
  Pill,
  Textarea,
  Toggle,
  cx,
} from "@/components/ui";
import { api } from "@/lib/api";
import type { Agent, TeamTemplate } from "@/lib/types";

export default function AgentsPage() {
  const agents = useAgents();
  const { meta } = useMeta();
  const toast = useToast();
  const [assignTo, setAssignTo] = useState<Agent | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const { org } = useOrg();

  const capName = (id: string) => meta?.agent_capabilities?.find((c) => c.id === id)?.name ?? id;

  const toggle = async (a: Agent, v: boolean) => {
    try {
      const updated = await api.updateAgent(a.id, { is_active: v });
      agents.setData((agents.data ?? []).map((x) => (x.id === a.id ? updated : x)));
    } catch (e) {
      toast.error(e);
    }
  };

  // Export downloads the org's agents as a team template file (see templates/).
  const exportTeam = async () => {
    try {
      const team = await api.exportTeam();
      const blob = new Blob([JSON.stringify(team, null, 2) + "\n"], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `${(org?.slug ?? "cueflow").replace(/[^a-z0-9-]+/gi, "-")}-team.json`;
      a.click();
      URL.revokeObjectURL(url);
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
            <Button variant="ghost" onClick={() => void exportTeam()} disabled={(agents.data ?? []).length === 0}>
              Export team
            </Button>
            <Button variant="secondary" onClick={() => setImportOpen(true)}>
              <Icon name="users" className="h-4 w-4" /> Import team
            </Button>
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
                <Button variant="secondary" onClick={() => setImportOpen(true)}>
                  Import a team
                </Button>
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
                      className="block truncate text-base font-semibold text-fg hover:underline"
                    >
                      {a.name}
                    </Link>
                    <p className="text-xs text-fg-3">
                      <span className="font-mono">@{a.slug}</span> · {ROLE_LABEL[a.role] ?? a.role}
                    </p>
                  </div>
                  <Toggle checked={a.is_active} onChange={(v) => void toggle(a, v)} />
                </div>
                <p className="mt-3 line-clamp-3 min-h-[3.75rem] text-sm text-fg-2">
                  {a.description || <span className="italic text-fg-4">No description</span>}
                </p>
                <div className="mt-3 flex flex-wrap gap-1">
                  {a.capabilities.map((c) => (
                    <Pill key={c} className="bg-accent-soft text-accent">
                      {capName(c)}
                    </Pill>
                  ))}
                </div>
                <div className="mt-auto flex items-center justify-between gap-2 pt-4 text-xs text-fg-3">
                  <span>
                    {a.stats?.open_tasks ?? 0} open
                    {(a.stats?.running ?? 0) > 0 && (
                      <span className="ml-2 inline-flex items-center gap-1 text-accent">
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
      <ImportTeamModal
        open={importOpen}
        onClose={() => setImportOpen(false)}
        agents={agents.data ?? []}
        onImported={() => void agents.reload()}
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

/** ImportTeamModal takes a team template file (pasted or picked) and adds the roles the org is missing. */
function ImportTeamModal({
  open,
  onClose,
  agents,
  onImported,
}: {
  open: boolean;
  onClose: () => void;
  agents: Agent[];
  onImported: () => void;
}) {
  const toast = useToast();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const have = new Set(agents.map((a) => a.slug));

  let team: TeamTemplate | null = null;
  let problem: string | null = null;
  if (text.trim()) {
    try {
      const parsed = JSON.parse(text) as TeamTemplate;
      if (!parsed || typeof parsed !== "object" || !Array.isArray(parsed.agents) || !parsed.name) {
        problem = "Not a team template: it needs a name and an agents list.";
      } else team = parsed;
    } catch (e) {
      problem = e instanceof Error ? e.message : "Invalid JSON";
    }
  }
  const missing = team ? team.agents.filter((a) => a.slug && !have.has(a.slug.toLowerCase())) : [];

  const pick = (file: File | undefined) => {
    if (!file) return;
    file
      .text()
      .then(setText)
      .catch((e) => toast.error(e));
  };

  const close = () => {
    setText("");
    onClose();
  };

  const run = async () => {
    if (!team) return;
    setBusy(true);
    try {
      const res = await api.importTeam(team);
      onImported();
      if (res.created.length === 0) toast.success("Nothing to add: every role is already on the team");
      else toast.success(`Added ${res.created.map((a) => a.name).join(", ")}`);
      close();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open={open} onClose={close} title="Import a team">
      <div className="space-y-4">
        <p className="text-sm text-fg-2">
          A team template is a JSON file (name, roles, capabilities, instructions), like a dashboard you import. Roles
          whose slug this org already has are skipped; everything it creates is editable afterwards. An example lives
          in the repository under <span className="font-mono text-xs">templates/</span>.
        </p>
        <Field label="Template file">
          <input
            type="file"
            accept="application/json,.json"
            onChange={(e) => pick(e.target.files?.[0])}
            className={cx(
              "block w-full text-sm text-fg-2",
              "file:mr-3 file:rounded-md file:border-0 file:bg-subtle file:px-3 file:py-1.5 file:text-sm file:font-medium",
              "file:text-fg file:bg-subtle hover:file:bg-muted",
            )}
          />
        </Field>
        <Field label="Or paste it">
          <Textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={8}
            spellCheck={false}
            className="font-mono text-xs"
          />
        </Field>
        {problem && <p className="text-sm text-red-600 dark:text-red-400">{problem}</p>}
        {team && (
          <div className="rounded-lg border border-line p-3">
            <div className="font-medium text-fg">{team.name}</div>
            {team.description && <p className="mt-0.5 text-sm text-fg-2">{team.description}</p>}
            <ul className="mt-3 space-y-1.5">
              {team.agents.map((a, i) => {
                const present = !!a.slug && have.has(a.slug.toLowerCase());
                return (
                  <li key={`${a.slug}-${i}`} className="flex items-start gap-2 text-sm">
                    <span className="w-6 shrink-0 text-center">{a.avatar ?? "🤖"}</span>
                    <span className="min-w-0 flex-1">
                      <span className={present ? "text-fg-4 line-through" : "text-fg"}>
                        {a.name}
                      </span>
                      <span className="ml-2 font-mono text-xs text-fg-3">@{a.slug}</span>
                      {a.role && <span className="ml-2 text-xs text-fg-3">{ROLE_LABEL[a.role] ?? a.role}</span>}
                      {present && <span className="ml-2 text-xs text-fg-3">already here</span>}
                      {a.description && <span className="block text-xs text-fg-3">{a.description}</span>}
                    </span>
                  </li>
                );
              })}
            </ul>
          </div>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!team || missing.length === 0} onClick={() => void run()}>
            {team && missing.length === 0 ? "Nothing to add" : `Import${missing.length ? ` ${missing.length}` : ""}`}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
