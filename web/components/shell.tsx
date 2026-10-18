"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { type ReactNode, useEffect, useState } from "react";
import { setToken } from "@/lib/api";
import { OrgSwitcher, useOrg } from "./org";
import { useMeta, useServerEvents } from "./providers";
import { Icon, Loading, cx } from "./ui";

interface NavItem {
  href: string;
  label: string;
  icon: string;
  match: (p: string) => boolean;
  /** Shown only when this feature module is mounted on the server (see useModule). */
  module?: string;
}

// Pages that belong to the current org.
const ORG_NAV: NavItem[] = [
  { href: "/command/", label: "Command", icon: "sparkle", match: (p: string) => p.startsWith("/command") },
  { href: "/board/", label: "Board", icon: "kanban", match: (p: string) => p.startsWith("/board") || p.startsWith("/tasks") },
  { href: "/agents/", label: "Agents", icon: "users", match: (p: string) => p.startsWith("/agents") },
  { href: "/deliverables/", label: "Deliverables", icon: "list", match: (p: string) => p.startsWith("/deliverables"), module: "deliverables" },
  { href: "/schedules/", label: "Schedules", icon: "clock", match: (p: string) => p.startsWith("/schedules") },
];

// Pages shared by every org.
const GLOBAL_NAV: NavItem[] = [
  { href: "/usage/", label: "Usage", icon: "chart", match: (p: string) => p.startsWith("/usage") },
  { href: "/connectors/", label: "Connectors", icon: "plug", match: (p: string) => p.startsWith("/connectors") },
  { href: "/runners/", label: "Runners", icon: "server", match: (p: string) => p.startsWith("/runners") },
];

export function Shell({ children }: { children: ReactNode }) {
  const pathname = usePathname() ?? "/";
  const [open, setOpen] = useState(false);
  const { connected } = useServerEvents();
  const { meta } = useMeta();
  const { org, ready } = useOrg();

  useEffect(() => setOpen(false), [pathname]);

  const modules = meta?.modules;
  const mounted = (n: NavItem) => !n.module || !modules || modules.includes(n.module);
  const links = (items: NavItem[]) =>
    items.filter(mounted).map((n) => {
      const active = n.match(pathname);
      return (
        <Link
          key={n.href}
          href={n.href}
          className={cx(
            "flex items-center gap-2.5 rounded-md px-2.5 py-2 text-sm font-medium transition-colors",
            active
              ? "bg-indigo-50 text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300"
              : "text-zinc-600 hover:bg-zinc-100 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-800/60 dark:hover:text-zinc-100",
          )}
        >
          <Icon name={n.icon} className="h-4 w-4" />
          {n.label}
        </Link>
      );
    });

  const nav = (
    <>
      <OrgSwitcher />
      <nav className="flex flex-col gap-0.5 p-2">{links(ORG_NAV)}</nav>
      <nav className="flex flex-col gap-0.5 border-t border-zinc-200 p-2 dark:border-zinc-800" aria-label="Shared">
        <span className="px-2.5 pt-1 pb-1 text-[11px] font-medium tracking-wide text-zinc-400 uppercase">Shared</span>
        {links(GLOBAL_NAV)}
      </nav>
    </>
  );

  const footer = (
    <div className="mt-auto space-y-2 border-t border-zinc-200 p-3 text-xs text-zinc-500 dark:border-zinc-800">
      <div className="flex items-center gap-2">
        <span className={cx("h-2 w-2 rounded-full", connected ? "bg-emerald-500" : "bg-zinc-400")} />
        {connected ? "Live updates" : "Reconnecting…"}
      </div>
      <div className="flex items-center justify-between">
        <span>v{meta?.version ?? "…"}</span>
        <button
          className="inline-flex items-center gap-1 hover:text-zinc-800 dark:hover:text-zinc-200"
          onClick={() => {
            setToken("");
            window.location.reload();
          }}
          title="Forget the stored admin token"
        >
          <Icon name="key" className="h-3.5 w-3.5" /> Sign out
        </button>
      </div>
    </div>
  );

  const brand = (
    <Link href="/" className="flex items-center gap-2 px-4 py-4">
      <span className="flex h-7 w-7 items-center justify-center rounded-md bg-indigo-600 text-white">
        <svg viewBox="0 0 24 24" className="h-4 w-4" fill="none" stroke="currentColor" strokeWidth={2.4} strokeLinecap="round">
          <path d="M6 8h7M6 12h12M6 16h5" />
        </svg>
      </span>
      <span className="text-[15px] font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">CueFlow</span>
    </Link>
  );

  return (
    <div className="flex min-h-screen">
      {/* desktop sidebar */}
      <aside className="sticky top-0 hidden h-screen w-56 shrink-0 flex-col border-r border-zinc-200 bg-white md:flex dark:border-zinc-800 dark:bg-zinc-900">
        {brand}
        {nav}
        {footer}
      </aside>

      {/* mobile drawer */}
      {open && (
        <div className="fixed inset-0 z-40 md:hidden" onClick={() => setOpen(false)}>
          <div className="absolute inset-0 bg-zinc-950/40" />
          <aside
            className="absolute inset-y-0 left-0 flex w-64 flex-col border-r border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-900"
            onClick={(e) => e.stopPropagation()}
          >
            {brand}
            {nav}
            {footer}
          </aside>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex items-center gap-2 border-b border-zinc-200 bg-white/90 px-4 py-2.5 backdrop-blur md:hidden dark:border-zinc-800 dark:bg-zinc-900/90">
          <button
            onClick={() => setOpen(true)}
            className="rounded p-1.5 text-zinc-600 hover:bg-zinc-100 dark:text-zinc-300 dark:hover:bg-zinc-800"
            aria-label="Open navigation"
          >
            <Icon name="menu" className="h-5 w-5" />
          </button>
          <span className="text-sm font-semibold">CueFlow</span>
          {org && <span className="truncate text-sm text-zinc-500">· {org.name}</span>}
        </header>
        {/* Re-mount the page when the org changes so it refetches for the new org. */}
        <main key={org?.id ?? ""} className="mx-auto w-full max-w-[1400px] flex-1 px-4 py-5 sm:px-6 lg:px-8">
          {ready ? children : <Loading />}
        </main>
      </div>
    </div>
  );
}
