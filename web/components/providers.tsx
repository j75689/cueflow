"use client";

import {
  type ReactNode,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { ORG_CHANGED_EVENT, UNAUTHORIZED_EVENT, api, apiOrg, errorMessage, eventsURL, getToken, setToken } from "@/lib/api";
import type { Meta } from "@/lib/types";
import { DialogProvider } from "./dialog";
import { Button, Field, Input, Modal, cx } from "./ui";

/* ---------------------------------- Toasts --------------------------------- */

type ToastKind = "success" | "error" | "info";
interface Toast {
  id: number;
  kind: ToastKind;
  text: string;
}

const ToastCtx = createContext<(kind: ToastKind, text: string) => void>(() => {});

export function useToast() {
  const push = useContext(ToastCtx);
  return useMemo(
    () => ({
      success: (t: string) => push("success", t),
      error: (t: string | unknown) => push("error", typeof t === "string" ? t : errorMessage(t)),
      info: (t: string) => push("info", t),
    }),
    [push],
  );
}

function ToastHost({ toasts, dismiss }: { toasts: Toast[]; dismiss: (id: number) => void }) {
  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-0 z-[60] flex flex-col items-center gap-2 p-4 sm:items-end">
      {toasts.map((t) => (
        <div
          key={t.id}
          role="status"
          onClick={() => dismiss(t.id)}
          className={cx(
            "pointer-events-auto w-full max-w-sm cursor-pointer rounded-md border px-3 py-2 text-sm shadow-lg",
            t.kind === "success" &&
              "border-emerald-200 bg-emerald-50 text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-200",
            t.kind === "error" &&
              "border-red-200 bg-red-50 text-red-900 dark:border-red-900 dark:bg-red-950 dark:text-red-200",
            t.kind === "info" &&
              "border-line bg-surface text-fg",
          )}
        >
          {t.text}
        </div>
      ))}
    </div>
  );
}

/* ----------------------------------- Meta ---------------------------------- */

interface MetaState {
  meta: Meta | null;
  error: string | null;
  reload: () => void;
}
const MetaCtx = createContext<MetaState>({ meta: null, error: null, reload: () => {} });
export const useMeta = () => useContext(MetaCtx);

/**
 * Whether a feature module is mounted on the server. Until /meta has loaded (or on servers that
 * predate the field) every module counts as present, so nothing flickers or disappears.
 */
export function useModule(id: string): boolean {
  const { meta } = useMeta();
  return !meta?.modules || meta.modules.includes(id);
}

/* ------------------------------ Server events ------------------------------ */

type Listener = (data: Record<string, unknown>) => void;
interface Bus {
  subscribe: (event: string, fn: Listener) => () => void;
  connected: boolean;
}
const BusCtx = createContext<Bus>({ subscribe: () => () => {}, connected: false });

export function useServerEvents() {
  return useContext(BusCtx);
}

// useServerEvent calls fn whenever one of the named SSE events arrives.
export function useServerEvent(events: string | string[], fn: Listener) {
  const { subscribe } = useServerEvents();
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const key = Array.isArray(events) ? events.join(",") : events;
  useEffect(() => {
    const offs = key.split(",").map((e) => subscribe(e, (d) => fnRef.current(d)));
    return () => offs.forEach((off) => off());
  }, [key, subscribe]);
}

/** How long a tab may be out of sight before its live updates are closed (see the SSE bus below). */
const HIDDEN_GRACE_MS = 60_000;

const SSE_EVENTS = [
  "post.updated",
  "post.deleted",
  "deliverable.updated",
  "deliverable.deleted",
  "run.updated",
  "run.context",
  "connector.updated",
  "task.updated",
  "task.deleted",
  "task.message",
  "task.file",
  "agent.updated",
  "schedule.updated",
  "org.updated",
];

/* -------------------------------- Providers -------------------------------- */

