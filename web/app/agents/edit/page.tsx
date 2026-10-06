"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useMemo, useState } from "react";
import { AgentAvatar, NewTaskModal, ROLE_LABEL } from "@/components/agents";
import { useDialog } from "@/components/dialog";
import { useMeta, useToast } from "@/components/providers";
import {
  Button,
  Card,
  CardHeader,
  Checkbox,
  ErrorBox,
  Field,
  Icon,
  Input,
  Loading,
  PageHeader,
  Select,
  Textarea,
  Toggle,
  cx,
} from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative } from "@/lib/format";
import { useAsync } from "@/lib/hooks";
import type { AgentInput, AgentRole, Connector } from "@/lib/types";

const AVATARS = [
  "🧭",
  "🔎",
  "✍️",
  "🧐",
  "📅",
  "📈",
  "🎨",
  "🤖",
  "🦉",
  "🚀",
  "💡",
  "🗞️",
];
const LANGUAGES = ["en", "zh-TW", "zh-CN", "ja", "ko"];
const FALLBACK_ROLES: AgentRole[] = [
  "manager",
  "researcher",
  "writer",
  "editor",
  "custom",
];

const blank = (): AgentInput => ({
  name: "",
  slug: "",
  avatar: "🤖",
  role: "custom",
  description: "",
  instructions: "",
  capabilities: ["research", "memory"],
  llm_connector_id: null,
  runtime_connector_id: null,
  search_connector_id: null,
  source_connector_ids: [],
  tool_connector_ids: [],
  language: "en",
  platforms: ["threads"],
  max_turns: 12,
  is_active: true,
});

function slugify(name: string): string {
  return name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 32);
}

