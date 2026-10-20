"use client";

// A task's files on its page: the Files card, the viewer, and links to a file from the thread.
// Files are the agent's working storage (its notes, and tool results too long for the conversation).
// A task lists its own files and the ones of the tasks above it, which it can read but not change.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { useSearchParams } from "next/navigation";
import { useDialog } from "@/components/dialog";
import { useServerEvent, useToast } from "@/components/providers";
import {
  Button,
  Card,
  CardHeader,
  ErrorBox,
  Icon,
  Loading,
  Modal,
  Pill,
} from "@/components/ui";
import { api } from "@/lib/api";
import { fmtRelative, fmtTime } from "@/lib/format";
import { useAsync } from "@/lib/hooks";
import type { TaskFile, TaskFilePage } from "@/lib/types";

const FileViewerCtx = createContext<((name: string) => void) | null>(null);

/** Wraps a task's page (the task is ?id=) so anything on it can open one of the task's files by name. */
export function TaskFilesProvider({ children }: { children: ReactNode }) {
  const taskId = useSearchParams().get("id") ?? "";
  const [open, setOpen] = useState<string | null>(null);
  return (
    <FileViewerCtx.Provider value={setOpen}>
      {children}
      <FileViewer taskId={taskId} name={open} onClose={() => setOpen(null)} />
    </FileViewerCtx.Provider>
  );
}

/** A link that opens a file of the current task in the viewer. */
export function FileLink({
  name,
  children,
}: {
  name: string;
  children?: ReactNode;
}) {
  const open = useContext(FileViewerCtx);
  if (!open) return <span className="font-mono">{children ?? name}</span>;
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        open(name);
      }}
      className="font-mono text-accent hover:underline"
    >
      {children ?? name}
    </button>
  );
}

export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

