"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { AgentAvatar, defaultTimezone } from "@/components/agents";
import { useServerEvent } from "@/components/providers";
import { Card, CardHeader, EmptyState, ErrorBox, Icon, Loading, PageHeader, Pill, cx } from "@/components/ui";
import { api } from "@/lib/api";
import { useAsync, useDebouncedCallback } from "@/lib/hooks";
import type { Usage, UsageDay } from "@/lib/types";

const PERIODS = [7, 30, 90] as const;

// Series colors (validated with the dataviz palette checker against the card surfaces:
// white in light mode, zinc-900 in dark mode). Identity is never color-alone: legend + tooltip labels.
const INPUT = "bg-[#2a78d6] dark:bg-[#3987e5]";
const OUTPUT = "bg-[#eb6834] dark:bg-[#d95926]";
const SHARE = "bg-[#2a78d6] dark:bg-[#3987e5]";

const compactFmt = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });
const exactFmt = new Intl.NumberFormat("en");

function Num({ value, className }: { value: number; className?: string }) {
  return (
    <span className={cx("tabular-nums", className)} title={exactFmt.format(value)}>
      {compactFmt.format(value)}
    </span>
  );
}

function total(r: { input_tokens: number; output_tokens: number }) {
  return r.input_tokens + r.output_tokens;
}

export default function UsagePage() {
  const [days, setDays] = useState<number>(30);
  // The browser timezone is only known on the client; reading it during render would make the
  // statically exported HTML disagree with the first client render (hydration mismatch).
  const [tz, setTz] = useState<string | null>(null);
  useEffect(() => setTz(defaultTimezone()), []);
  const usage = useAsync(() => (tz ? api.usage({ days, tz }) : new Promise<never>(() => {})), [days, tz]);
  const refresh = useDebouncedCallback(() => void usage.reload(), 1500);
  useServerEvent(["run.updated", "task.updated"], refresh);

  const u = usage.data;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Usage"
        subtitle={`Token usage across agent tasks${tz ? ` · days in ${tz}` : ""}`}
        actions={
          <div role="group" aria-label="Period" className="inline-flex rounded-lg border border-line bg-surface p-0.5">
            {PERIODS.map((p) => (
              <button
                key={p}
                type="button"
                aria-pressed={days === p}
                onClick={() => setDays(p)}
                className={cx(
                  "rounded-md px-3 py-1 text-sm font-medium transition-colors",
                  days === p
                    ? "bg-inverse text-on-inverse"
                    : "text-fg-2 hover:text-fg",
                )}
              >
                {p}d
              </button>
            ))}
          </div>
        }
      />

      {(usage.loading || !tz) && !u ? (
        <Loading />
      ) : usage.error && !u ? (
        <ErrorBox onRetry={() => void usage.reload()}>{usage.error}</ErrorBox>
      ) : !u ? null : u.totals.runs === 0 ? (
        <Card>
          <EmptyState
            title="No runs in this period"
            hint="Token usage appears here once agents work tasks."
          />
        </Card>
      ) : (
        <UsageBody u={u} days={days} />
      )}
    </div>
  );
}