function AgentEditor() {
  const params = useSearchParams();
  const id = params.get("id");
  const router = useRouter();
  const toast = useToast();
  const dialog = useDialog();
  const { meta } = useMeta();
  const [form, setForm] = useState<AgentInput>(blank);
  const [slugTouched, setSlugTouched] = useState(false);
  const [saving, setSaving] = useState(false);
  const [assignOpen, setAssignOpen] = useState(false);

  const existing = useAsync(
    () => (id ? api.getAgent(id) : Promise.resolve(null)),
    [id],
  );
  const connectors = useAsync(() => api.listConnectors(), []);

  useEffect(() => {
    const a = existing.data;
    if (!a) return;
    setForm({
      name: a.name,
      slug: a.slug,
      avatar: a.avatar,
      role: a.role,
      description: a.description,
      instructions: a.instructions,
      capabilities: a.capabilities ?? [],
      llm_connector_id: a.llm_connector_id,
      runtime_connector_id: a.runtime_connector_id ?? null,
      search_connector_id: a.search_connector_id,
      source_connector_ids: a.source_connector_ids ?? [],
      tool_connector_ids: a.tool_connector_ids ?? [],
      language: a.language,
      platforms: a.platforms ?? [],
      max_turns: a.max_turns,
      is_active: a.is_active,
    });
    setSlugTouched(true);
  }, [existing.data]);

  const set = <K extends keyof AgentInput>(k: K, v: AgentInput[K]) =>
    setForm((f) => ({ ...f, [k]: v }));
  const byKind = (kind: string): Connector[] =>
    (connectors.data ?? []).filter((c) => c.kind === kind);
  const toggleIn = (list: string[], v: string, on: boolean) =>
    on ? [...new Set([...list, v])] : list.filter((x) => x !== v);

  const roles = meta?.agent_roles ?? FALLBACK_ROLES;
  const caps = meta?.agent_capabilities ?? [];
  const valid = form.name.trim() !== "" && form.max_turns > 0;

  const save = async () => {
    if (!valid) return;
    setSaving(true);
    const body: Partial<AgentInput> = {
      ...form,
      name: form.name.trim(),
      slug: form.slug.trim() || undefined,
    };
    try {
      if (id) {
        await api.updateAgent(id, body);
        toast.success("Agent saved");
        void existing.reload();
      } else {
        const a = await api.createAgent(body);
        toast.success(`${a.name} joined the team`);
        router.replace(`/agents/edit/?id=${a.id}`);
      }
    } catch (e) {
      toast.error(e);
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    if (
      !id ||
      !(await dialog.confirm({
        title: `Delete ${form.name}?`,
        message: "Its tasks become unassigned and its memories are deleted.",
        confirmLabel: "Delete agent",
        tone: "danger",
      }))
    )
      return;
    try {
      await api.deleteAgent(id);
      toast.success("Agent deleted");
      router.push("/agents/");
    } catch (e) {
      toast.error(e);
    }
  };

  if (id && existing.loading && !existing.data) return <Loading />;
  if (id && existing.error)
    return <ErrorBox onRetry={existing.reload}>{existing.error}</ErrorBox>;

  return (
    <div>
      <PageHeader
        title={
          <span className="flex items-center gap-3">
            <AgentAvatar
              agent={{ avatar: form.avatar, name: form.name || "?" }}
              size="md"
            />
            {id ? form.name || "Agent" : "New agent"}
          </span>
        }
        subtitle={
          <Link href="/agents/" className="hover:underline">
            ← All agents
          </Link>
        }
        actions={
          <>
            {id && (
              <Button
                variant="secondary"
                onClick={() => setAssignOpen(true)}
                disabled={!form.is_active}
              >
                Assign a task
              </Button>
            )}
            <Button
              variant="primary"
              loading={saving}
              disabled={!valid}
              onClick={save}
            >
              {id ? "Save" : "Create agent"}
            </Button>
          </>
        }
      />

      <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_340px]">
        <div className="space-y-5">
          <Card>
            <CardHeader title="Identity" />
            <div className="grid gap-4 p-4 sm:grid-cols-2">
              <Field label="Name" required>
                <Input
                  value={form.name}
                  onChange={(e) => {
                    const name = e.target.value;
                    setForm((f) => ({
                      ...f,
                      name,
                      slug: slugTouched ? f.slug : slugify(name),
                    }));
                  }}
                  placeholder="e.g. Researcher"
                />
              </Field>
              <Field
                label="Slug"
                help="Used for @mentions; lowercase letters, digits and dashes."
              >
                <Input
                  value={form.slug}
                  onChange={(e) => {
                    setSlugTouched(true);
                    set(
                      "slug",
                      e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ""),
                    );
                  }}
                  placeholder="researcher"
                  className="font-mono"
                />
              </Field>
              <Field label="Avatar">
                <div className="flex flex-wrap items-center gap-1.5">
                  <Input
                    value={form.avatar}
                    onChange={(e) =>
                      set(
                        "avatar",
                        Array.from(e.target.value).slice(0, 2).join(""),
                      )
                    }
                    className="w-16 text-center text-lg"
                  />
                  {AVATARS.map((em) => (
                    <button
                      key={em}
                      type="button"
                      onClick={() => set("avatar", em)}
                      className={cx(
                        "flex h-8 w-8 items-center justify-center rounded-md text-lg hover:bg-zinc-100 dark:hover:bg-zinc-800",
                        form.avatar === em &&
                          "bg-indigo-50 ring-1 ring-indigo-300 dark:bg-indigo-500/10 dark:ring-indigo-500/40",
                      )}
                    >
                      {em}
                    </button>
                  ))}
                </div>
              </Field>
              <Field label="Role">
                <Select
                  value={form.role}
                  onChange={(e) => set("role", e.target.value as AgentRole)}
                >
                  {roles.map((r) => (
                    <option key={r} value={r}>
                      {ROLE_LABEL[r] ?? r}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field
                label="Description"
                className="sm:col-span-2"
                help="One or two lines. Other agents read this to decide whether to delegate to this one."
              >
                <Input
                  value={form.description}
                  onChange={(e) => set("description", e.target.value)}
                  placeholder="Finds and verifies facts with sources"
                />
              </Field>
              <Field
                label="Instructions"
                className="sm:col-span-2"
                help="Persona and standing orders: voice, audience, what to do and avoid. Added to CueFlow's built-in agent rules."
              >
                <Textarea
                  rows={10}
                  value={form.instructions}
                  onChange={(e) => set("instructions", e.target.value)}
                  placeholder={
                    "You are the team's researcher…\n- Link a source for every conclusion\n- Say so when something is unconfirmed"
                  }
                  className="font-mono text-[13px]"
                />
              </Field>
            </div>
          </Card>

          <Card>
            <CardHeader
              title="Capabilities"
              subtitle="Tools this agent may use. Every agent can comment, ask you questions and complete tasks. No capability can publish."
            />
            <div className="grid gap-3 p-4 sm:grid-cols-2">
              {caps.length === 0 && (
                <p className="text-sm text-zinc-500">
                  Capabilities load from the server…
                </p>
              )}
              {caps.map((c) => {
                const on = form.capabilities.includes(c.id);
                return (
                  <label
                    key={c.id}
                    className={cx(
                      "flex cursor-pointer gap-3 rounded-md border p-3 transition-colors",
                      on
                        ? "border-indigo-300 bg-indigo-50/50 dark:border-indigo-500/40 dark:bg-indigo-500/5"
                        : "border-zinc-200 hover:bg-zinc-50 dark:border-zinc-800 dark:hover:bg-zinc-800/40",
                    )}
                  >
                    <input
                      type="checkbox"
                      checked={on}
                      onChange={(e) =>
                        set(
                          "capabilities",
                          toggleIn(form.capabilities, c.id, e.target.checked),
                        )
                      }
                      className="mt-0.5 h-4 w-4 accent-indigo-600"
                    />
                    <span className="min-w-0">
                      <span className="block text-sm font-medium text-zinc-900 dark:text-zinc-100">
                        {c.name}
                      </span>
                      <span className="block text-xs text-zinc-500">
                        {c.description}
                      </span>
                      <span className="mt-1 flex flex-wrap gap-1">
                        {c.tools.map((tl) => (
                          <code
                            key={tl}
                            className="rounded bg-zinc-100 px-1 py-0.5 text-[10px] text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400"
                          >
                            {tl}
                          </code>
                        ))}
                      </span>
                    </span>
                  </label>
                );
              })}
            </div>
          </Card>
        </div>

        <div className="space-y-5">
          <Card>
            <CardHeader title="Settings" />
            <div className="space-y-4 p-4">
              <Toggle
                checked={form.is_active}
                onChange={(v) => set("is_active", v)}
                label="Active (picks up assigned tasks)"
              />
              <Field
                label="Runtime"
                help={
                  form.runtime_connector_id
                    ? "Tasks run in this external agent over ACP. It uses CueFlow's tools for the capabilities below; its drafts still go to Review."
                    : "Built-in: CueFlow runs the agent loop with the LLM below."
                }
              >
                <Select
                  value={form.runtime_connector_id ?? ""}
                  onChange={(e) =>
                    set("runtime_connector_id", e.target.value || null)
                  }
                >
                  <option value="">Built-in (LLM connector)</option>
                  {byKind("runtime").map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                      {!c.is_active ? " (inactive)" : ""}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field
                label="LLM"
                help={
                  form.runtime_connector_id
                    ? "Not used while an external runtime is selected."
                    : undefined
                }
              >
                <Select
                  disabled={!!form.runtime_connector_id}
                  value={form.llm_connector_id ?? ""}
                  onChange={(e) =>
                    set("llm_connector_id", e.target.value || null)
                  }
                >
                  <option value="">Default (first active)</option>
                  {byKind("llm").map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                      {!c.is_active ? " (inactive)" : ""}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field
                label="Search engine"
                help="Used by web_search (Research capability)."
              >
                <Select
                  value={form.search_connector_id ?? ""}
                  onChange={(e) =>
                    set("search_connector_id", e.target.value || null)
                  }
                >
                  <option value="">Default (first active)</option>
                  {byKind("search").map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field
                label="Sources"
                help="Feeds and mailboxes this agent may read and fetch."
              >
                <div className="flex flex-col items-start gap-1.5">
                  {byKind("source").length === 0 && (
                    <p className="text-xs text-zinc-500">
                      No source connectors yet.
                    </p>
                  )}
                  {byKind("source").map((c) => (
                    <Checkbox
                      key={c.id}
                      checked={form.source_connector_ids.includes(c.id)}
                      onChange={(v) =>
                        set(
                          "source_connector_ids",
                          toggleIn(form.source_connector_ids, c.id, v),
                        )
                      }
                      label={c.name}
                    />
                  ))}
                </div>
              </Field>
              <Field
                label="MCP tool servers"
                help="External tools this agent may use. Set what each tool may do on the server's connector page (allow, ask you first, or deny)."
              >
                <div className="flex flex-col items-start gap-1.5">
                  {byKind("tools").length === 0 && (
                    <p className="text-xs text-zinc-500">
                      No MCP servers yet. Add one under Connectors.
                    </p>
                  )}
                  {byKind("tools").map((c) => (
                    <Checkbox
                      key={c.id}
                      checked={form.tool_connector_ids.includes(c.id)}
                      onChange={(v) =>
                        set(
                          "tool_connector_ids",
                          toggleIn(form.tool_connector_ids, c.id, v),
                        )
                      }
                      label={`${c.name}${!c.is_active ? " (inactive)" : ""}`}
                    />
                  ))}
                </div>
              </Field>
              <Field label="Language">
                <Select
                  value={form.language}
                  onChange={(e) => set("language", e.target.value)}
                >
                  {[...new Set([...LANGUAGES, form.language])].map((l) => (
                    <option key={l} value={l}>
                      {l}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field
                label="Default platforms"
                help="Used when drafting posts and the task doesn't say."
              >
                <div className="flex flex-wrap gap-3">
                  {(meta?.platforms ?? []).map((p) => (
                    <Checkbox
                      key={p.id}
                      checked={form.platforms.includes(p.id)}
                      onChange={(v) =>
                        set("platforms", toggleIn(form.platforms, p.id, v))
                      }
                      label={p.name}
                    />
                  ))}
                </div>
              </Field>
              <Field
                label="Max turns per run"
                help="Upper bound on LLM round-trips each time the agent works a task."
              >
                <Input
                  type="number"
                  min={1}
                  max={50}
                  value={form.max_turns}
                  onChange={(e) =>
                    set("max_turns", Number(e.target.value) || 0)
                  }
                />
              </Field>
            </div>
          </Card>

          {id && <MemoryPanel agentId={id} />}

          {id && (
            <div className="flex justify-end">
              <Button
                variant="ghost"
                size="sm"
                onClick={remove}
                className="text-red-600 dark:text-red-400"
              >
                <Icon name="trash" className="h-3.5 w-3.5" /> Delete agent
              </Button>
            </div>
          )}
        </div>
      </div>

      {id && (
        <NewTaskModal
          open={assignOpen}
          onClose={() => setAssignOpen(false)}
          agents={existing.data ? [existing.data] : []}
          defaultAgentId={id}
          onCreated={(t) => router.push(`/tasks/view/?id=${t.id}`)}
        />
      )}
    </div>
  );
}

function MemoryPanel({ agentId }: { agentId: string }) {
  const toast = useToast();
  const memories = useAsync(() => api.agentMemories(agentId), [agentId]);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const list = useMemo(() => memories.data ?? [], [memories.data]);

  const add = async () => {
    if (!draft.trim()) return;
    setBusy(true);
    try {
      await api.addAgentMemory(agentId, draft.trim());
      setDraft("");
      void memories.reload();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };
  const remove = async (mid: string) => {
    try {
      await api.deleteAgentMemory(agentId, mid);
      void memories.reload();
    } catch (e) {
      toast.error(e);
    }
  };

  return (
    <Card>
      <CardHeader
        title="Memory"
        subtitle="Notes the agent always sees. Agents with the Memory capability add their own."
      />
      <div className="space-y-2 p-4">
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            void add();
          }}
        >
          <Input
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder="e.g. Most readers are engineers in Taiwan"
          />
          <Button
            type="submit"
            variant="secondary"
            loading={busy}
            disabled={!draft.trim()}
          >
            Add
          </Button>
        </form>
        {memories.error && (
          <ErrorBox onRetry={memories.reload}>{memories.error}</ErrorBox>
        )}
        {memories.loading && !memories.data ? (
          <Loading />
        ) : list.length === 0 ? (
          <p className="text-xs text-zinc-500">No memories yet.</p>
        ) : (
          <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {list.map((m) => (
              <li key={m.id} className="group flex items-start gap-2 py-2">
                <p className="min-w-0 flex-1 text-sm whitespace-pre-wrap text-zinc-700 dark:text-zinc-300">
                  {m.content}
                </p>
                <span className="shrink-0 text-[11px] text-zinc-400">
                  {fmtRelative(m.created_at)}
                </span>
                <button
                  onClick={() => void remove(m.id)}
                  className="shrink-0 rounded p-0.5 text-zinc-400 hover:bg-zinc-100 hover:text-red-600 dark:hover:bg-zinc-800"
                  aria-label="Delete memory"
                >
                  <Icon name="x" className="h-3.5 w-3.5" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </Card>
  );
}

export default function Page() {
  return (
    <Suspense fallback={<Loading />}>
      <AgentEditor />
    </Suspense>
  );
}
