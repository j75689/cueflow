import type {
  Agent,
  AgentInput,
  AgentMemory,
  AgentRun,
  AgentToken,
  BulkResult,
  Connector,
  ConnectorAccount,
  ConnectorTestResult,
  ConnectorTool,
  Deliverable,
  DeliverableAudit,
  DeliverableList,
  MediaItem,
  Meta,
  NewAgentToken,
  Org,
  Runner,
  RunnerJob,
  RunnerRegistration,
  Schedule,
  ScheduleInput,
  SourceItem,
  Stats,
  Task,
  TaskCreate,
  TaskDetail,
  TaskMessage,
  TaskPatch,
  TaskStatus,
  TeamTemplate,
  ToolApproval,
  ToolPermission,
  ToolPermissionValue,
  Usage,
  TaskFile,
  TaskFilePage,
} from "./types";

const BASE = "/api/v1";
const TOKEN_KEY = "cueflow.token";

export function getToken(): string {
  try {
    return window.localStorage.getItem(TOKEN_KEY) ?? "";
  } catch {
    return "";
  }
}

export function setToken(token: string) {
  try {
    if (token) window.localStorage.setItem(TOKEN_KEY, token);
    else window.localStorage.removeItem(TOKEN_KEY);
  } catch {
    /* storage unavailable: token lives only for this request cycle */
  }
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

// The org every request works in (sent as ?org=); set by the org switcher. Global endpoints
// (connectors, usage, media) ignore it.
let currentOrg = "";

// Fired when the org changes, so the live-update stream can reopen for the new org.
export const ORG_CHANGED_EVENT = "cueflow:org";

export function setApiOrg(id: string) {
  if (id === currentOrg) return;
  currentOrg = id;
  if (typeof window !== "undefined") window.dispatchEvent(new Event(ORG_CHANGED_EVENT));
}

export function apiOrg(): string {
  return currentOrg;
}

// Fired when the server answers 401 so the token prompt can open.
export const UNAUTHORIZED_EVENT = "cueflow:unauthorized";

type Query = Record<string, string | number | boolean | string[] | null | undefined>;

function buildQuery(q?: Query): string {
  if (!q) return "";
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v === undefined || v === null || v === "") continue;
    if (Array.isArray(v)) v.forEach((x) => p.append(k, x));
    else p.append(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

// fetchApi sends one API request with the token and the current org, and opens the token prompt on 401.
async function fetchApi(method: string, path: string, body?: unknown, query?: Query): Promise<Response> {
  const headers: Record<string, string> = { Accept: "application/json" };
  const token = getToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  const form = body instanceof FormData;
  if (body !== undefined && !form) headers["Content-Type"] = "application/json";

  let res: Response;
  try {
    const q = currentOrg && !path.startsWith("/orgs") ? { org: currentOrg, ...query } : query;
    res = await fetch(`${BASE}${path}${buildQuery(q)}`, {
      method,
      headers,
      body: body === undefined ? undefined : form ? body : JSON.stringify(body),
    });
  } catch (e) {
    throw new ApiError(0, "network", e instanceof Error ? e.message : "network error");
  }

  if (res.status === 401) {
    window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
  }
  return res;
}

async function request<T>(method: string, path: string, body?: unknown, query?: Query): Promise<T> {
  const res = await fetchApi(method, path, body, query);
  if (res.status === 204) return undefined as T;

  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string } } | undefined)?.error;
    throw new ApiError(res.status, err?.code ?? "http_error", err?.message ?? `${res.status} ${res.statusText}`);
  }
  return data as T;
}