function UsageBody({ u, days }: { u: Usage; days: number }) {
  const t = u.totals;
  const tokens = t.input_tokens + t.output_tokens;
  return (
    <>
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Tile label="Total tokens">
          <Num value={tokens} className="text-2xl font-semibold" />
          <p className="mt-1 text-xs text-fg-3">
            <Num value={t.input_tokens} /> input · <Num value={t.output_tokens} /> output
          </p>
          {t.cache_read_tokens > 0 && (
            <p
              className="text-xs text-fg-3"
              title="Input tokens served from the provider's prompt cache. They are billed at a fraction of the normal input price (Claude: about 0.1×), so a cached run costs far less than its input count suggests."
            >
              <Num value={t.cache_read_tokens} /> of input read from cache
            </p>
          )}
        </Tile>
        <Tile label="Runs">
          <Num value={t.runs} className="text-2xl font-semibold" />
          <p className="mt-1 text-xs text-fg-3">
            {t.failed_runs > 0 ? `${exactFmt.format(t.failed_runs)} failed` : "none failed"}
          </p>
        </Tile>
        <Tile label="Tool calls">
          <Num value={t.tool_calls} className="text-2xl font-semibold" />
          <p className="mt-1 text-xs text-fg-3">
            {t.runs > 0 ? `${(t.tool_calls / t.runs).toFixed(1)} per run` : "—"}
          </p>
        </Tile>
        <Tile label="Avg tokens / run">
          <Num value={t.runs > 0 ? Math.round(tokens / Math.max(1, t.runs - t.unreported_runs)) : 0} className="text-2xl font-semibold" />
          <p className="mt-1 text-xs text-fg-3">last {days} days</p>
        </Tile>
      </div>

      {t.unreported_runs > 0 && (
        <p className="flex items-start gap-2 rounded-lg border border-line bg-sunken px-3 py-2 text-xs text-fg-2">
          <Icon name="warn" className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>
            {exactFmt.format(t.unreported_runs)} of {exactFmt.format(t.runs)} runs did not report token usage (for
            example external ACP agents that don&apos;t return usage), so these totals undercount.
          </span>
        </p>
      )}

      <Card>
        <CardHeader title="Daily tokens" subtitle="Input and output tokens per day" />
        <div className="px-4 pb-4">
          <DailyChart daily={u.daily} />
        </div>
      </Card>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader title="By model / connector" />
          <ShareTable
            rows={u.by_model.map((m, i) => ({
              key: `${m.connector_id ?? "deleted"}-${m.model}-${i}`,
              label: (
                <div className="min-w-0">
                  <div className="flex items-center gap-1.5">
                    <span className={cx("truncate", !m.connector_name && "italic text-fg-3")}>
                      {m.connector_name ||
                        (m.connector_id ? (
                          "(deleted connector)"
                        ) : (
                          <span title="These runs happened before CueFlow started recording which connector and model each run used.">
                            Not recorded
                          </span>
                        ))}
                    </span>
                    <Pill>{m.runtime === "acp" ? "ACP" : "Built-in"}</Pill>
                  </div>
                  {m.model && <div className="truncate font-mono text-[11px] text-fg-3">{m.model}</div>}
                </div>
              ),
              runs: m.runs,
              input: m.input_tokens,
              cached: m.cache_read_tokens,
              output: m.output_tokens,
            }))}
            empty="No model usage recorded."
          />
        </Card>
        <Card>
          <CardHeader title="By agent" />
          <ShareTable
            rows={u.by_agent.map((a) => ({
              key: a.agent_id,
              label: (
                <Link href={`/agents/edit/?id=${a.agent_id}`} className="flex min-w-0 items-center gap-2 hover:text-accent">
                  <AgentAvatar agent={a} size="xs" />
                  <span className="truncate">{a.name}</span>
                </Link>
              ),
              runs: a.runs,
              input: a.input_tokens,
              cached: a.cache_read_tokens,
              output: a.output_tokens,
            }))}
            empty="No agent runs in this period."
          />
        </Card>
      </div>

      <div className="grid gap-6 lg:grid-cols-[1fr_2fr]">
        <div className="space-y-6">
          <Card>
            <CardHeader title="By org" />
            <ShareTable
              rows={(u.by_org ?? []).map((o) => ({
                key: o.org_id,
                label: <span className="truncate">{o.name}</span>,
                runs: o.runs,
                input: o.input_tokens,
                cached: o.cache_read_tokens,
                output: o.output_tokens,
              }))}
              empty="—"
              compact
            />
          </Card>
          <Card>
            <CardHeader title="By source" />
            <ShareTable
              rows={u.by_source.map((s) => ({
                key: s.source,
                label: <span>{s.source === "agent" ? "Agent tasks" : s.source === "pipeline" ? "Simple schedules (retired)" : s.source}</span>,
                runs: s.runs,
                input: s.input_tokens,
                cached: s.cache_read_tokens,
                output: s.output_tokens,
              }))}
              empty="—"
              compact
            />
          </Card>
        </div>
        <Card>
          <CardHeader title="Top tasks" subtitle="Most tokens in this period" />
          {u.top_tasks.length === 0 ? (
            <p className="px-4 pb-4 text-sm text-fg-3">No task runs in this period.</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[360px] table-fixed text-sm">
                <thead>
                  <tr className="border-b border-line text-left text-xs text-fg-3">
                    <th className="px-4 py-2 font-medium">Task</th>
                    <th className="w-16 px-4 py-2 text-right font-medium">Runs</th>
                    <th className="w-20 px-4 py-2 text-right font-medium">Tokens</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {u.top_tasks.map((t) => (
                    <tr key={t.task_id}>
                      <td className="truncate px-4 py-2">
                        <Link href={`/tasks/view/?id=${t.task_id}`} className="flex min-w-0 gap-1.5 hover:text-accent">
                          <span className="shrink-0 text-fg-3">#{t.number}</span>
                          <span className="truncate">{t.title}</span>
                        </Link>
                      </td>
                      <td className="px-4 py-2 text-right tabular-nums text-fg-2">{t.runs}</td>
                      <td className="px-4 py-2 text-right">
                        <Num value={total(t)} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      </div>
    </>
  );
}

function Tile({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <Card className="p-4">
      <p className="text-xs font-medium text-fg-3">{label}</p>
      <div className="mt-1 text-fg">{children}</div>
    </Card>
  );
}

interface ShareRow {
  key: string;
  label: React.ReactNode;
  runs: number;
  input: number;
  /** The part of input read from the prompt cache. */
  cached: number;
  output: number;
}

function ShareTable({ rows, empty, compact }: { rows: ShareRow[]; empty: string; compact?: boolean }) {
  if (rows.length === 0) return <p className="px-4 pb-4 text-sm text-fg-3">{empty}</p>;
  const sum = rows.reduce((acc, r) => acc + r.input + r.output, 0) || 1;
  return (
    <div className="overflow-x-auto">
      <table className={cx("w-full table-fixed text-sm", compact ? "min-w-[300px]" : "min-w-[520px]")}>
        <thead>
          <tr className="border-b border-line text-left text-xs text-fg-3">
            <th className="px-4 py-2 font-medium">Name</th>
            <th className="w-16 px-4 py-2 text-right font-medium">Runs</th>
            {!compact && <th className="w-20 px-4 py-2 text-right font-medium">Input</th>}
            {!compact && <th className="w-20 px-4 py-2 text-right font-medium">Output</th>}
            <th className="w-36 px-4 py-2 font-medium">Share</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {rows.map((r) => {
            const share = (r.input + r.output) / sum;
            return (
              <tr key={r.key} className="align-middle">
                <td className="truncate px-4 py-2">{r.label}</td>
                <td className="px-4 py-2 text-right tabular-nums text-fg-2">{exactFmt.format(r.runs)}</td>
                {!compact && (
                  <td className="px-4 py-2 text-right">
                    <Num value={r.input} />
                    {r.cached > 0 && (
                      <div className="text-[11px] text-fg-3">
                        <Num value={r.cached} /> cached
                      </div>
                    )}
                  </td>
                )}
                {!compact && (
                  <td className="px-4 py-2 text-right">
                    <Num value={r.output} />
                  </td>
                )}
                <td className="px-4 py-2">
                  <div className="flex items-center gap-2">
                    <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-subtle">
                      <div className={cx("h-full rounded-full", SHARE)} style={{ width: `${Math.max(share * 100, share > 0 ? 2 : 0)}%` }} />
                    </div>
                    <span className="w-10 text-right text-xs tabular-nums text-fg-3">{Math.round(share * 100)}%</span>
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// niceMax rounds up to 1/2/2.5/5 × 10^n so gridlines land on readable values.
function niceMax(v: number): number {
  if (v <= 0) return 1;
  const exp = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * exp >= v) return m * exp;
  return 10 * exp;
}

function fmtDay(date: string, opts: Intl.DateTimeFormatOptions): string {
  const [y, m, d] = date.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d)).toLocaleDateString(undefined, { ...opts, timeZone: "UTC" });
}

function DailyChart({ daily }: { daily: UsageDay[] }) {
  const [hover, setHover] = useState<number | null>(null);
  const [showTable, setShowTable] = useState(false);
  const max = niceMax(Math.max(0, ...daily.map(total)));
  const ticks = [0, max / 2, max];
  const labelEvery = Math.max(1, Math.ceil(daily.length / 7));
  const h = hover !== null ? daily[hover] : null;

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2 text-xs text-fg-2">
        <div className="flex items-center gap-4" aria-hidden="true">
          <span className="flex items-center gap-1.5">
            <span className={cx("h-2.5 w-2.5 rounded-sm", INPUT)} /> Input
          </span>
          <span className="flex items-center gap-1.5">
            <span className={cx("h-2.5 w-2.5 rounded-sm", OUTPUT)} /> Output
          </span>
        </div>
        <button
          type="button"
          onClick={() => setShowTable((v) => !v)}
          className="text-fg-3 underline-offset-2 hover:text-fg hover:underline"
        >
          {showTable ? "Show chart" : "Show table"}
        </button>
      </div>

      {showTable ? (
        <div className="max-h-80 overflow-auto">
          <table className="w-full text-sm">
            <thead className="sticky top-0 bg-surface">
              <tr className="border-b border-line text-left text-xs text-fg-3">
                <th className="py-2 font-medium">Date</th>
                <th className="py-2 text-right font-medium">Runs</th>
                <th className="py-2 text-right font-medium">Input</th>
                <th className="py-2 text-right font-medium">Output</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {daily.map((d) => (
                <tr key={d.date}>
                  <td className="py-1.5">{fmtDay(d.date, { year: "numeric", month: "short", day: "numeric" })}</td>
                  <td className="py-1.5 text-right tabular-nums">{exactFmt.format(d.runs)}</td>
                  <td className="py-1.5 text-right tabular-nums">{exactFmt.format(d.input_tokens)}</td>
                  <td className="py-1.5 text-right tabular-nums">{exactFmt.format(d.output_tokens)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="mt-2 flex gap-2">
          {/* y axis */}
          <div className="relative h-48 w-10 shrink-0 text-right text-[10px] tabular-nums text-fg-3">
            {ticks.map((v) => (
              <span key={v} className="absolute right-0 translate-y-1/2" style={{ bottom: `${(v / max) * 100}%` }}>
                {compactFmt.format(v)}
              </span>
            ))}
          </div>
          <div className="relative min-w-0 flex-1">
            {/* gridlines */}
            <div className="pointer-events-none absolute inset-x-0 top-0 h-48">
              {ticks.map((v) => (
                <div
                  key={v}
                  className={cx("absolute inset-x-0 border-t", v === 0 ? "border-line-strong" : "border-dashed border-line")}
                  style={{ bottom: `${(v / max) * 100}%` }}
                />
              ))}
            </div>
            {/* bars */}
            <div
              className={cx("relative flex h-48 items-end", daily.length > 45 ? "gap-px" : "gap-[2px]")}
              onMouseLeave={() => setHover(null)}
              role="list"
              aria-label="Daily token usage"
            >
              {daily.map((d, i) => {
                const inH = (d.input_tokens / max) * 100;
                const outH = (d.output_tokens / max) * 100;
                const label = `${fmtDay(d.date, { month: "short", day: "numeric" })}: ${exactFmt.format(d.input_tokens)} input, ${exactFmt.format(d.output_tokens)} output tokens, ${d.runs} runs`;
                return (
                  <div
                    key={d.date}
                    role="listitem"
                    tabIndex={0}
                    aria-label={label}
                    onMouseEnter={() => setHover(i)}
                    onFocus={() => setHover(i)}
                    onBlur={() => setHover(null)}
                    className={cx(
                      "group flex h-full min-w-0 flex-1 cursor-default flex-col justify-end rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-indigo-500",
                      hover === i && "bg-subtle",
                    )}
                  >
                    {/* output on top of input, 2px surface gap between segments, rounded data end */}
                    {d.output_tokens > 0 && (
                      <div className={cx("w-full rounded-t-[4px]", OUTPUT)} style={{ height: `max(${outH}%, 2px)` }} />
                    )}
                    {d.output_tokens > 0 && d.input_tokens > 0 && <div className="h-[2px] w-full shrink-0" />}
                    {d.input_tokens > 0 && (
                      <div
                        className={cx("w-full", INPUT, d.output_tokens > 0 ? "" : "rounded-t-[4px]")}
                        style={{ height: `max(${inH}%, 2px)` }}
                      />
                    )}
                  </div>
                );
              })}
            </div>
            {/* tooltip */}
            {h && hover !== null && (
              <div
                className="pointer-events-none absolute top-0 z-10 w-44 rounded-lg border border-line bg-surface p-2.5 text-xs shadow-lg"
                style={{
                  left: `clamp(0px, calc(${((hover + 0.5) / daily.length) * 100}% - 88px), calc(100% - 176px))`,
                }}
              >
                <p className="font-medium text-fg">
                  {fmtDay(h.date, { weekday: "short", month: "short", day: "numeric" })}
                </p>
                <dl className="mt-1.5 space-y-1 text-fg-2">
                  <div className="flex items-center justify-between gap-2">
                    <dt className="flex items-center gap-1.5">
                      <span className={cx("h-2 w-2 rounded-sm", INPUT)} /> Input
                    </dt>
                    <dd className="tabular-nums text-fg">{exactFmt.format(h.input_tokens)}</dd>
                  </div>
                  <div className="flex items-center justify-between gap-2">
                    <dt className="flex items-center gap-1.5">
                      <span className={cx("h-2 w-2 rounded-sm", OUTPUT)} /> Output
                    </dt>
                    <dd className="tabular-nums text-fg">{exactFmt.format(h.output_tokens)}</dd>
                  </div>
                  <div className="flex items-center justify-between gap-2 border-t border-line pt-1">
                    <dt>Runs</dt>
                    <dd className="tabular-nums text-fg">{h.runs}</dd>
                  </div>
                </dl>
              </div>
            )}
            {/* x labels */}
            <div className="mt-1.5 flex text-[10px] text-fg-3">
              {daily.map((d, i) => (
                <span key={d.date} className="min-w-0 flex-1 overflow-visible whitespace-nowrap text-center">
                  {i % labelEvery === 0 ? fmtDay(d.date, { month: "numeric", day: "numeric" }) : ""}
                </span>
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
