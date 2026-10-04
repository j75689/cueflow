"use client";

import { type KeyboardEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { cx, inputBase } from "./cx";
import { Popover } from "./popover";
import { Select } from "./select";

const pad = (n: number) => String(n).padStart(2, "0");
const WEEKDAYS = ["Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"];
const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];

// Values use the same local "YYYY-MM-DDTHH:mm" (or "YYYY-MM-DD" with dateOnly) strings the native
// inputs produced, so call sites keep converting with toLocalInput/fromLocalInput.
function parse(v: string): Date | null {
  if (!v) return null;
  const m = /^(\d{4})-(\d{2})-(\d{2})(?:T(\d{2}):(\d{2}))?/.exec(v);
  if (!m) return null;
  const d = new Date(+m[1], +m[2] - 1, +m[3], m[4] ? +m[4] : 9, m[5] ? +m[5] : 0);
  return Number.isNaN(d.getTime()) ? null : d;
}

function format(d: Date, dateOnly: boolean): string {
  const day = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return dateOnly ? day : `${day}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

const sameDay = (a: Date, b: Date) =>
  a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();

function addDays(d: Date, n: number): Date {
  const x = new Date(d);
  x.setDate(x.getDate() + n);
  return x;
}

function addMonths(d: Date, n: number): Date {
  const x = new Date(d.getFullYear(), d.getMonth() + n, 1, d.getHours(), d.getMinutes());
  const last = new Date(x.getFullYear(), x.getMonth() + 1, 0).getDate();
  x.setDate(Math.min(d.getDate(), last));
  return x;
}

export function DateTimePicker({
  value,
  onChange,
  disabled,
  dateOnly = false,
  placeholder,
  className,
  "aria-label": ariaLabel,
}: {
  value: string;
  onChange: (v: string) => void;
  disabled?: boolean;
  dateOnly?: boolean;
  placeholder?: string;
  className?: string;
  "aria-label"?: string;
}) {
  const triggerRef = useRef<HTMLButtonElement>(null);
  const gridRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<Date | null>(null);
  const [focus, setFocus] = useState<Date>(() => new Date());
  const [dirty, setDirty] = useState(false);
  const [timeText, setTimeText] = useState("");

  const current = parse(value);
  const display = current
    ? `${current.getFullYear()}-${pad(current.getMonth() + 1)}-${pad(current.getDate())}` +
      (dateOnly ? "" : ` ${pad(current.getHours())}:${pad(current.getMinutes())}`)
    : "";

  const openPicker = () => {
    if (disabled) return;
    const start = parse(value);
    setDraft(start);
    setFocus(start ?? new Date());
    setTimeText(start ? `${pad(start.getHours())}:${pad(start.getMinutes())}` : "");
    setDirty(false);
    setOpen(true);
  };

  const commit = useCallback(
    (d: Date | null) => {
      onChange(d ? format(d, dateOnly) : "");
      setOpen(false);
      triggerRef.current?.focus();
    },
    [onChange, dateOnly],
  );

  // Clicking outside keeps whatever was picked; Esc discards.
  const closeOutside = useCallback(() => {
    if (dirty) onChange(draft ? format(draft, dateOnly) : "");
    setOpen(false);
  }, [dirty, draft, dateOnly, onChange]);

  const cancel = () => {
    setOpen(false);
    triggerRef.current?.focus();
  };

  useEffect(() => {
    if (!open) return;
    const t = setTimeout(() => gridRef.current?.querySelector<HTMLElement>('[tabindex="0"]')?.focus(), 0);
    return () => clearTimeout(t);
  }, [open]);

  const pickDay = (day: Date) => {
    const base = draft ?? new Date(day.getFullYear(), day.getMonth(), day.getDate(), 9, 0);
    const next = new Date(day.getFullYear(), day.getMonth(), day.getDate(), base.getHours(), base.getMinutes());
    setFocus(next);
    if (dateOnly) {
      commit(next);
      return;
    }
    setDraft(next);
    setTimeText(`${pad(next.getHours())}:${pad(next.getMinutes())}`);
    setDirty(true);
  };

  const setTime = (h: number, m: number) => {
    const base = draft ?? new Date(focus.getFullYear(), focus.getMonth(), focus.getDate());
    const next = new Date(base.getFullYear(), base.getMonth(), base.getDate(), h, m);
    setDraft(next);
    setTimeText(`${pad(h)}:${pad(m)}`);
    setDirty(true);
  };

  const applyTimeText = () => {
    const m = /^(\d{1,2}):?(\d{2})$/.exec(timeText.trim());
    if (m && +m[1] < 24 && +m[2] < 60) setTime(+m[1], +m[2]);
    else if (draft) setTimeText(`${pad(draft.getHours())}:${pad(draft.getMinutes())}`);
  };

  // 6 weeks starting on the Sunday on/before the 1st of the focused month.
  const days = useMemo(() => {
    const first = new Date(focus.getFullYear(), focus.getMonth(), 1);
    const start = addDays(first, -first.getDay());
    return Array.from({ length: 42 }, (_, i) => addDays(start, i));
  }, [focus]);

  const onGridKey = (e: KeyboardEvent) => {
    const moves: Record<string, () => Date> = {
      ArrowLeft: () => addDays(focus, -1),
      ArrowRight: () => addDays(focus, 1),
      ArrowUp: () => addDays(focus, -7),
      ArrowDown: () => addDays(focus, 7),
      PageUp: () => addMonths(focus, -1),
      PageDown: () => addMonths(focus, 1),
      Home: () => addDays(focus, -focus.getDay()),
      End: () => addDays(focus, 6 - focus.getDay()),
    };
    if (moves[e.key]) {
      e.preventDefault();
      setFocus(moves[e.key]());
      setTimeout(() => gridRef.current?.querySelector<HTMLElement>('[tabindex="0"]')?.focus(), 0);
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      pickDay(focus);
    } else if (e.key === "Escape") {
      e.preventDefault();
      cancel();
    }
  };

  const today = new Date();
  const hours = Array.from({ length: 24 }, (_, i) => i);
  const minutes = useMemo(() => {
    const base = Array.from({ length: 12 }, (_, i) => i * 5);
    const m = draft?.getMinutes();
    return m !== undefined && !base.includes(m) ? [...base, m].sort((a, b) => a - b) : base;
  }, [draft]);

  return (
    <div className={cx("relative", className)}>
      <button
        ref={triggerRef}
        type="button"
        disabled={disabled}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={ariaLabel}
        onClick={() => (open ? cancel() : openPicker())}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" || e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            openPicker();
          }
        }}
        className={cx(
          inputBase,
          "flex h-9 items-center gap-2 text-left disabled:cursor-not-allowed",
          current && !disabled && "pr-8",
          open && "border-indigo-500 ring-2 ring-indigo-500/20",
        )}
      >
        <svg viewBox="0 0 20 20" fill="currentColor" aria-hidden className="h-4 w-4 shrink-0 text-zinc-400">
          <path fillRule="evenodd" d="M5.75 2a.75.75 0 0 1 .75.75V4h7V2.75a.75.75 0 0 1 1.5 0V4h.25A2.75 2.75 0 0 1 18 6.75v8.5A2.75 2.75 0 0 1 15.25 18H4.75A2.75 2.75 0 0 1 2 15.25v-8.5A2.75 2.75 0 0 1 4.75 4H5V2.75A.75.75 0 0 1 5.75 2Zm-1 5.5c-.69 0-1.25.56-1.25 1.25v6.5c0 .69.56 1.25 1.25 1.25h10.5c.69 0 1.25-.56 1.25-1.25v-6.5c0-.69-.56-1.25-1.25-1.25H4.75Z" clipRule="evenodd" />
        </svg>
        <span className={cx("min-w-0 flex-1 truncate tabular-nums", !display && "text-zinc-400 dark:text-zinc-600")}>
          {display || placeholder || (dateOnly ? "Pick a date" : "Pick a date and time")}
        </span>
      </button>
      {current && !disabled && (
        <button
          type="button"
          aria-label="Clear"
          onClick={() => onChange("")}
          className="absolute top-1/2 right-1.5 flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
        >
          <svg viewBox="0 0 20 20" fill="currentColor" aria-hidden className="h-3.5 w-3.5">
            <path d="M6.28 5.22a.75.75 0 0 0-1.06 1.06L8.94 10l-3.72 3.72a.75.75 0 1 0 1.06 1.06L10 11.06l3.72 3.72a.75.75 0 1 0 1.06-1.06L11.06 10l3.72-3.72a.75.75 0 0 0-1.06-1.06L10 8.94 6.28 5.22Z" />
          </svg>
        </button>
      )}
      <Popover
        open={open}
        anchorRef={triggerRef}
        onClose={closeOutside}
        minWidth={288}
        preferredHeight={dateOnly ? 330 : 400}
        label="Choose date"
      >
        {(maxHeight) => (
          <div role="dialog" aria-label={ariaLabel ?? "Choose date"} className="overflow-y-auto p-3" style={{ maxHeight }}>
            <div className="mb-2 flex items-center justify-between">
              <button
                type="button"
                aria-label="Previous month"
                onClick={() => setFocus(addMonths(focus, -1))}
                className="flex h-7 w-7 items-center justify-center rounded-md text-zinc-500 hover:bg-zinc-100 hover:text-zinc-800 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
              >
                ‹
              </button>
              <div className="text-sm font-semibold text-zinc-900 dark:text-zinc-100" aria-live="polite">
                {MONTHS[focus.getMonth()]} {focus.getFullYear()}
              </div>
              <button
                type="button"
                aria-label="Next month"
                onClick={() => setFocus(addMonths(focus, 1))}
                className="flex h-7 w-7 items-center justify-center rounded-md text-zinc-500 hover:bg-zinc-100 hover:text-zinc-800 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
              >
                ›
              </button>
            </div>
            <div className="grid grid-cols-7 gap-0.5 text-center text-[11px] font-medium text-zinc-400" aria-hidden>
              {WEEKDAYS.map((w) => (
                <div key={w} className="py-1">
                  {w}
                </div>
              ))}
            </div>
            <div ref={gridRef} role="grid" aria-label={`${MONTHS[focus.getMonth()]} ${focus.getFullYear()}`} onKeyDown={onGridKey} className="grid grid-cols-7 gap-0.5">
              {days.map((d) => {
                const inMonth = d.getMonth() === focus.getMonth();
                const isSel = !!draft && sameDay(d, draft);
                const isToday = sameDay(d, today);
                const isFocus = sameDay(d, focus);
                return (
                  <button
                    key={d.toISOString()}
                    type="button"
                    role="gridcell"
                    tabIndex={isFocus ? 0 : -1}
                    aria-selected={isSel}
                    aria-label={`${MONTHS[d.getMonth()]} ${d.getDate()}, ${d.getFullYear()}`}
                    onClick={() => pickDay(d)}
                    className={cx(
                      "h-8 rounded-md text-sm tabular-nums transition-colors outline-none focus-visible:ring-2 focus-visible:ring-indigo-500",
                      isSel
                        ? "bg-indigo-600 font-semibold text-white hover:bg-indigo-500 dark:bg-indigo-500"
                        : inMonth
                          ? "text-zinc-800 hover:bg-zinc-100 dark:text-zinc-200 dark:hover:bg-zinc-800"
                          : "text-zinc-300 hover:bg-zinc-100 dark:text-zinc-600 dark:hover:bg-zinc-800",
                      isToday && !isSel && "font-semibold text-indigo-600 ring-1 ring-indigo-200 ring-inset dark:text-indigo-400 dark:ring-indigo-500/40",
                    )}
                  >
                    {d.getDate()}
                  </button>
                );
              })}
            </div>
            {!dateOnly && (
              <div className="mt-3 flex items-center gap-1.5 border-t border-zinc-100 pt-3 dark:border-zinc-800">
                <span className="mr-1 text-xs font-medium text-zinc-500">Time</span>
                <Select
                  aria-label="Hour"
                  className="h-8 w-[4.5rem] py-1"
                  searchable={false}
                  value={draft ? String(draft.getHours()) : ""}
                  placeholder="HH"
                  onChange={(e) => setTime(+e.target.value, draft?.getMinutes() ?? 0)}
                >
                  {hours.map((h) => (
                    <option key={h} value={String(h)}>
                      {pad(h)}
                    </option>
                  ))}
                </Select>
                <span className="text-zinc-400">:</span>
                <Select
                  aria-label="Minute"
                  className="h-8 w-[4.5rem] py-1"
                  searchable={false}
                  value={draft ? String(draft.getMinutes()) : ""}
                  placeholder="MM"
                  onChange={(e) => setTime(draft?.getHours() ?? 9, +e.target.value)}
                >
                  {minutes.map((m) => (
                    <option key={m} value={String(m)}>
                      {pad(m)}
                    </option>
                  ))}
                </Select>
                <input
                  aria-label="Time (HH:MM)"
                  value={timeText}
                  onChange={(e) => setTimeText(e.target.value)}
                  onBlur={applyTimeText}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      applyTimeText();
                    }
                  }}
                  placeholder="HH:MM"
                  className="ml-auto h-8 w-[4.5rem] rounded-md border border-zinc-200 bg-zinc-50 px-2 text-center text-sm tabular-nums text-zinc-900 focus:border-indigo-500 focus:outline-none dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100"
                />
              </div>
            )}
            <div className="mt-3 flex items-center gap-2">
              <button
                type="button"
                onClick={() => commit(null)}
                className="rounded-md px-2 py-1 text-xs font-medium text-zinc-500 hover:bg-zinc-100 hover:text-zinc-800 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
              >
                Clear
              </button>
              <button
                type="button"
                onClick={() => {
                  const n = new Date();
                  const base = draft ?? new Date(n.getFullYear(), n.getMonth(), n.getDate(), 9, 0);
                  pickDay(new Date(n.getFullYear(), n.getMonth(), n.getDate(), base.getHours(), base.getMinutes()));
                }}
                className="rounded-md px-2 py-1 text-xs font-medium text-indigo-600 hover:bg-indigo-50 dark:text-indigo-400 dark:hover:bg-indigo-500/10"
              >
                Today
              </button>
              <button
                type="button"
                onClick={() => commit(draft)}
                className="ml-auto rounded-md bg-indigo-600 px-3 py-1 text-xs font-medium text-white hover:bg-indigo-500 dark:bg-indigo-500 dark:hover:bg-indigo-400"
              >
                Done
              </button>
            </div>
          </div>
        )}
      </Popover>
    </div>
  );
}
