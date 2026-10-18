"use client";

import { useEffect, useState } from "react";
import { Button, Icon, Input, Textarea, cx } from "./ui";

// TagInput edits a list of short tokens (hashtags, queries) as chips.
export function TagInput({
  value,
  onChange,
  placeholder,
  prefix,
  disabled,
}: {
  value: string[];
  onChange: (v: string[]) => void;
  placeholder?: string;
  prefix?: string;
  disabled?: boolean;
}) {
  const [draft, setDraft] = useState("");
  const add = (raw: string) => {
    const parts = raw
      .split(/[,\n]/)
      .map((s) => s.trim())
      .map((s) => (prefix && s.startsWith(prefix) ? s.slice(prefix.length) : s))
      .filter(Boolean)
      .filter((s) => !value.includes(s));
    if (parts.length) onChange([...value, ...parts]);
    setDraft("");
  };
  return (
    <div
      className={cx(
        "flex min-h-9 flex-wrap items-center gap-1.5 rounded-md border border-line-strong bg-surface px-2 py-1.5",
        disabled && "opacity-60",
      )}
    >
      {value.map((t) => (
        <span
          key={t}
          className="inline-flex items-center gap-1 rounded bg-subtle px-1.5 py-0.5 text-xs text-fg-2"
        >
          {prefix}
          {t}
          {!disabled && (
            <button
              type="button"
              aria-label={`Remove ${t}`}
              className="text-fg-4 hover:text-fg"
              onClick={() => onChange(value.filter((x) => x !== t))}
            >
              <Icon name="x" className="h-3 w-3" />
            </button>
          )}
        </span>
      ))}
      <input
        disabled={disabled}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === ",") {
            e.preventDefault();
            add(draft);
          } else if (e.key === "Backspace" && !draft && value.length) {
            onChange(value.slice(0, -1));
          }
        }}
        onBlur={() => draft && add(draft)}
        placeholder={value.length ? "" : placeholder}
        className="min-w-[8rem] flex-1 bg-transparent text-sm outline-none placeholder:text-fg-4"
      />
    </div>
  );
}

// StringListInput edits a list of longer strings (URLs) one row each.
export function StringListInput({
  value,
  onChange,
  placeholder,
  disabled,
}: {
  value: string[];
  onChange: (v: string[]) => void;
  placeholder?: string;
  disabled?: boolean;
}) {
  return (
    <div className="space-y-1.5">
      {value.map((v, i) => (
        <div key={i} className="flex gap-1.5">
          <Input
            value={v}
            disabled={disabled}
            placeholder={placeholder}
            onChange={(e) => onChange(value.map((x, j) => (j === i ? e.target.value : x)))}
          />
          <Button
            variant="ghost"
            disabled={disabled}
            aria-label="Remove"
            onClick={() => onChange(value.filter((_, j) => j !== i))}
          >
            <Icon name="trash" />
          </Button>
        </div>
      ))}
      <Button size="sm" variant="ghost" disabled={disabled} onClick={() => onChange([...value, ""])}>
        <Icon name="plus" className="h-3.5 w-3.5" /> Add
      </Button>
    </div>
  );
}

// KVInput edits a string→string map.
export function KVInput({
  value,
  onChange,
  disabled,
  keyPlaceholder = "key",
  valuePlaceholder = "value",
}: {
  value: Record<string, string>;
  onChange: (v: Record<string, string>) => void;
  disabled?: boolean;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
}) {
  // Keep rows locally so empty/duplicate keys don't vanish while typing.
  const [rows, setRows] = useState<[string, string][]>(() => Object.entries(value ?? {}));
  useEffect(() => {
    const cur = Object.fromEntries(rows.filter(([k]) => k));
    if (JSON.stringify(cur) !== JSON.stringify(value ?? {})) setRows(Object.entries(value ?? {}));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);
  const commit = (next: [string, string][]) => {
    setRows(next);
    onChange(Object.fromEntries(next.filter(([k]) => k.trim()).map(([k, v]) => [k.trim(), v])));
  };
  return (
    <div className="space-y-1.5">
      {rows.map(([k, v], i) => (
        <div key={i} className="flex gap-1.5">
          <Input
            value={k}
            disabled={disabled}
            placeholder={keyPlaceholder}
            className="font-mono text-xs"
            onChange={(e) => commit(rows.map((r, j) => (j === i ? [e.target.value, r[1]] : r)))}
          />
          <Input
            value={v}
            disabled={disabled}
            placeholder={valuePlaceholder}
            className="font-mono text-xs"
            onChange={(e) => commit(rows.map((r, j) => (j === i ? [r[0], e.target.value] : r)))}
          />
          <Button
            variant="ghost"
            disabled={disabled}
            aria-label="Remove"
            onClick={() => commit(rows.filter((_, j) => j !== i))}
          >
            <Icon name="trash" />
          </Button>
        </div>
      ))}
      <Button size="sm" variant="ghost" disabled={disabled} onClick={() => setRows([...rows, ["", ""]])}>
        <Icon name="plus" className="h-3.5 w-3.5" /> Add entry
      </Button>
    </div>
  );
}

// JsonInput edits arbitrary JSON; reports parse errors inline and only emits valid values.
export function JsonInput({
  value,
  onChange,
  disabled,
  placeholder,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
  disabled?: boolean;
  placeholder?: string;
}) {
  const [text, setText] = useState(() => (value === undefined || value === null ? "" : JSON.stringify(value, null, 2)));
  const [err, setErr] = useState<string | null>(null);
  return (
    <div>
      <Textarea
        value={text}
        disabled={disabled}
        placeholder={placeholder ?? "{ }"}
        className="font-mono text-xs"
        rows={5}
        onChange={(e) => {
          const t = e.target.value;
          setText(t);
          if (!t.trim()) {
            setErr(null);
            onChange(null);
            return;
          }
          try {
            onChange(JSON.parse(t));
            setErr(null);
          } catch (ex) {
            setErr(ex instanceof Error ? ex.message : "invalid JSON");
          }
        }}
      />
      {err && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{err}</p>}
    </div>
  );
}
