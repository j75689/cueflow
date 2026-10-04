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
import { UNAUTHORIZED_EVENT, api, errorMessage, eventsURL, getToken, setToken } from "@/lib/api";
import type { Meta } from "@/lib/types";
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
              "border-zinc-200 bg-white text-zinc-800 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-200",
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

const SSE_EVENTS = ["post.updated", "post.deleted", "run.updated", "connector.updated"];

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

  // SSE bus: one shared EventSource with exponential backoff reconnects.
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
    let backoff = 1000;
    let closed = false;

    const connect = () => {
      if (closed) return;
      es = new EventSource(eventsURL());
      es.onopen = () => {
        backoff = 1000;
        setConnected(true);
      };
      es.onerror = () => {
        setConnected(false);
        es?.close();
        if (!closed) {
          retry = setTimeout(connect, backoff);
          backoff = Math.min(backoff * 2, 30000);
        }
      };
      for (const name of SSE_EVENTS) {
        es.addEventListener(name, (ev) => {
          let data: Record<string, unknown> = {};
          try {
            data = JSON.parse((ev as MessageEvent).data);
          } catch {
            /* ignore malformed payloads */
          }
          listeners.current.get(name)?.forEach((fn) => fn(data));
        });
      }
    };
    connect();
    return () => {
      closed = true;
      if (retry) clearTimeout(retry);
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
          <div key={authEpoch} className="contents">
            {children}
          </div>
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
              <p className="text-sm text-zinc-600 dark:text-zinc-400">
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
