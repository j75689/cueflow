"use client";

import { useState } from "react";
import { useDialog } from "@/components/dialog";
import { useServerEvent, useToast } from "@/components/providers";
import {
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorBox,
  Field,
  Icon,
  Input,
  Loading,
  Modal,
  PageHeader,
  Pill,
  Toggle,
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
            <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {runners.data.map((r) => (
                <li key={r.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                  <span
                    className={`h-2 w-2 shrink-0 rounded-full ${r.online ? "bg-emerald-500" : "bg-zinc-400"}`}
                    title={r.online ? "Online" : "Offline"}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">{r.name}</div>
                    <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-zinc-500">
                      {r.labels.map((l) => (
                        <Pill key={l}>{l}</Pill>
                      ))}
                      {r.labels.length === 0 && <span>no labels</span>}
                      <span>{r.platform || "unknown platform"}</span>
                      {r.version && <span>v{r.version}</span>}
                      <span title={fmtTime(r.last_seen_at)}>
                        {r.last_seen_at ? `seen ${fmtRelative(r.last_seen_at)}` : "never seen"}
                      </span>
                      <span>
                        {r.running_jobs}/{r.max_concurrency} running
                      </span>
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <Toggle checked={r.is_active} onChange={(on) => void patch(r, { is_active: on })} />
                    <Button size="sm" variant="ghost" onClick={() => void revoke(r)}>
                      Revoke
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Card>
      )}
      <AddRunner open={adding} onClose={() => setAdding(false)} />
    </div>
  );
}

function AddRunner({ open, onClose }: { open: boolean; onClose: () => void }) {
  const toast = useToast();
  const [labels, setLabels] = useState("claude-code");
  const [busy, setBusy] = useState(false);
  const [reg, setReg] = useState<RunnerRegistration | null>(null);

  const close = () => {
    setReg(null);
    onClose();
  };
  const create = async () => {
    setBusy(true);
    try {
      setReg(
        await api.createRunnerRegistration({
          labels: labels
            .split(",")
            .map((l) => l.trim())
            .filter(Boolean),
        }),
      );
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
            help="Comma-separated. A Runner pool connector asks for labels; only runners that have all of them take its work."
          >
            <Input value={labels} onChange={(e) => setLabels(e.target.value)} placeholder="claude-code, mac" />
          </Field>
        </div>
      ) : (
        <div className="space-y-3 text-sm">
          <p>
            Run this on the machine that should do the work (it needs the <code>cueflow</code> binary and the agent
            CLI, e.g. Claude Code). The token works once and expires {fmtRelative(reg.expires_at)}.
          </p>
          <pre className="overflow-x-auto rounded-md bg-zinc-100 p-3 text-xs break-all whitespace-pre-wrap dark:bg-zinc-800">
            {reg.command}
          </pre>
          <p>Then start it with:</p>
          <pre className="rounded-md bg-zinc-100 p-3 text-xs dark:bg-zinc-800">cueflow runner run</pre>
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
