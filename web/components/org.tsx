"use client";

import { type ReactNode, createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { api, errorMessage, setApiOrg } from "@/lib/api";
import type { Org } from "@/lib/types";
import { useDialog } from "./dialog";
import { Popover } from "./popover";
import { useServerEvent, useToast } from "./providers";
import { Button, Checkbox, Field, Icon, Input, Modal, cx } from "./ui";

const ORG_KEY = "cueflow.org";

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
        className="flex w-full items-center gap-2 rounded-md border border-zinc-200 px-2.5 py-1.5 text-left text-sm hover:bg-zinc-50 dark:border-zinc-800 dark:hover:bg-zinc-800/60"
        aria-haspopup="listbox"
        aria-expanded={open}
      >
        <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded bg-zinc-900 text-[10px] font-semibold text-white uppercase dark:bg-zinc-100 dark:text-zinc-900">
          {(org?.name ?? "?").slice(0, 1)}
        </span>
        <span className="min-w-0 flex-1 truncate font-medium text-zinc-900 dark:text-zinc-100">{org?.name ?? "…"}</span>
        <Icon name="down" className="h-3.5 w-3.5 text-zinc-400" />
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
                  "flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-zinc-100 dark:hover:bg-zinc-800",
                  o.id === org?.id && "font-medium text-indigo-700 dark:text-indigo-300",
                )}
              >
                <span className="min-w-0 flex-1 truncate">{o.name}</span>
                {o.id === org?.id && <Icon name="check" className="h-3.5 w-3.5" />}
              </button>
            ))}
            <div className="my-1 border-t border-zinc-200 dark:border-zinc-800" />
            <button
              onClick={() => {
                setOpen(false);
                setManage(true);
              }}
              className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm text-zinc-600 hover:bg-zinc-100 dark:text-zinc-400 dark:hover:bg-zinc-800"
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
      message: "Only an empty org can be deleted: remove its agents, tasks, schedules and posts first.",
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
        <p className="text-sm text-zinc-600 dark:text-zinc-400">
          Each org has its own agents, board, schedules, review queue and runs. Connectors and usage are shared.
        </p>
        <ul className="divide-y divide-zinc-200 rounded-md border border-zinc-200 dark:divide-zinc-800 dark:border-zinc-800">
          {orgs.map((o, i) => (
            <li key={o.id} className="flex items-center gap-2 px-3 py-2 text-sm">
              <span className="min-w-0 flex-1">
                <span className="font-medium text-zinc-900 dark:text-zinc-100">{o.name}</span>
                <span className="ml-2 font-mono text-xs text-zinc-500">{o.slug}</span>
                {i === 0 && <span className="ml-2 text-xs text-zinc-500">default</span>}
                {o.id === org?.id && <span className="ml-2 text-xs text-indigo-600 dark:text-indigo-400">current</span>}
              </span>
              <Button size="sm" variant="ghost" onClick={() => rename(o)} aria-label={`Rename ${o.name}`}>
                <Icon name="edit" className="h-3.5 w-3.5" />
              </Button>
              {orgs.length > 1 && (
                <Button size="sm" variant="ghost" onClick={() => remove(o)} aria-label={`Delete ${o.name}`}>
                  <Icon name="trash" className="h-3.5 w-3.5" />
                </Button>
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
          <Checkbox checked={seed} onChange={setSeed} label="Start with the default crew (Chief, Researcher, Writer, Editor)" />
        </form>
      </div>
    </Modal>
  );
}