export const api = {
  meta: () => request<Meta>("GET", "/meta"),
  stats: () => request<Stats>("GET", "/stats"),
  health: () => request<{ status: string }>("GET", "/health"),

  listOrgs: () => request<Org[]>("GET", "/orgs"),
  createOrg: (body: { name: string; slug?: string; seed_agents?: boolean }) => request<Org>("POST", "/orgs", body),
  updateOrg: (id: string, body: { name?: string; slug?: string; notifier_id?: string }) => request<Org>("PATCH", `/orgs/${id}`, body),
  deleteOrg: (id: string) => request<void>("DELETE", `/orgs/${id}`),

  listDeliverables: (q: { status?: string[]; kind?: string; task_id?: string; q?: string; limit?: number; offset?: number }) =>
    request<DeliverableList>("GET", "/deliverables", undefined, q),
  getDeliverable: (id: string) => request<Deliverable>("GET", `/deliverables/${id}`),
  createDeliverable: (body: {
    kind: string;
    title: string;
    payload: Record<string, unknown>;
    task_id?: string | null;
    action_connector_id?: string | null;
    submit?: boolean;
  }) => request<Deliverable>("POST", "/deliverables", body),
  updateDeliverable: (
    id: string,
    body: { version: number; title?: string; payload?: Record<string, unknown>; action_connector_id?: string | null },
  ) => request<Deliverable>("PATCH", `/deliverables/${id}`, body),
  deleteDeliverable: (id: string) => request<void>("DELETE", `/deliverables/${id}`),
  submitDeliverable: (id: string) => request<Deliverable>("POST", `/deliverables/${id}/submit`),
  approveDeliverable: (id: string, body: { note?: string; action_connector_id?: string | null } = {}) =>
    request<Deliverable>("POST", `/deliverables/${id}/approve`, body),
  rejectDeliverable: (id: string, reason: string) =>
    request<Deliverable>("POST", `/deliverables/${id}/reject`, { reason }),
  retryDeliverable: (id: string) => request<Deliverable>("POST", `/deliverables/${id}/retry`),
  bulkDeliverables: (body: { action: "approve" | "reject"; ids: string[]; note?: string; reason?: string }) =>
    request<BulkResult>("POST", "/deliverables/bulk", body),
  deliverableAudit: (id: string) => request<DeliverableAudit[]>("GET", `/deliverables/${id}/audit`),

  listConnectors: (kind?: string) => request<Connector[]>("GET", "/connectors", undefined, { kind }),
  getConnector: (id: string) => request<Connector>("GET", `/connectors/${id}`),
  createConnector: (body: { name: string; type: string; is_active?: boolean; config: Record<string, unknown> }) =>
    request<Connector>("POST", "/connectors", body),
  updateConnector: (id: string, body: { name?: string; is_active?: boolean; config?: Record<string, unknown> }) =>
    request<Connector>("PATCH", `/connectors/${id}`, body),
  deleteConnector: (id: string) => request<void>("DELETE", `/connectors/${id}`),
  testConnector: (id: string) => request<ConnectorTestResult>("POST", `/connectors/${id}/test`),
  connectorTools: (id: string) => request<ConnectorTool[]>("GET", `/connectors/${id}/tools`),
  connectorAccounts: (id: string) => request<ConnectorAccount[]>("GET", `/connectors/${id}/accounts`),
  toolPermissions: (id: string) => request<{ tools: ToolPermission[] }>("GET", `/connectors/${id}/tool-permissions`),
  setToolPermissions: (id: string, permissions: Record<string, ToolPermissionValue>) =>
    request<{ permissions: Record<string, ToolPermissionValue> }>("PUT", `/connectors/${id}/tool-permissions`, {
      permissions,
    }),
  taskApprovals: (id: string) => request<ToolApproval[]>("GET", `/tasks/${id}/approvals`),
  decideApproval: (taskId: string, approvalId: string, decision: "approve" | "reject", note?: string) =>
    request<ToolApproval>("POST", `/tasks/${taskId}/approvals/${approvalId}`, { decision, note }),

  uploadMedia: (file: File) => {
    const fd = new FormData();
    fd.append("file", file);
    return request<MediaItem>("POST", "/media", fd);
  },
  generateMedia: (body: { prompt: string; aspect?: string; connector_id?: string | null }) =>
    request<MediaItem>("POST", "/media/generate", body),
  fetchConnector: (id: string) => request<{ new_items: number }>("POST", `/connectors/${id}/fetch`),
  connectorItems: (id: string, limit = 50) =>
    request<SourceItem[]>("GET", `/connectors/${id}/items`, undefined, { limit }),

  listAgents: () => request<Agent[]>("GET", "/agents"),
  getAgent: (id: string) => request<Agent>("GET", `/agents/${id}`),
  createAgent: (body: Partial<AgentInput>) => request<Agent>("POST", "/agents", body),
  importTeam: (team: TeamTemplate) =>
    request<{ created: Agent[]; skipped: string[] }>("POST", "/agents/template", team),
  exportTeam: () => request<TeamTemplate>("GET", "/agents/template"),
  updateAgent: (id: string, body: Partial<AgentInput>) => request<Agent>("PATCH", `/agents/${id}`, body),
  deleteAgent: (id: string) => request<void>("DELETE", `/agents/${id}`),
  agentMemories: (id: string) => request<AgentMemory[]>("GET", `/agents/${id}/memories`),
  addAgentMemory: (id: string, content: string) =>
    request<AgentMemory>("POST", `/agents/${id}/memories`, { content }),
  deleteAgentMemory: (id: string, memoryId: string) =>
    request<void>("DELETE", `/agents/${id}/memories/${memoryId}`),

  agentTokens: (id: string) => request<AgentToken[]>("GET", `/agents/${id}/tokens`),
  createAgentToken: (id: string, name: string) => request<NewAgentToken>("POST", `/agents/${id}/tokens`, { name }),
  revokeAgentToken: (id: string, tokenId: string) => request<void>("DELETE", `/agents/${id}/tokens/${tokenId}`),

  listRunners: () => request<Runner[]>("GET", "/runners"),
  createRunnerRegistration: (body: { labels: string[]; org_ids?: string[] }) =>
    request<RunnerRegistration>("POST", "/runners/registrations", body),
  updateRunner: (
    id: string,
    body: Partial<Pick<Runner, "name" | "labels" | "org_ids" | "is_active" | "max_concurrency">>,
  ) => request<Runner>("PATCH", `/runners/${id}`, body),
  deleteRunner: (id: string) => request<void>("DELETE", `/runners/${id}`),
  taskRunnerJobs: (id: string) => request<RunnerJob[]>("GET", `/tasks/${id}/runner-jobs`),

  listTasks: (q: { status?: string[]; assignee?: string; parent_id?: string; q?: string; all?: boolean } = {}) =>
    request<{ items: Task[] }>("GET", "/tasks", undefined, { ...q, all: q.all ? "1" : undefined }),
  getTask: (id: string) => request<TaskDetail>("GET", `/tasks/${id}`),
  createTask: (body: TaskCreate) => request<Task>("POST", "/tasks", body),
  updateTask: (id: string, body: TaskPatch) => request<Task>("PATCH", `/tasks/${id}`, body),
  deleteTask: (id: string) => request<void>("DELETE", `/tasks/${id}`),
  bulkTasks: (body: { action: "move" | "delete"; ids: string[]; status?: TaskStatus }) =>
    request<BulkResult>("POST", "/tasks/bulk", body),
  taskMessages: (id: string) => request<TaskMessage[]>("GET", `/tasks/${id}/messages`),
  postTaskMessage: (id: string, content: string) =>
    request<TaskMessage>("POST", `/tasks/${id}/messages`, { content }),
  runTask: (id: string) => request<Task>("POST", `/tasks/${id}/run`),
  cancelTask: (id: string) => request<Task>("POST", `/tasks/${id}/cancel`),
  taskRuns: (id: string) => request<AgentRun[]>("GET", `/tasks/${id}/runs`),
  // A task's files: its own and its ancestors' (own: false), addressed by task and name.
  taskFiles: (id: string) => request<{ files: TaskFile[]; total: number }>("GET", `/tasks/${id}/files`),
  taskFilePage: (id: string, name: string, offset = 0) =>
    request<TaskFilePage>("GET", `/tasks/${id}/files/content`, undefined, { name, offset }),
  deleteTaskFile: (id: string, name: string) => request<void>("DELETE", `/tasks/${id}/files`, undefined, { name }),
  downloadTaskFile: (id: string, name: string) => downloadTaskFile(id, name),

  // usage
  usage: (q: { days: number; tz?: string }) =>
    request<Usage>("GET", "/usage", undefined, { days: String(q.days), tz: q.tz }),

  listSchedules: () => request<Schedule[]>("GET", "/schedules"),
  createSchedule: (body: ScheduleInput) => request<Schedule>("POST", "/schedules", body),
  updateSchedule: (id: string, body: Partial<ScheduleInput>) => request<Schedule>("PATCH", `/schedules/${id}`, body),
  deleteSchedule: (id: string) => request<void>("DELETE", `/schedules/${id}`),
  runSchedule: (id: string) => request<{ task_id: string }>("POST", `/schedules/${id}/run`),
};

export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}

// eventsURL is the live-update stream for the current org: its events and the global ones.
export function eventsURL(): string {
  return `${BASE}/events${buildQuery({ token: getToken(), org: currentOrg })}`;
}

// downloadTaskFile saves a task file. The API needs the token header, which a plain link can't send,
// so the file is fetched and handed to the browser as a download.
async function downloadTaskFile(id: string, name: string): Promise<void> {
  const res = await fetchApi("GET", `/tasks/${id}/files/download`, undefined, { name });
  if (!res.ok) throw new ApiError(res.status, "http_error", `${res.status} ${res.statusText}`);
  saveBlob(await res.blob(), name.slice(name.lastIndexOf("/") + 1));
}

/** saveBlob hands bytes to the browser as a download named filename. */
export function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
