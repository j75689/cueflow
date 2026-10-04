"use client";

import {
  Children,
  type KeyboardEvent,
  type ReactElement,
  type ReactNode,
  isValidElement,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";

import { cx, inputBase } from "./cx";
import { Popover } from "./popover";

export type SelectOption = { value: string; label: string; disabled?: boolean };

type OptionProps = { value?: string | number; disabled?: boolean; children?: ReactNode };

// textOf flattens option children ("{a.avatar} {a.name}") into plain text.
function textOf(node: ReactNode): string {
  let out = "";
  Children.forEach(node, (c) => {
    if (typeof c === "string" || typeof c === "number") out += String(c);
    else if (isValidElement(c)) out += textOf((c.props as { children?: ReactNode }).children);
  });
  return out;
}

// optionsOf reads the <option> children call sites already pass, so Select stays a drop-in
// replacement for the native element.
function optionsOf(children: ReactNode): SelectOption[] {
  const out: SelectOption[] = [];
  const walk = (node: ReactNode) => {
    Children.forEach(node, (c) => {
      if (!isValidElement(c)) return;
      const el = c as ReactElement<OptionProps>;
      if (el.type === "option") {
        const label = textOf(el.props.children);
        out.push({ value: el.props.value === undefined ? label : String(el.props.value), label, disabled: el.props.disabled });
      } else if ((el.props as { children?: ReactNode }).children) {
        walk((el.props as { children?: ReactNode }).children); // fragments
      }
    });
  };
  walk(children);
  return out;
}

export type SelectProps = {
  value?: string | number | null;
  onChange?: (e: { target: { value: string } }) => void;
  children?: ReactNode;
  className?: string;
  disabled?: boolean;
  id?: string;
  placeholder?: string;
  /** Show a filter box; defaults to on when there are more than 10 options. */
  searchable?: boolean;
  "aria-label"?: string;
};

export function Select({
  value,
  onChange,
  children,
  className,
  disabled,
  id,
  placeholder = "Select…",
  searchable,
  "aria-label": ariaLabel,
}: SelectProps) {
  const options = useMemo(() => optionsOf(children), [children]);
  const current = value === null || value === undefined ? "" : String(value);
  const selected = options.find((o) => o.value === current);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(-1);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const listRef = useRef<HTMLUListElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const typeahead = useRef({ text: "", at: 0 });
  const listId = useId();
  const showSearch = searchable ?? options.length > 10;

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q ? options.filter((o) => o.label.toLowerCase().includes(q) || o.value.toLowerCase().includes(q)) : options;
  }, [options, query]);

  const close = useCallback((refocus = true) => {
    setOpen(false);
    setQuery("");
    if (refocus) triggerRef.current?.focus();
  }, []);

  const openList = () => {
    if (disabled) return;
    setOpen(true);
    const idx = options.findIndex((o) => o.value === current);
    setActive(idx >= 0 ? idx : options.findIndex((o) => !o.disabled));
  };

  // Focus the filter box or the list once the popover has rendered.
  useEffect(() => {
    if (!open) return;
    const t = setTimeout(() => (showSearch ? searchRef.current : listRef.current)?.focus(), 0);
    return () => clearTimeout(t);
  }, [open, showSearch]);

  // Keep the active option in view.
  useEffect(() => {
    if (!open || active < 0) return;
    listRef.current?.querySelector<HTMLElement>(`[data-index="${active}"]`)?.scrollIntoView({ block: "nearest" });
  }, [open, active]);

  // Re-anchor the highlight when filtering changes the list.
  useEffect(() => {
    if (!open) return;
    setActive((a) => (a >= 0 && a < visible.length && !visible[a].disabled ? a : visible.findIndex((o) => !o.disabled)));
  }, [visible, open]);

  const choose = (o: SelectOption | undefined) => {
    if (!o || o.disabled) return;
    if (o.value !== current) onChange?.({ target: { value: o.value } });
    close();
  };

  const move = (delta: number) => {
    if (!visible.length) return;
    let i = active;
    for (let n = 0; n < visible.length; n++) {
      i = (i + delta + visible.length) % visible.length;
      if (!visible[i].disabled) break;
    }
    setActive(i);
  };

  const onListKey = (e: KeyboardEvent) => {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        move(1);
        return;
      case "ArrowUp":
        e.preventDefault();
        move(-1);
        return;
      case "Home":
        e.preventDefault();
        setActive(visible.findIndex((o) => !o.disabled));
        return;
      case "End": {
        e.preventDefault();
        let i = visible.length - 1;
        while (i >= 0 && visible[i].disabled) i--;
        setActive(i);
        return;
      }
      case "Enter":
        e.preventDefault();
        choose(visible[active]);
        return;
      case " ":
        if (e.currentTarget === searchRef.current) return; // let the filter box type spaces
        e.preventDefault();
        choose(visible[active]);
        return;
      case "Escape":
        e.preventDefault();
        close();
        return;
      case "Tab":
        close(false);
        return;
    }
    // Type-ahead when there is no filter box.
    if (!showSearch && e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
      const now = Date.now();
      const ta = typeahead.current;
      ta.text = now - ta.at > 600 ? e.key.toLowerCase() : ta.text + e.key.toLowerCase();
      ta.at = now;
      const i = visible.findIndex((o) => !o.disabled && o.label.toLowerCase().startsWith(ta.text));
      if (i >= 0) setActive(i);
    }
  };

  const onTriggerKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (["ArrowDown", "ArrowUp", "Enter", " "].includes(e.key)) {
      e.preventDefault();
      openList();
    }
  };

  const label = selected ? selected.label : placeholder;
  return (
    <>
      <button
        ref={triggerRef}
        id={id}
        type="button"
        disabled={disabled}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-label={ariaLabel}
        onClick={() => (open ? close() : openList())}
        onKeyDown={onTriggerKey}
        className={cx(
          // A width utility from the call site (e.g. w-10) must win over the base w-full.
          className && /(^|\s)w-/.test(className) ? inputBase.replace("w-full ", "") : inputBase,
          "flex h-9 items-center gap-2 pr-2 text-left disabled:cursor-not-allowed",
          open && "border-indigo-500 ring-2 ring-indigo-500/20",
          className,
        )}
      >
        <span className={cx("min-w-0 flex-1 truncate", !selected && "text-zinc-400 dark:text-zinc-600")}>{label}</span>
        <svg viewBox="0 0 20 20" fill="currentColor" aria-hidden className={cx("h-4 w-4 shrink-0 text-zinc-400 transition-transform", open && "rotate-180")}>
          <path fillRule="evenodd" d="M5.23 7.21a.75.75 0 0 1 1.06.02L10 11.17l3.71-3.94a.75.75 0 1 1 1.08 1.04l-4.25 4.5a.75.75 0 0 1-1.08 0l-4.25-4.5a.75.75 0 0 1 .02-1.06Z" clipRule="evenodd" />
        </svg>
      </button>
      <Popover open={open} anchorRef={triggerRef} onClose={() => close(false)} minWidth={220}>
        {(maxHeight) => (
          <div className="flex flex-col" style={{ maxHeight }}>
            {showSearch && (
              <div className="border-b border-zinc-100 p-1.5 dark:border-zinc-800">
                <input
                  ref={searchRef}
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  onKeyDown={onListKey}
                  placeholder="Search…"
                  aria-label="Filter options"
                  aria-controls={listId}
                  aria-activedescendant={active >= 0 ? `${listId}-${active}` : undefined}
                  className="h-8 w-full rounded-md border border-zinc-200 bg-zinc-50 px-2 text-sm text-zinc-900 placeholder:text-zinc-400 focus:border-indigo-500 focus:outline-none dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100"
                />
              </div>
            )}
            <ul
              ref={listRef}
              id={listId}
              role="listbox"
              tabIndex={-1}
              aria-label={ariaLabel}
              aria-activedescendant={active >= 0 ? `${listId}-${active}` : undefined}
              onKeyDown={onListKey}
              className="min-h-0 flex-1 overflow-y-auto p-1 outline-none"
            >
              {visible.length === 0 && <li className="px-2.5 py-2 text-sm text-zinc-500">No matches</li>}
              {visible.map((o, i) => {
                const isSel = o.value === current;
                return (
                  <li
                    key={`${o.value}-${i}`}
                    id={`${listId}-${i}`}
                    data-index={i}
                    role="option"
                    aria-selected={isSel}
                    aria-disabled={o.disabled || undefined}
                    onPointerEnter={() => !o.disabled && setActive(i)}
                    onClick={() => choose(o)}
                    className={cx(
                      "flex cursor-pointer items-center gap-2 rounded-md px-2.5 py-1.5 text-sm",
                      i === active && !o.disabled && "bg-indigo-50 text-indigo-900 dark:bg-indigo-500/15 dark:text-indigo-100",
                      i !== active && "text-zinc-800 dark:text-zinc-200",
                      o.disabled && "cursor-not-allowed opacity-40",
                    )}
                  >
                    <span className="min-w-0 flex-1 truncate">{o.label || " "}</span>
                    {isSel && (
                      <svg viewBox="0 0 20 20" fill="currentColor" aria-hidden className="h-4 w-4 shrink-0 text-indigo-600 dark:text-indigo-400">
                        <path fillRule="evenodd" d="M16.7 5.3a1 1 0 0 1 0 1.4l-7.5 7.5a1 1 0 0 1-1.4 0l-3.5-3.5a1 1 0 1 1 1.4-1.4l2.8 2.8 6.8-6.8a1 1 0 0 1 1.4 0Z" clipRule="evenodd" />
                      </svg>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        )}
      </Popover>
    </>
  );
}
