"use client";

import { useState } from "react";
import { useDialog } from "@/components/dialog";
import { TagInput } from "@/components/inputs";
import { useServerEvent, useToast } from "@/components/providers";
import {
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorBox,
  Field,
  Icon,
  Loading,
  Modal,
  PageHeader,
  Pill,
  Toggle,
  cx,
} from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative, fmtTime } from "@/lib/format";
import { useAsync, useDebouncedCallback } from "@/lib/hooks";
import type { Runner, RunnerRegistration } from "@/lib/types";

export default function RunnersPage() {
  const toast = useToast();
  const dialog = useDialog();
  const runners = useAsync(() => api.listRunners(), []);
  const refresh = useDebouncedCallback(() => void runners.reload(), 300);
  useServerEvent("runner.updated", refresh);
  useServerEvent("task.updated", refresh);
  const [adding, setAdding] = useState(false);

  const patch = async (r: Runner, body: Parameters<typeof api.updateRunner>[1]) => {
    try {
      const u = await api.updateRunner(r.id, body);
      runners.setData((runners.data ?? []).map((x) => (x.id === r.id ? { ...x, ...u } : x)));
    } catch (e) {
      toast.error(e);
    }
  };

  const revoke = async (r: Runner) => {
    const ok = await dialog.confirm({
      title: `Revoke ${r.name}?`,
      message: "Its token stops working at once and any job it is running is canceled.",
      confirmLabel: "Revoke",
      tone: "danger",
    });
    if (!ok) return;
    try {
      await api.deleteRunner(r.id);
      void runners.reload();
    } catch (e) {
      toast.error(e);
    }
  };

  return (
    <div>
      <PageHeader
        title="Runners"
        subtitle="Your own machines that work agents' tasks. A runner connects out to CueFlow, runs the agent CLI locally with its own credentials, and reports back."
        actions={
          <Button variant="primary" onClick={() => setAdding(true)}>
            <Icon name="plus" /> Add runner
          </Button>
        }
      />
      {runners.error && <ErrorBox onRetry={runners.reload}>{runners.error}</ErrorBox>}
      {runners.loading && !runners.data && <Loading />}
      {runners.data && (
        <Card>
          <CardHeader
            title="Registered runners"
            subtitle="Point an agent at them with a Runner pool connector; its labels decide which runners may take the work."
          />
          {runners.data.length === 0 ? (
            <EmptyState
              title="No runners yet"
              hint="Add one, then run the command it shows on the machine that should do the work."
            />
          ) : (
            <>
              <div
                className={cx(
                  grid,
                  "hidden border-b border-line py-2 text-[11px] font-medium tracking-wider text-fg-4 uppercase md:grid",
                )}
              >
                <span>Runner</span>
                <span>Labels</span>
                <span>Load</span>
                <span className="text-right">Active</span>
              </div>
              <ul className="divide-y divide-line">
                {runners.data.map((r) => (
                  <li
                    key={r.id}
                    className={cx(grid, "group flex flex-col items-stretch gap-3 py-3.5 hover:bg-subtle/40 md:grid")}
                  >
                    <div className="min-w-0">
                      <div className="flex items-center gap-2.5">
                        <span
                          className={cx(
                            "h-2 w-2 shrink-0 rounded-full",
                            r.online ? "bg-emerald-500 ring-3 ring-emerald-500/15" : "bg-zinc-400 dark:bg-zinc-600",
                          )}
                          title={r.online ? "Online" : "Offline"}
                        />
                        <span className="truncate text-sm font-medium text-fg">{r.name}</span>
                        {!r.online && <Pill className="bg-subtle text-fg-3">Offline</Pill>}
                      </div>
                      <div className="mt-0.5 ml-[18px] text-xs text-fg-4 tabular-nums">
                        <span className="whitespace-nowrap">{r.platform || "unknown platform"}</span>
                        {r.version && (
                          <>
                            <Sep />
                            <span className="whitespace-nowrap">v{r.version}</span>
                          </>
                        )}
                        <Sep />
                        <span className="whitespace-nowrap" title={fmtTime(r.last_seen_at)}>
                          {r.last_seen_at ? `seen ${fmtRelative(r.last_seen_at)}` : "never seen"}
                        </span>
                      </div>
                    </div>
                    <TagInput
                      variant="inline"
                      value={r.labels}
                      onChange={(labels) => void patch(r, { labels })}
                      placeholder="Add label"
                    />
                    <div className="flex items-center gap-4 md:contents">
                      <div className="min-w-0 flex-1 text-xs text-fg-3 tabular-nums">
                        <span className="font-medium text-fg">{r.running_jobs}</span> / {r.max_concurrency} running
                        <div className="mt-1.5 h-1 overflow-hidden rounded-full bg-muted">
                          <div
                            className="h-full rounded-full bg-accent-solid"
                            style={{ width: `${Math.min(100, (100 * r.running_jobs) / Math.max(r.max_concurrency, 1))}%` }}
                          />
                        </div>
                      </div>
                      <div className="flex items-center justify-end gap-3">
                        <Toggle checked={r.is_active} onChange={(on) => void patch(r, { is_active: on })} />
                        <button
                          type="button"
                          onClick={() => void revoke(r)}
                          className="h-7 rounded-md border border-transparent px-2.5 text-xs font-medium text-fg-3 transition-colors hover:border-red-500/25 hover:bg-red-500/10 hover:text-red-600 focus-visible:border-red-500/25 focus-visible:bg-red-500/10 focus-visible:text-red-600 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-indigo-500 dark:hover:text-red-300 dark:focus-visible:text-red-300"
                        >
                          Revoke
                        </button>
                      </div>
                    </div>
                  </li>
                ))}
              </ul>
            </>
          )}
        </Card>
      )}
      <AddRunner open={adding} onClose={() => setAdding(false)} />
    </div>
  );
}

