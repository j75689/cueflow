"use client";

import { type ReactNode, type RefObject, useEffect, useLayoutEffect, useState } from "react";
import { createPortal } from "react-dom";

import { cx } from "./cx";

type Placement = { top: number; left: number; width: number; maxHeight: number; up: boolean };

const GAP = 4;
const MARGIN = 8;

// computePlacement anchors a popover under (or, near the viewport bottom, above) its trigger and
// keeps it inside the viewport horizontally.
function computePlacement(anchor: DOMRect, minWidth: number, preferredHeight: number): Placement {
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  const width = Math.min(Math.max(anchor.width, minWidth), vw - MARGIN * 2);
  let left = anchor.left;
  if (left + width > vw - MARGIN) left = Math.max(MARGIN, anchor.right - width);
  if (left + width > vw - MARGIN) left = vw - MARGIN - width;
  const below = vh - anchor.bottom - GAP - MARGIN;
  const above = anchor.top - GAP - MARGIN;
  // Open upward when the content does not fit below but there is more room above.
  const up = below < preferredHeight && above > below;
  const maxHeight = Math.max(120, Math.min(preferredHeight, up ? above : below));
  const top = up ? anchor.top - GAP : anchor.bottom + GAP;
  return { top, left, width, maxHeight, up };
}

export function Popover({
  open,
  anchorRef,
  onClose,
  minWidth = 200,
  preferredHeight = 320,
  className,
  children,
  label,
}: {
  open: boolean;
  anchorRef: RefObject<HTMLElement | null>;
  onClose: () => void;
  minWidth?: number;
  preferredHeight?: number;
  className?: string;
  children: (maxHeight: number) => ReactNode;
  label?: string;
}) {
  const [place, setPlace] = useState<Placement | null>(null);

  useLayoutEffect(() => {
    if (!open) {
      setPlace(null);
      return;
    }
    const update = () => {
      const el = anchorRef.current;
      if (el) setPlace(computePlacement(el.getBoundingClientRect(), minWidth, preferredHeight));
    };
    update();
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    return () => {
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
    };
  }, [open, anchorRef, minWidth, preferredHeight]);

  // Close on a press outside the trigger and outside every open popover (popovers can nest, e.g.
  // the hour picker inside the date picker).
  useEffect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent) => {
      const t = e.target as Node | null;
      if (!t) return;
      if (anchorRef.current?.contains(t)) return;
      if (t instanceof Element && t.closest("[data-cf-popover]")) return;
      onClose();
    };
    document.addEventListener("pointerdown", onDown, true);
    return () => document.removeEventListener("pointerdown", onDown, true);
  }, [open, anchorRef, onClose]);

  if (!open || !place || typeof document === "undefined") return null;
  return createPortal(
    <div
      data-cf-popover=""
      aria-label={label}
      style={{
        position: "fixed",
        left: place.left,
        width: place.width,
        ...(place.up ? { bottom: window.innerHeight - place.top } : { top: place.top }),
        zIndex: 60,
      }}
      className={cx(
        "overflow-hidden rounded-lg border border-zinc-200 bg-white shadow-lg ring-1 ring-black/5",
        "dark:border-zinc-700 dark:bg-zinc-900 dark:ring-white/5",
        className,
      )}
    >
      {children(place.maxHeight)}
    </div>,
    document.body,
  );
}
