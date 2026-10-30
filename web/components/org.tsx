"use client";

import { type ReactNode, createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { api, errorMessage, setApiOrg } from "@/lib/api";
import type { Connector, Org } from "@/lib/types";
import { useDialog } from "./dialog";
import { Popover } from "./popover";
import { useMeta, useServerEvent, useToast } from "./providers";
import { Select } from "./select";
import { Button, Checkbox, Field, Icon, Input, Modal, cx } from "./ui";

const ORG_KEY = "cueflow.org";
/** The highest agent_concurrency the server accepts (store.MaxOrgAgentConcurrency). */
const MAX_AGENT_CONCURRENCY = 64;

interface OrgState {
  orgs: Org[];
  org: Org | null;
  /** True once the org list has loaded (or failed), so pages can fetch with the right org. */
  ready: boolean;
  setOrg: (id: string) => void;
  reload: () => Promise<void>;
}

const OrgCtx = createContext<OrgState>({ orgs: [], org: null, ready: true, setOrg: () => {}, reload: async () => {} });

export const useOrg = () => useContext(OrgCtx);

function stored(): string {
  try {
    return window.localStorage.getItem(ORG_KEY) ?? "";
  } catch {
    return "";
  }
}

function remember(id: string) {
  try {
    window.localStorage.setItem(ORG_KEY, id);
  } catch {
    /* storage unavailable: the choice lasts for this page only */
  }
}

// pick chooses the org from ?org= (id or slug), then the last choice, then the default (first) org.
function pick(orgs: Org[], current: string): Org | null {
  const fromURL = typeof window === "undefined" ? "" : (new URLSearchParams(window.location.search).get("org") ?? "");
  for (const ref of [fromURL, current, stored()]) {
    const o = ref && orgs.find((x) => x.id === ref || x.slug === ref);
    if (o) return o;
  }
  return orgs[0] ?? null;
}

export function OrgProvider({ children }: { children: ReactNode }) {
  const [orgs, setOrgs] = useState<Org[]>([]);
  const [orgId, setOrgId] = useState("");
  const [ready, setReady] = useState(false);
  const orgIdRef = useRef("");

  const choose = useCallback((o: Org | null) => {
    const id = o?.id ?? "";
    // Set the API org before React re-renders the pages, so their first fetch uses it.
    setApiOrg(id);
    orgIdRef.current = id;
    if (id) remember(id);
    setOrgId(id);
  }, []);

  const reload = useCallback(async () => {
    try {
      const list = await api.listOrgs();
      setOrgs(list);
      choose(pick(list, orgIdRef.current));
    } catch {
      /* unauthorized or offline: pages fall back to the server's default org */
    } finally {
      setReady(true);
    }
  }, [choose]);

  useEffect(() => {
    void reload();
  }, [reload]);
  useServerEvent("org.updated", () => void reload());

  const setOrg = useCallback(
    (id: string) => {
      const o = orgs.find((x) => x.id === id);
      if (o && o.id !== orgIdRef.current) choose(o);
    },
    [orgs, choose],
  );

  const value = useMemo(
    () => ({ orgs, org: orgs.find((o) => o.id === orgId) ?? null, ready, setOrg, reload }),
    [orgs, orgId, ready, setOrg, reload],
  );
  return <OrgCtx.Provider value={value}>{children}</OrgCtx.Provider>;
}

/** useOrgFollow switches to the org of the item a detail page shows (e.g. a task opened from a link). */
export function useOrgFollow(orgId: string | null | undefined) {
  const { org, setOrg } = useOrg();
  useEffect(() => {
    if (orgId && org && orgId !== org.id) setOrg(orgId);
  }, [orgId, org, setOrg]);
}

/** OrgSwitcher sits at the top of the sidebar: pick the org, or manage orgs. */
export function OrgSwitcher() {
  const { orgs, org, setOrg } = useOrg();
  const [open, setOpen] = useState(false);
  const [manage, setManage] = useState(false);
  const ref = useRef<HTMLButtonElement>(null);

  return (
    <div className="px-2 pb-1">
      <button
        ref={ref}
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 rounded-md border border-line px-2.5 py-1.5 text-left text-sm hover:bg-subtle"
        aria-haspopup="listbox"
        aria-expanded={open}
      >
        <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded bg-inverse text-[10px] font-semibold text-on-inverse uppercase">
          {(org?.name ?? "?").slice(0, 1)}
        </span>
        <span className="min-w-0 flex-1 truncate font-medium text-fg">{org?.name ?? "…"}</span>
        <Icon name="down" className="h-3.5 w-3.5 text-fg-4" />
      </button>
      <Popover open={open} anchorRef={ref} onClose={() => setOpen(false)} minWidth={208} label="Orgs">
        {(maxHeight) => (
          <div className="overflow-y-auto py-1" style={{ maxHeight }} role="listbox">
            {orgs.map((o) => (
              <button
                key={o.id}
                role="option"
                aria-selected={o.id === org?.id}
                onClick={() => {
                  setOpen(false);
                  setOrg(o.id);
                }}
                className={cx(
                  "flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-subtle",
                  o.id === org?.id && "font-medium text-accent",
                )}
              >
                <span className="min-w-0 flex-1 truncate">{o.name}</span>
                {o.id === org?.id && <Icon name="check" className="h-3.5 w-3.5" />}
              </button>
            ))}
            <div className="my-1 border-t border-line" />
            <button
              onClick={() => {
                setOpen(false);
                setManage(true);
              }}
              className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm text-fg-2 hover:bg-subtle"
            >
              <Icon name="edit" className="h-3.5 w-3.5" /> Manage orgs
            </button>
          </div>
        )}
      </Popover>
      <OrgManager open={manage} onClose={() => setManage(false)} />
    </div>
  );
}

function OrgManager({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { orgs, org, setOrg, reload } = useOrg();
  const toast = useToast();
  const dialog = useDialog();
  const [name, setName] = useState("");
  const [seed, setSeed] = useState(true);
  const [busy, setBusy] = useState(false);
  const [notifiers, setNotifiers] = useState<Connector[] | null>(null);
  const { meta } = useMeta();

  useEffect(() => {
    if (!open) return;
    api.listConnectors("notifier").then(setNotifiers, () => setNotifiers([]));
  }, [open]);

  const setNotifier = async (o: Org, id: string) => {
    try {
      await api.updateOrg(o.id, { notifier_id: id });
      await reload();
    } catch (e) {
      toast.error(e);
    }
  };

  const setConcurrency = async (o: Org, n: number | null) => {
    try {
      await api.updateOrg(o.id, { agent_concurrency: n });
      await reload();
    } catch (e) {
      toast.error(e);
    }
  };

  const create = async () => {
    if (!name.trim()) return;
    setBusy(true);
    try {
      const o = await api.createOrg({ name: name.trim(), seed_agents: seed });
      setName("");
      await reload();
      setOrg(o.id);
      toast.success(`Created ${o.name}`);
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  const rename = async (o: Org) => {
    const next = await dialog.prompt({ title: "Rename org", label: "Name", defaultValue: o.name, required: true });
    if (!next || next.trim() === o.name) return;
    try {
      await api.updateOrg(o.id, { name: next.trim() });
      await reload();
    } catch (e) {
      toast.error(e);
    }
  };

  const remove = async (o: Org) => {
    const ok = await dialog.confirm({
      title: `Delete ${o.name}?`,
      message: "Only an empty org can be deleted: remove its agents, tasks, schedules and deliverables first.",
      confirmLabel: "Delete",
      tone: "danger",
    });
    if (!ok) return;
    try {
      await api.deleteOrg(o.id);
      await reload();
      toast.success(`Deleted ${o.name}`);
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  return (
    <Modal open={open} onClose={onClose} title="Orgs">
      <div className="space-y-4">
        <p className="text-sm text-fg-2">
          Each org has its own agents, board, schedules, deliverables and runs. Connectors and usage are shared.
        </p>
        <p className="text-sm text-fg-2">
          {notifiers?.length
            ? "Alerts tell an org's chosen notifier when a task needs you: ready for review, a question, an approval, or a run that failed or was interrupted."
            : "To get an alert when a task needs you, add a notifier connector (Telegram or email), then pick it for the org here."}
        </p>
        <ul className="divide-y divide-line rounded-md border border-line">
          {orgs.map((o, i) => (
            <li key={o.id} className="space-y-2 px-3 py-2 text-sm">
              <div className="flex items-center gap-2">
                <span className="min-w-0 flex-1">
                  <span className="font-medium text-fg">{o.name}</span>
                  <span className="ml-2 font-mono text-xs text-fg-3">{o.slug}</span>
                  {i === 0 && <span className="ml-2 text-xs text-fg-3">default</span>}
                  {o.id === org?.id && <span className="ml-2 text-xs text-accent">current</span>}
                </span>
                <Button size="sm" variant="ghost" onClick={() => rename(o)} aria-label={`Rename ${o.name}`}>
                  <Icon name="edit" className="h-3.5 w-3.5" />
                </Button>
                {orgs.length > 1 && (
                  <Button size="sm" variant="ghost" onClick={() => remove(o)} aria-label={`Delete ${o.name}`}>
                    <Icon name="trash" className="h-3.5 w-3.5" />
                  </Button>
                )}
              </div>
              <div className="flex items-center gap-2">
                <span className="shrink-0 text-xs text-fg-3">Parallel tasks</span>
                <ConcurrencyInput
                  key={`${o.id}:${o.agent_concurrency ?? ""}`}
                  org={o}
                  fallback={meta?.agent_concurrency}
                  onSave={(n) => void setConcurrency(o, n)}
                />
              </div>
              {!!notifiers?.length && (
                <div className="flex items-center gap-2">
                  <span className="shrink-0 text-xs text-fg-3">Alerts</span>
                  <Select
                    className="min-w-0 flex-1"
                    value={o.notifier_id ?? ""}
                    onChange={(e) => void setNotifier(o, e.target.value)}
                    aria-label={`Alerts for ${o.name}`}
                  >
                    <option value="">Off</option>
                    {notifiers.map((n) => (
                      <option key={n.id} value={n.id}>
                        {n.is_active ? n.name : `${n.name} (disabled)`}
                      </option>
                    ))}
                  </Select>
                </div>
              )}
            </li>
          ))}
        </ul>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void create();
          }}
          className="space-y-2"
        >
          <Field label="New org">
            <div className="flex gap-2">
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. CueFlow Dev" />
              <Button type="submit" variant="primary" loading={busy} disabled={!name.trim()}>
                Create
              </Button>
            </div>
          </Field>
          <Checkbox checked={seed} onChange={setSeed} label="Start with a Chief agent" />
        </form>
      </div>
    </Modal>
  );
}

/**
 * ConcurrencyInput edits how many of an org's tasks agents work at once, across every server instance.
 * Blank goes back to the server's default (CUEFLOW_AGENT_CONCURRENCY), which the placeholder shows.
 */
function ConcurrencyInput({
  org,
  fallback,
  onSave,
}: {
  org: Org;
  fallback?: number;
  onSave: (n: number | null) => void;
}) {
  const [value, setValue] = useState(org.agent_concurrency?.toString() ?? "");
  const save = () => {
    const text = value.trim();
    const n = text === "" ? null : Number(text);
    if (n === org.agent_concurrency) return;
    if (n !== null && (!Number.isInteger(n) || n < 1 || n > MAX_AGENT_CONCURRENCY)) {
      setValue(org.agent_concurrency?.toString() ?? "");
      return;
    }
    onSave(n);
  };
  return (
    <Input
      type="number"
      min={1}
      max={MAX_AGENT_CONCURRENCY}
      step={1}
      className="w-28"
      value={value}
      placeholder={fallback ? `default ${fallback}` : "default"}
      onChange={(e) => setValue(e.target.value)}
      onBlur={save}
      onKeyDown={(e) => {
        if (e.key === "Enter") e.currentTarget.blur();
      }}
      aria-label={`Parallel tasks for ${org.name}`}
    />
  );
}
