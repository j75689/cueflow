import type {
  Agent,
  AgentInput,
  AgentMemory,
  AgentRun,
  AuditLog,
  Connector,
  ConnectorTestResult,
  ConnectorTool,
  Meta,
  Pipeline,
  PipelineInput,
  PipelineRun,
  Post,
  PostList,
  SourceItem,
  Schedule,
  ScheduleInput,
  Stats,
  Task,
  TaskCreate,
  TaskDetail,
  TaskMessage,
  TaskPatch,
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

async function request<T>(method: string, path: string, body?: unknown, query?: Query): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  const token = getToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";

  let res: Response;
  try {
    res = await fetch(`${BASE}${path}${buildQuery(query)}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (e) {
    throw new ApiError(0, "network", e instanceof Error ? e.message : "network error");
  }

  if (res.status === 401) {
    window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
  }
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

export type PostPatch = Partial<
  Pick<
    Post,
    | "title"
    | "content"
    | "platforms"
    | "thread_parts"
    | "hashtags"
    | "media_urls"
    | "image_prompt"
    | "scheduled_at"
    | "publisher_connector_id"
  >
> & { version: number };

export type PostCreate = Pick<Post, "title" | "content" | "platforms"> &
  Partial<Pick<Post, "thread_parts" | "hashtags" | "media_urls" | "image_prompt" | "scheduled_at">> & {
    submit?: boolean;
  };

export const api = {
  meta: () => request<Meta>("GET", "/meta"),
  stats: () => request<Stats>("GET", "/stats"),
  health: () => request<{ status: string }>("GET", "/health"),

  listPosts: (q: {
    status?: string[];
    q?: string;
    pipeline_id?: string;
    run_id?: string;
    limit?: number;
    offset?: number;
  }) =>
    request<PostList>("GET", "/posts", undefined, q),
  getPost: (id: string) => request<Post>("GET", `/posts/${id}`),
  createPost: (body: PostCreate) => request<Post>("POST", "/posts", body),
  updatePost: (id: string, body: PostPatch) => request<Post>("PATCH", `/posts/${id}`, body),
  deletePost: (id: string) => request<void>("DELETE", `/posts/${id}`),
  submitPost: (id: string) => request<Post>("POST", `/posts/${id}/submit`),
  approvePost: (id: string, body: { scheduled_at?: string | null; publisher_connector_id?: string | null } = {}) =>
    request<Post>("POST", `/posts/${id}/approve`, body),
  rejectPost: (id: string, reason: string) => request<Post>("POST", `/posts/${id}/reject`, { reason }),
  retryPost: (id: string) => request<Post>("POST", `/posts/${id}/retry`),
  postAudit: (id: string) => request<AuditLog[]>("GET", `/posts/${id}/audit`),

  listConnectors: (kind?: string) => request<Connector[]>("GET", "/connectors", undefined, { kind }),
  getConnector: (id: string) => request<Connector>("GET", `/connectors/${id}`),
  createConnector: (body: { name: string; type: string; is_active?: boolean; config: Record<string, unknown> }) =>
    request<Connector>("POST", "/connectors", body),
  updateConnector: (id: string, body: { name?: string; is_active?: boolean; config?: Record<string, unknown> }) =>
    request<Connector>("PATCH", `/connectors/${id}`, body),
  deleteConnector: (id: string) => request<void>("DELETE", `/connectors/${id}`),
  testConnector: (id: string) => request<ConnectorTestResult>("POST", `/connectors/${id}/test`),
  connectorTools: (id: string) => request<ConnectorTool[]>("GET", `/connectors/${id}/tools`),
  fetchConnector: (id: string) => request<{ new_items: number }>("POST", `/connectors/${id}/fetch`),
  connectorItems: (id: string, limit = 50) =>
    request<SourceItem[]>("GET", `/connectors/${id}/items`, undefined, { limit }),

  listPipelines: () => request<Pipeline[]>("GET", "/pipelines"),
  getPipeline: (id: string) => request<Pipeline>("GET", `/pipelines/${id}`),
  createPipeline: (body: PipelineInput) => request<Pipeline>("POST", "/pipelines", body),
  updatePipeline: (id: string, body: Partial<PipelineInput>) => request<Pipeline>("PATCH", `/pipelines/${id}`, body),
  deletePipeline: (id: string) => request<void>("DELETE", `/pipelines/${id}`),
  runPipeline: (id: string) => request<{ run_id: string }>("POST", `/pipelines/${id}/run`),

  listRuns: (q: { pipeline_id?: string; limit?: number } = {}) =>
    request<PipelineRun[]>("GET", "/runs", undefined, q),
  getRun: (id: string) => request<PipelineRun>("GET", `/runs/${id}`),

  listAgents: () => request<Agent[]>("GET", "/agents"),
  getAgent: (id: string) => request<Agent>("GET", `/agents/${id}`),
  createAgent: (body: Partial<AgentInput>) => request<Agent>("POST", "/agents", body),
  updateAgent: (id: string, body: Partial<AgentInput>) => request<Agent>("PATCH", `/agents/${id}`, body),
  deleteAgent: (id: string) => request<void>("DELETE", `/agents/${id}`),
  agentMemories: (id: string) => request<AgentMemory[]>("GET", `/agents/${id}/memories`),
  addAgentMemory: (id: string, content: string) =>
    request<AgentMemory>("POST", `/agents/${id}/memories`, { content }),
  deleteAgentMemory: (id: string, memoryId: string) =>
    request<void>("DELETE", `/agents/${id}/memories/${memoryId}`),

  listTasks: (q: { status?: string[]; assignee?: string; parent_id?: string; q?: string; all?: boolean } = {}) =>
    request<{ items: Task[] }>("GET", "/tasks", undefined, { ...q, all: q.all ? "1" : undefined }),
  getTask: (id: string) => request<TaskDetail>("GET", `/tasks/${id}`),
  createTask: (body: TaskCreate) => request<Task>("POST", "/tasks", body),
  updateTask: (id: string, body: TaskPatch) => request<Task>("PATCH", `/tasks/${id}`, body),
  deleteTask: (id: string) => request<void>("DELETE", `/tasks/${id}`),
  taskMessages: (id: string) => request<TaskMessage[]>("GET", `/tasks/${id}/messages`),
  postTaskMessage: (id: string, content: string) =>
    request<TaskMessage>("POST", `/tasks/${id}/messages`, { content }),
  runTask: (id: string) => request<Task>("POST", `/tasks/${id}/run`),
  cancelTask: (id: string) => request<Task>("POST", `/tasks/${id}/cancel`),
  taskRuns: (id: string) => request<AgentRun[]>("GET", `/tasks/${id}/runs`),

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

export function eventsURL(): string {
  const token = getToken();
  return `${BASE}/events${token ? `?token=${encodeURIComponent(token)}` : ""}`;
}