// grid lays out the list's header and rows as Runner | Labels | Load | Active from md up.
const grid = "px-4 md:grid-cols-[minmax(15rem,1fr)_minmax(0,1.6fr)_7.5rem_9.5rem] md:items-center md:gap-6";

function Sep() {
  return <span className="mx-1.5 text-line-strong">·</span>;
}

function AddRunner({ open, onClose }: { open: boolean; onClose: () => void }) {
  const toast = useToast();
  const [labels, setLabels] = useState<string[]>(["claude-code"]);
  const [busy, setBusy] = useState(false);
  const [reg, setReg] = useState<RunnerRegistration | null>(null);

  const close = () => {
    setReg(null);
    onClose();
  };
  const create = async () => {
    setBusy(true);
    try {
      setReg(await api.createRunnerRegistration({ labels }));
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={close}
      title="Add a runner"
      footer={
        reg ? (
          <Button variant="primary" onClick={close}>
            Done
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={close}>
              Cancel
            </Button>
            <Button variant="primary" loading={busy} onClick={() => void create()}>
              Create registration
            </Button>
          </>
        )
      }
    >
      {!reg ? (
        <div className="space-y-3">
          <Field
            label="Labels"
            help="A Runner pool connector asks for labels; a runner needs all of them, or one of them when the pool matches any."
          >
            <TagInput value={labels} onChange={setLabels} placeholder="Add label" />
          </Field>
        </div>
      ) : (
        <div className="space-y-3 text-sm">
          <p>
            Run this on the machine that should do the work (it needs the <code>cueflow</code> binary and the agent
            CLI, e.g. Claude Code). The token works once and expires {fmtRelative(reg.expires_at)}.
          </p>
          <pre className="overflow-x-auto rounded-md bg-subtle p-3 text-xs break-all whitespace-pre-wrap">
            {reg.command}
          </pre>
          <p>Then start it with:</p>
          <pre className="rounded-md bg-subtle p-3 text-xs">cueflow runner run</pre>
          <Button
            size="sm"
            onClick={() => {
              void navigator.clipboard?.writeText(reg.command);
              toast.success("Copied");
            }}
          >
            Copy command
          </Button>
        </div>
      )}
    </Modal>
  );
}
