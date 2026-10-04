"use client";

import type { FieldSpec } from "@/lib/types";
import { JsonInput, KVInput, StringListInput } from "./inputs";
import { Button, Input, Select, Textarea, Toggle } from "./ui";

export type ConfigValues = Record<string, unknown>;

// initialValues fills defaults for a new connector, or merges stored non-secret config when editing.
export function initialValues(fields: FieldSpec[], stored?: Record<string, unknown>): ConfigValues {
  const out: ConfigValues = {};
  for (const f of fields) {
    if (f.secret) {
      out[f.key] = ""; // blank = keep stored value
      continue;
    }
    if (stored && f.key in stored) out[f.key] = stored[f.key];
    else if (f.default !== undefined && f.default !== null) out[f.key] = f.default;
    else if (f.type === "bool") out[f.key] = false;
    else if (f.type === "string_list") out[f.key] = [];
    else if (f.type === "kv") out[f.key] = {};
    else if (f.type === "json") out[f.key] = null;
    else out[f.key] = "";
  }
  return out;
}

// buildPayload converts form values to the API config object.
// Secrets left blank are omitted (keep); secrets marked for clearing are sent as null.
export function buildPayload(
  fields: FieldSpec[],
  values: ConfigValues,
  cleared: Set<string>,
  isNew: boolean,
): ConfigValues {
  const out: ConfigValues = {};
  for (const f of fields) {
    const v = values[f.key];
    if (f.secret) {
      if (cleared.has(f.key)) out[f.key] = null;
      else if (typeof v === "string" ? v !== "" : v !== undefined && v !== null) out[f.key] = v;
      continue;
    }
    if (f.type === "number") {
      if (v === "" || v === undefined || v === null) {
        if (!isNew) out[f.key] = null;
        continue;
      }
      out[f.key] = Number(v);
      continue;
    }
    if (f.type === "string_list") {
      out[f.key] = ((v as string[]) ?? []).map((s) => s.trim()).filter(Boolean);
      continue;
    }
    out[f.key] = v;
  }
  return out;
}

export function missingRequired(
  fields: FieldSpec[],
  values: ConfigValues,
  secretsSet: string[],
  cleared: Set<string>,
): string[] {
  return fields
    .filter((f) => f.required)
    .filter((f) => {
      const v = values[f.key];
      if (f.secret && secretsSet.includes(f.key) && !cleared.has(f.key)) return false;
      if (f.type === "bool") return false;
      if (f.type === "string_list") return !((v as string[]) ?? []).some((s) => s.trim());
      if (f.type === "kv") return !v || Object.keys(v as object).length === 0;
      return v === undefined || v === null || v === "";
    })
    .map((f) => f.label);
}

export function ConnectorFields({
  fields,
  values,
  onChange,
  secretsSet,
  cleared,
  onToggleClear,
}: {
  fields: FieldSpec[];
  values: ConfigValues;
  onChange: (v: ConfigValues) => void;
  secretsSet: string[];
  cleared: Set<string>;
  onToggleClear: (key: string) => void;
}) {
  const set = (k: string, v: unknown) => onChange({ ...values, [k]: v });

  return (
    <div className="grid gap-4 md:grid-cols-2">
      {fields.map((f) => {
        const v = values[f.key];
        const wide = ["text", "string_list", "kv", "json"].includes(f.type);
        const stored = f.secret && secretsSet.includes(f.key);
        let input: React.ReactNode;

        if (f.secret) {
          const isClear = cleared.has(f.key);
          input = (
            <div className="flex gap-1.5">
              {f.type === "text" || f.type === "json" ? (
                <Textarea
                  className="font-mono text-xs"
                  value={(v as string) ?? ""}
                  disabled={isClear}
                  placeholder={isClear ? "Will be cleared on save" : stored ? "•••• stored — leave blank to keep" : (f.placeholder ?? "")}
                  onChange={(e) => set(f.key, e.target.value)}
                />
              ) : (
                <Input
                  type="password"
                  autoComplete="new-password"
                  value={(v as string) ?? ""}
                  disabled={isClear}
                  placeholder={isClear ? "Will be cleared on save" : stored ? "•••• stored — leave blank to keep" : (f.placeholder ?? "")}
                  onChange={(e) => set(f.key, e.target.value)}
                />
              )}
              {stored && (
                <Button size="md" variant="ghost" onClick={() => onToggleClear(f.key)}>
                  {isClear ? "Undo" : "Clear"}
                </Button>
              )}
            </div>
          );
        } else {
          switch (f.type) {
            case "text":
              input = (
                <Textarea
                  rows={4}
                  value={(v as string) ?? ""}
                  placeholder={f.placeholder ?? ""}
                  onChange={(e) => set(f.key, e.target.value)}
                />
              );
              break;
            case "number":
              input = (
                <Input
                  type="number"
                  value={v === null || v === undefined ? "" : String(v)}
                  placeholder={f.placeholder ?? ""}
                  onChange={(e) => set(f.key, e.target.value)}
                />
              );
              break;
            case "bool":
              input = (
                <div className="pt-1">
                  <Toggle checked={!!v} onChange={(b) => set(f.key, b)} label={v ? "Enabled" : "Disabled"} />
                </div>
              );
              break;
            case "select":
              input = (
                <Select value={(v as string) ?? ""} onChange={(e) => set(f.key, e.target.value)}>
                  {!f.required && <option value="">—</option>}
                  {(f.options ?? []).map((o) => (
                    <option key={o} value={o}>
                      {o}
                    </option>
                  ))}
                </Select>
              );
              break;
            case "string_list":
              input = (
                <StringListInput
                  value={(v as string[]) ?? []}
                  placeholder={f.placeholder ?? ""}
                  onChange={(x) => set(f.key, x)}
                />
              );
              break;
            case "kv":
              input = <KVInput value={(v as Record<string, string>) ?? {}} onChange={(x) => set(f.key, x)} />;
              break;
            case "json":
              input = <JsonInput value={v} placeholder={f.placeholder ?? undefined} onChange={(x) => set(f.key, x)} />;
              break;
            case "password":
              input = (
                <Input
                  type="password"
                  value={(v as string) ?? ""}
                  placeholder={f.placeholder ?? ""}
                  onChange={(e) => set(f.key, e.target.value)}
                />
              );
              break;
            default:
              input = (
                <Input
                  value={(v as string) ?? ""}
                  placeholder={f.placeholder ?? ""}
                  onChange={(e) => set(f.key, e.target.value)}
                />
              );
          }
        }

        // Toggle/list widgets contain their own buttons; avoid wrapping them in <label>.
        const Wrapper = f.type === "bool" || f.type === "string_list" || f.type === "kv" || (f.secret && stored) ? "div" : "label";
        return (
          <Wrapper key={f.key} className={wide ? "block md:col-span-2" : "block"}>
            <FieldLabel f={f} />
            {input}
            {f.help && <span className="mt-1 block text-xs text-zinc-500">{f.help}</span>}
          </Wrapper>
        );
      })}
    </div>
  );
}

function FieldLabel({ f }: { f: FieldSpec }) {
  return (
    <span className="mb-1 flex items-center gap-1.5 text-xs font-medium text-zinc-700 dark:text-zinc-300">
      {f.label}
      {f.required && <span className="text-red-500">*</span>}
      {f.secret && (
        <span className="rounded bg-zinc-100 px-1 py-px text-[10px] font-normal text-zinc-500 dark:bg-zinc-800">
          encrypted
        </span>
      )}
    </span>
  );
}