/** The Files card for the task's sidebar. */
export function TaskFilesCard({
  taskId,
  parentId,
}: {
  taskId: string;
  parentId?: string | null;
}) {
  const toast = useToast();
  const dialog = useDialog();
  const open = useContext(FileViewerCtx);
  const files = useAsync(() => api.taskFiles(taskId), [taskId]);
  const list = files.data?.files ?? [];
  // A file of this task or of one above it changed: those are the ones this card shows (the parent
  // counts even before it has files; tasks further up once one of their files is listed).
  const owners = new Set([
    taskId,
    ...(parentId ? [parentId] : []),
    ...list.map((f) => f.task_id),
  ]);
  useServerEvent("task.file", (d) => {
    if (d.resync || owners.has(String(d.task_id))) void files.reload();
  });

  const remove = async (f: TaskFile) => {
    if (
      !(await dialog.confirm({
        title: `Delete ${f.name}?`,
        message: "The agent can no longer read it. This can't be undone.",
        confirmLabel: "Delete file",
        tone: "danger",
      }))
    )
      return;
    try {
      await api.deleteTaskFile(taskId, f.name);
      toast.success(`Deleted ${f.name}`);
      void files.reload();
    } catch (e) {
      toast.error(e);
    }
  };

  const total = files.data?.total ?? 0;
  return (
    <Card>
      <CardHeader
        title="Files"
        subtitle="The agent's notes and saved tool results. Files of tasks above this one are marked."
      />
      {files.loading && !files.data ? (
        <Loading />
      ) : files.error ? (
        <ErrorBox onRetry={files.reload}>{files.error}</ErrorBox>
      ) : list.length === 0 ? (
        <p className="px-4 py-3 text-xs text-fg-3">No files yet.</p>
      ) : (
        <ul className="divide-y divide-line">
          {list.map((f) => (
            <li
              key={`${f.task_id}/${f.name}`}
              className="group px-4 py-2 text-xs"
            >
              <div className="flex min-w-0 items-center gap-2">
                <button
                  type="button"
                  onClick={() => open?.(f.name)}
                  className="min-w-0 flex-1 truncate text-left font-mono text-[12px] text-fg hover:text-accent hover:underline"
                  title={f.name}
                >
                  {f.name}
                </button>
                {!f.own && <Pill>from #{f.task_number}</Pill>}
                <button
                  type="button"
                  aria-label={`Download ${f.name}`}
                  title="Download"
                  onClick={() =>
                    api
                      .downloadTaskFile(taskId, f.name)
                      .catch((e) => toast.error(e))
                  }
                  className="shrink-0 rounded p-0.5 text-fg-3 hover:bg-subtle hover:text-fg"
                >
                  <Icon name="down" className="h-3.5 w-3.5" />
                </button>
                {f.own && (
                  <button
                    type="button"
                    aria-label={`Delete ${f.name}`}
                    title="Delete"
                    onClick={() => void remove(f)}
                    className="shrink-0 rounded p-0.5 text-fg-3 hover:bg-subtle hover:text-red-600"
                  >
                    <Icon name="trash" className="h-3.5 w-3.5" />
                  </button>
                )}
              </div>
              {f.description && (
                <p className="mt-0.5 truncate text-fg-3" title={f.description}>
                  {f.description}
                </p>
              )}
              <p className="mt-0.5 text-[11px] text-fg-4">
                {fmtBytes(f.size_bytes)} · {f.created_by} ·{" "}
                <span title={fmtTime(f.updated_at)}>
                  {fmtRelative(f.updated_at)}
                </span>
              </p>
            </li>
          ))}
          {total > list.length && (
            <li className="px-4 py-2 text-[11px] text-fg-3">
              {total - list.length} more not shown
            </li>
          )}
        </ul>
      )}
    </Card>
  );
}

/** Shows a file a page (64 KB) at a time; more pages load on request, so a large file never loads whole. */
function FileViewer({
  taskId,
  name,
  onClose,
}: {
  taskId: string;
  name: string | null;
  onClose: () => void;
}) {
  const toast = useToast();
  const [pages, setPages] = useState<TaskFilePage[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(
    async (offset: number) => {
      if (!name) return;
      setLoading(true);
      setError(null);
      try {
        const page = await api.taskFilePage(taskId, name, offset);
        setPages((ps) => (offset === 0 ? [page] : [...ps, page]));
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setLoading(false);
      }
    },
    [taskId, name],
  );
  useEffect(() => {
    setPages([]);
    if (name) void load(0);
  }, [name, load]);

  const last = pages[pages.length - 1];
  const shown = pages.reduce(
    (n, p) => n + new TextEncoder().encode(p.text).length,
    0,
  );
  return (
    <Modal
      open={name !== null}
      onClose={onClose}
      title={<span className="font-mono text-sm">{name}</span>}
      wide
      footer={
        <div className="flex w-full items-center gap-2">
          {last && (
            <span className="text-xs text-fg-3">
              {last.binary
                ? fmtBytes(last.size_bytes)
                : `${fmtBytes(shown)} of ${fmtBytes(last.size_bytes)}`}
            </span>
          )}
          <span className="flex-1" />
          {name && (
            <Button
              size="sm"
              onClick={() =>
                api.downloadTaskFile(taskId, name).catch((e) => toast.error(e))
              }
            >
              <Icon name="down" className="h-3.5 w-3.5" /> Download
            </Button>
          )}
          {last?.next_offset != null && !last.binary && (
            <Button
              size="sm"
              loading={loading}
              onClick={() => void load(last.next_offset!)}
            >
              Load more
            </Button>
          )}
        </div>
      }
    >
      {error && (
        <ErrorBox onRetry={() => void load(last?.next_offset ?? 0)}>
          {error}
        </ErrorBox>
      )}
      {loading && pages.length === 0 && <Loading />}
      {last?.binary ? (
        <p className="text-sm text-fg-2">
          This file isn&apos;t text, so it can&apos;t be shown here. Download it
          instead.
        </p>
      ) : (
        pages.length > 0 && (
          <pre className="max-h-[65vh] overflow-auto rounded-md bg-canvas p-3 font-mono text-[12px] leading-relaxed whitespace-pre-wrap text-fg-2">
            {pages.map((p) => p.text).join("")}
          </pre>
        )
      )}
    </Modal>
  );
}
