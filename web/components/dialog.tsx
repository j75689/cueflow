"use client";

import {
  type ReactNode,
  createContext,
  useCallback,
  useContext,
  useId,
  useRef,
  useState,
} from "react";

import { Button, Field, Input, Modal, Textarea } from "./ui";

export type ConfirmOptions = {
  title: ReactNode;
  message?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: "default" | "danger";
};

export type PromptOptions = {
  title: ReactNode;
  message?: ReactNode;
  label?: string;
  placeholder?: string;
  defaultValue?: string;
  required?: boolean;
  multiline?: boolean;
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: "default" | "danger";
};

export type AlertOptions = {
  title: ReactNode;
  message?: ReactNode;
  okLabel?: string;
};

type Request =
  | { kind: "confirm"; opts: ConfirmOptions; resolve: (v: boolean) => void }
  | { kind: "prompt"; opts: PromptOptions; resolve: (v: string | null) => void }
  | { kind: "alert"; opts: AlertOptions; resolve: () => void };

type DialogApi = {
  confirm: (opts: ConfirmOptions) => Promise<boolean>;
  prompt: (opts: PromptOptions) => Promise<string | null>;
  alert: (opts: AlertOptions) => Promise<void>;
};

const DialogCtx = createContext<DialogApi | null>(null);

// useDialog returns promise-based replacements for window.confirm / prompt / alert.
export function useDialog(): DialogApi {
  const api = useContext(DialogCtx);
  if (!api) throw new Error("useDialog must be used inside <DialogProvider>");
  return api;
}

export function DialogProvider({ children }: { children: ReactNode }) {
  // A queue, so a dialog requested while another is open waits its turn.
  const [queue, setQueue] = useState<Request[]>([]);
  const push = useCallback((r: Request) => setQueue((q) => [...q, r]), []);
  const api = useRef<DialogApi>({
    confirm: (opts) =>
      new Promise((resolve) => push({ kind: "confirm", opts, resolve })),
    prompt: (opts) =>
      new Promise((resolve) => push({ kind: "prompt", opts, resolve })),
    alert: (opts) =>
      new Promise((resolve) => push({ kind: "alert", opts, resolve })),
  }).current;

  const current = queue[0];
  const done = useCallback(() => setQueue((q) => q.slice(1)), []);

  return (
    <DialogCtx.Provider value={api}>
      {children}
      {current && (
        <DialogView
          key={queue.length + ":" + current.kind}
          req={current}
          onDone={done}
        />
      )}
    </DialogCtx.Provider>
  );
}

function DialogView({ req, onDone }: { req: Request; onDone: () => void }) {
  const msgId = useId();
  const [value, setValue] = useState(
    req.kind === "prompt" ? (req.opts.defaultValue ?? "") : "",
  );
  const settled = useRef(false);

  const finish = (result: boolean) => {
    if (settled.current) return;
    settled.current = true;
    if (req.kind === "confirm") req.resolve(result);
    else if (req.kind === "prompt") req.resolve(result ? value.trim() : null);
    else req.resolve();
    onDone();
  };

  const opts = req.opts;
  const tone = req.kind !== "alert" ? (req.opts.tone ?? "default") : "default";
  const blocked = req.kind === "prompt" && !!req.opts.required && !value.trim();
  const submit = () => {
    if (!blocked) finish(true);
  };
  const message = opts.message ? (
    <div
      id={msgId}
      className="text-sm leading-relaxed text-fg-2"
    >
      {opts.message}
    </div>
  ) : null;

  const okLabel =
    req.kind === "alert"
      ? (req.opts.okLabel ?? "OK")
      : (req.opts.confirmLabel ?? (tone === "danger" ? "Delete" : "Confirm"));
  const cancelLabel =
    req.kind === "alert" ? null : (req.opts.cancelLabel ?? "Cancel");

  return (
    <Modal
      open
      onClose={() => finish(false)}
      title={opts.title}
      role={req.kind === "prompt" ? "dialog" : "alertdialog"}
      describedBy={opts.message ? msgId : undefined}
      footer={
        <>
          {cancelLabel && (
            <Button variant="ghost" onClick={() => finish(false)}>
              {cancelLabel}
            </Button>
          )}
          <Button
            variant={tone === "danger" ? "danger" : "primary"}
            onClick={submit}
            disabled={blocked}
            data-autofocus={req.kind === "prompt" ? undefined : ""}
          >
            {okLabel}
          </Button>
        </>
      }
    >
      {req.kind === "prompt" ? (
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          {message}
          <Field
            label={req.opts.label ?? ""}
            required={req.opts.required}
            className={req.opts.label ? "" : "[&>span:first-child]:hidden"}
          >
            {req.opts.multiline ? (
              <Textarea
                data-autofocus=""
                value={value}
                placeholder={req.opts.placeholder}
                onChange={(e) => setValue(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                    e.preventDefault();
                    submit();
                  }
                }}
              />
            ) : (
              <Input
                data-autofocus=""
                value={value}
                placeholder={req.opts.placeholder}
                onChange={(e) => setValue(e.target.value)}
              />
            )}
          </Field>
          {req.opts.multiline && (
            <p className="text-[11px] text-fg-4">
              ⌘/Ctrl + Enter to submit
            </p>
          )}
        </form>
      ) : (
        <div
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              submit();
            }
          }}
        >
          {message ?? <span className="sr-only">{okLabel}</span>}
        </div>
      )}
    </Modal>
  );
}