export function Providers({ children }: { children: ReactNode }) {
  // toasts
  const [toasts, setToasts] = useState<Toast[]>([]);
  const nextId = useRef(1);
  const dismiss = useCallback((id: number) => setToasts((ts) => ts.filter((t) => t.id !== id)), []);
  const push = useCallback(
    (kind: ToastKind, text: string) => {
      const id = nextId.current++;
      setToasts((ts) => [...ts.slice(-3), { id, kind, text }]);
      setTimeout(() => dismiss(id), kind === "error" ? 7000 : 3500);
    },
    [dismiss],
  );

  // auth prompt
  const [authOpen, setAuthOpen] = useState(false);
  const [tokenDraft, setTokenDraft] = useState("");
  const [authEpoch, setAuthEpoch] = useState(0);
  useEffect(() => {
    const open = () => {
      setTokenDraft(getToken());
      setAuthOpen(true);
    };
    window.addEventListener(UNAUTHORIZED_EVENT, open);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, open);
  }, []);

  // meta
  const [meta, setMeta] = useState<Meta | null>(null);
  const [metaError, setMetaError] = useState<string | null>(null);
  const loadMeta = useCallback(() => {
    api
      .meta()
      .then((m) => {
        setMeta(m);
        setMetaError(null);
      })
      .catch((e) => setMetaError(errorMessage(e)));
  }, []);
  useEffect(loadMeta, [loadMeta, authEpoch]);

  // SSE bus: one shared EventSource with exponential backoff reconnects, closed while the tab is hidden.
  const listeners = useRef(new Map<string, Set<Listener>>());
  const [connected, setConnected] = useState(false);
  const subscribe = useCallback((event: string, fn: Listener) => {
    let set = listeners.current.get(event);
    if (!set) {
      set = new Set();
      listeners.current.set(event, set);
    }
    set.add(fn);
    return () => {
      set.delete(fn);
    };
  }, []);

  useEffect(() => {
    if (typeof EventSource === "undefined") return;
    let es: EventSource | null = null;
    let retry: ReturnType<typeof setTimeout> | null = null;
    let sleep: ReturnType<typeof setTimeout> | null = null;
    let backoff = 1000;
    let closed = false;
    // The stream was closed while the tab was in the background, so updates may have been missed: when it opens
    // again, every listener is told to catch up ({ resync: true }).
    let missed = false;

    const connect = () => {
      if (closed || es) return;
      const stream = new EventSource(eventsURL());
      es = stream;
      stream.onopen = () => {
        backoff = 1000;
        setConnected(true);
        if (missed) {
          missed = false;
          const told = new Set<Listener>();
          for (const name of SSE_EVENTS) {
            listeners.current.get(name)?.forEach((fn) => {
              if (!told.has(fn)) {
                told.add(fn);
                fn({ resync: true });
              }
            });
          }
        }
      };
      stream.onerror = () => {
        setConnected(false);
        stream.close();
        if (es === stream) es = null;
        if (!closed && !document.hidden) {
          retry = setTimeout(connect, backoff);
          backoff = Math.min(backoff * 2, 30000);
        }
      };
      for (const name of SSE_EVENTS) {
        stream.addEventListener(name, (ev) => {
          let data: Record<string, unknown> = {};
          try {
            data = JSON.parse((ev as MessageEvent).data);
          } catch {
            /* ignore malformed payloads */
          }
          // The stream is opened for one org; an event for another can only be one sent before a switch. With
          // no org chosen (yet, or the org list failed to load) the stream is unfiltered and every event counts.
          const org = apiOrg();
          if (org && typeof data.org_id === "string" && data.org_id !== org) return;
          listeners.current.get(name)?.forEach((fn) => fn(data));
        });
      }
    };

    // An open stream is a request the server is busy with, and Cloud Run bills the whole time one is open. A tab
    // left in the background would keep it busy all day, so the stream closes after a minute out of sight and
    // reopens (and catches up) when the tab is looked at again.
    const disconnect = () => {
      if (retry) clearTimeout(retry);
      retry = null;
      if (es) {
        es.close();
        es = null;
        missed = true;
        setConnected(false);
      }
    };
    const onVisibility = () => {
      if (document.hidden) {
        if (!sleep) sleep = setTimeout(disconnect, HIDDEN_GRACE_MS);
        return;
      }
      if (sleep) clearTimeout(sleep);
      sleep = null;
      backoff = 1000;
      connect();
    };

    // The stream carries one org's events: reopen it for the new org when the org changes. No catch-up is
    // needed then: the pages remount for the new org (see Shell) and load it afresh. A hidden tab stays closed
    // and keeps `missed`, so it catches up on what happened meanwhile when it is looked at again.
    const onOrgChange = () => {
      if (!es) return;
      disconnect();
      if (document.hidden) return;
      missed = false;
      connect();
    };

    document.addEventListener("visibilitychange", onVisibility);
    window.addEventListener(ORG_CHANGED_EVENT, onOrgChange);
    if (document.hidden) onVisibility();
    connect();
    return () => {
      closed = true;
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener(ORG_CHANGED_EVENT, onOrgChange);
      if (retry) clearTimeout(retry);
      if (sleep) clearTimeout(sleep);
      es?.close();
    };
  }, [authEpoch]);

  const bus = useMemo(() => ({ subscribe, connected }), [subscribe, connected]);
  const metaState = useMemo(() => ({ meta, error: metaError, reload: loadMeta }), [meta, metaError, loadMeta]);

  const saveToken = () => {
    setToken(tokenDraft.trim());
    setAuthOpen(false);
    setAuthEpoch((n) => n + 1);
    push("info", "Token saved");
  };

  return (
    <ToastCtx.Provider value={push}>
      <MetaCtx.Provider value={metaState}>
        <BusCtx.Provider value={bus}>
          {/* Re-mount pages after a new token so they refetch. */}
          <DialogProvider>
            <div key={authEpoch} className="contents">
              {children}
            </div>
          </DialogProvider>
          <Modal
            open={authOpen}
            onClose={() => setAuthOpen(false)}
            title="Admin token required"
            footer={
              <>
                <Button variant="ghost" onClick={() => setAuthOpen(false)}>
                  Cancel
                </Button>
                <Button variant="primary" onClick={saveToken}>
                  Save token
                </Button>
              </>
            }
          >
            <form
              onSubmit={(e) => {
                e.preventDefault();
                saveToken();
              }}
              className="space-y-3"
            >
              <p className="text-sm text-fg-2">
                This CueFlow server is protected by <code className="font-mono text-xs">CUEFLOW_ADMIN_TOKEN</code>.
                Paste it below; it is stored only in this browser.
              </p>
              <Field label="Token">
                <Input
                  type="password"
                  autoFocus
                  value={tokenDraft}
                  onChange={(e) => setTokenDraft(e.target.value)}
                  placeholder="cueflow admin token"
                />
              </Field>
            </form>
          </Modal>
          <ToastHost toasts={toasts} dismiss={dismiss} />
        </BusCtx.Provider>
      </MetaCtx.Provider>
    </ToastCtx.Provider>
  );
}
