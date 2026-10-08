// Types mirror docs/api.md (the source of truth).

export type PostStatus =
  | "DRAFT"
  | "PENDING_REVIEW"
  | "APPROVED"
  | "REJECTED"
  | "QUEUED"
  | "PUBLISHED"
  | "FAILED";

export type ConnectorKind = "publisher" | "source" | "search" | "llm" | "notifier" | "runtime" | "image" | "tools";

export type FieldType =
  | "string"
  | "password"
  | "text"
  | "number"
  | "bool"
  | "select"
  | "string_list"
  | "kv"
  | "json";

export interface FieldSpec {
  key: string;
  label: string;
  type: FieldType;
  required: boolean;
  secret: boolean;
  placeholder?: string | null;
  help?: string | null;
  default?: unknown;
  options?: string[] | null;
}

export interface ConnectorSpec {
  type: string;
  kind: ConnectorKind;
  name: string;
  description: string;
  fields: FieldSpec[];
  /** Publisher that can post to individual accounts chosen per post (MCP publisher, Postiz). */
  accounts?: boolean;
  /** Posts through this publisher must name their accounts (MCP publisher). */
  accounts_required?: boolean;
  /** Kept only so connectors that already exist under this type load and can be edited; not offered for new ones. */
  hidden?: boolean;
}

/** An image CueFlow stores (uploaded or generated); `url` is "/media/{id}". */
export interface MediaItem {
  id: string;
  url: string;
  mime_type: string;
  size_bytes: number;
  source: "upload" | "generated";
  prompt: string;
}

/** An account a per-account publisher can post to. */
export interface ConnectorAccount {
  id: string;
  platform: string;
  name: string;
  /** The service's label, e.g. brand, personal or test. */
  tag?: string;
}

/** A team with its own agents, board, schedules, review queue and runs. Connectors are shared. */
export interface Org {
  id: string;
  name: string;
  slug: string;
  created_at: string;
  updated_at: string;
}

export interface Platform {
  id: string;
  name: string;
  max_chars: number;
  supports_thread: boolean;
  max_hashtags: number;
  requires_media: boolean;
}

export interface AgentCapability {
  id: string;
  name: string;
  description: string;
  tools: string[];
}

export interface Meta {
  version: string;
  statuses: PostStatus[];
  transitions: Record<string, PostStatus[]>;
  platforms: Platform[];
  connector_types: ConnectorSpec[];
  agent_capabilities?: AgentCapability[];
  agent_roles?: AgentRole[];
  task_statuses?: TaskStatus[];
  task_priorities?: TaskPriority[];
}

export interface SourceRef {
  title?: string;
  url?: string;
  source?: string;
  [k: string]: unknown;
}

export interface SourceContext {
  topic?: string;
  items?: SourceRef[];
  search?: SourceRef[];
  [k: string]: unknown;
}

export interface Post {
  id: string;
  org_id: string;
  task_id?: string | null;
  group_id: string | null;
  pipeline_id: string | null;
  run_id: string | null;
  publisher_connector_id: string | null;
  title: string;
  content: string;
  thread_parts: string[];
  hashtags: string[];
  image_prompt: string;
  platforms: string[];
  media_urls: string[];
  accounts: string[];
  /** Threads topic tag. */
  topic: string;
  /** URL shown as a link preview card. */
  link_url: string;
  status: PostStatus;
  review_note: string;
  source_context: SourceContext | null;
  error_message: string;
  external_post_ids: Record<string, string> | null;
  attempts: number;
  version: number;
  warnings: string[] | null;
  scheduled_at: string | null;
  published_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface PostList {
  items: Post[];
  total: number;
}

export interface AuditLog {
  id: string;
  post_id: string;
  action: string;
  actor: string;
  from_status: string | null;
  to_status: string | null;
  note: string | null;
  payload_diff: Record<string, { before: unknown; after: unknown }> | null;
  created_at: string;
}

export interface Connector {
  id: string;
  name: string;
  kind: ConnectorKind;
  type: string;
  is_active: boolean;
  config: Record<string, unknown>;
  secrets_set: string[] | null;
  last_status: "ok" | "error" | null;
  last_error: string | null;
  last_checked_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface ConnectorTestResult {
  ok: boolean;
  message: string;
  details?: Record<string, unknown> | null;
}

export interface ConnectorTool {
  name: string;
  description: string;
  agent_allowed: boolean;
  reason?: string | null;
}

export interface SourceItem {
  id: string;
  connector_id: string;
  external_id: string;
  title: string;
  url: string;
  author: string;
  content: string;
  published_at: string | null;
  fetched_at: string;
  consumed_at: string | null;
}

export interface Pipeline {
  id: string;
  org_id: string;
  name: string;
  description: string;
  is_active: boolean;
  cron_expr: string;
  timezone: string;
  topic: string;
  instructions: string;
  language: string;
  platforms: string[];
  llm_connector_id: string | null;
  source_connector_ids: string[];
  search_connector_id: string | null;
  search_queries: string[];
  publisher_connector_id: string | null;
  notifier_connector_id: string | null;
  lookback_hours: number;
  max_items: number;
  last_run_at: string | null;
  next_run_at: string | null;
  created_at: string;
  updated_at: string;
}

export type PipelineInput = Omit<
  Pipeline,
  "id" | "org_id" | "created_at" | "updated_at" | "last_run_at" | "next_run_at"
>;

export interface RunLogLine {
  at: string;
  level: string;
  msg: string;
}

export interface PipelineRun {
  id: string;
  pipeline_id: string;
  pipeline_name: string;
  trigger: "cron" | "manual";
  status: "RUNNING" | "SUCCEEDED" | "FAILED";
  started_at: string;
  finished_at: string | null;
  error: string | null;
  stats: Record<string, number> | null;
  log: RunLogLine[] | null;
}

export interface Stats {
  posts_by_status: Partial<Record<PostStatus, number>>;
  connectors: { total: number; active: number };
  pipelines: { total: number; active: number };
  last_run: PipelineRun | null;
}

/* ------------------------------ Agents & tasks ----------------------------- */

export type AgentRole = "manager" | "researcher" | "writer" | "editor" | "custom";

export interface Agent {
  id: string;
  org_id: string;
  name: string;
  slug: string;
  avatar: string;
  role: AgentRole;
  description: string;
  instructions: string;
  capabilities: string[];
  llm_connector_id: string | null;
  search_connector_id: string | null;
  /** External ACP agent runtime; null = built-in LLM loop. */
  runtime_connector_id: string | null;
  source_connector_ids: string[];
  /** MCP tool servers this agent may use (per-tool permissions live on the connector). */
  tool_connector_ids: string[];
  language: string;
  platforms: string[];
  max_turns: number;
  is_active: boolean;
  stats?: { open_tasks: number; running: number } | null;
  created_at: string;
  updated_at: string;
}

export type AgentInput = Omit<Agent, "id" | "org_id" | "stats" | "created_at" | "updated_at">;

export interface AgentMemory {
  id: string;
  agent_id: string;
  content: string;
  created_at: string;
}

export type TaskStatus =
  | "BACKLOG"
  | "TODO"
  | "IN_PROGRESS"
  | "BLOCKED"
  | "WAITING_HUMAN"
  | "REVIEW"
  | "DONE"
  | "CANCELED";

export type TaskPriority = "low" | "normal" | "high" | "urgent";

export interface AgentRef {
  id: string;
  name: string;
  slug: string;
  avatar: string;
}

export interface Task {
  id: string;
  org_id: string;
  number: number;
  title: string;
  description: string;
  status: TaskStatus;
  priority: TaskPriority;
  labels: string[] | null;
  assignee_agent_id: string | null;
  assignee: AgentRef | null;
  created_by: "human" | "agent";
  creator_agent_id: string | null;
  parent_id: string | null;
  subtask_counts: { total: number; done: number } | null;
  result: string;
  due_at: string | null;
  position: number;
  running: boolean;
  attempts: number;
  schedule_id: string | null;
  /** Publisher and accounts applied to drafts made for this task (inherited by subtasks). */
  publisher_connector_id: string | null;
  publish_accounts: string[];
  version: number;
  created_at: string;
  updated_at: string;
  started_at: string | null;
  completed_at: string | null;
}

export interface TaskPostRef {
  id: string;
  title: string;
  status: PostStatus;
  platforms: string[];
}

export interface TaskDetail extends Task {
  subtasks: Task[] | null;
  parent: Task | null;
  posts: TaskPostRef[] | null;
}

export interface TaskCreate {
  title: string;
  description?: string;
  assignee_agent_id?: string | null;
  priority?: TaskPriority;
  labels?: string[];
  due_at?: string | null;
  status?: TaskStatus;
  parent_id?: string | null;
}

export type TaskPatch = Partial<
  Pick<
    Task,
    | "title"
    | "description"
    | "status"
    | "priority"
    | "labels"
    | "assignee_agent_id"
    | "due_at"
    | "position"
    | "publisher_connector_id"
    | "publish_accounts"
  >
>;

export type TaskMessageKind = "message" | "tool_call" | "tool_result" | "status" | "question" | "result" | "approval";

export type ToolPermissionValue = "allow" | "ask" | "deny";

/** A tool of an MCP tool server with its effective permission. */
export interface ToolPermission {
  name: string;
  description: string;
  read_only: boolean;
  destructive: boolean;
  risky: boolean;
  default: ToolPermissionValue;
  permission: ToolPermissionValue;
}

/** An agent's call to an "ask" tool. */
export interface ToolApproval {
  id: string;
  task_id: string;
  server_name: string;
  tool: string;
  arguments: unknown;
  status: "pending" | "approved" | "rejected";
  result: string;
  is_error: boolean;
  note: string;
  created_at: string;
  decided_at: string | null;
}

export interface TaskMessage {
  id: string;
  task_id: string;
  seq: number;
  author_type: "human" | "agent" | "system" | "tool";
  agent_id: string | null;
  agent: { name: string; avatar: string; slug: string } | null;
  kind: TaskMessageKind;
  content: string;
  data: Record<string, unknown> | null;
  created_at: string;
}

export interface AgentRun {
  id: string;
  task_id: string;
  agent_id: string;
  status: "RUNNING" | "SUCCEEDED" | "FAILED" | "CANCELED";
  turns: number;
  tool_calls: number;
  input_tokens: number;
  output_tokens: number;
  error: string | null;
  started_at: string;
  finished_at: string | null;
  /** LLM connector (built-in) or runtime connector (ACP) used by this run. */
  connector_id?: string | null;
  model?: string;
  runtime?: "builtin" | "acp";
}

export interface UsageTotals {
  runs: number;
  failed_runs: number;
  input_tokens: number;
  output_tokens: number;
  tool_calls: number;
  unreported_runs: number;
}

export interface UsageDay {
  date: string;
  runs: number;
  input_tokens: number;
  output_tokens: number;
}

export interface UsageSource {
  source: "agent" | "pipeline" | string;
  runs: number;
  input_tokens: number;
  output_tokens: number;
}

export interface UsageAgent {
  agent_id: string;
  name: string;
  avatar: string;
  slug: string;
  runs: number;
  input_tokens: number;
  output_tokens: number;
}

export interface UsageModel {
  connector_id: string | null;
  connector_name: string;
  connector_type: string;
  runtime: "builtin" | "acp" | string;
  model: string;
  runs: number;
  input_tokens: number;
  output_tokens: number;
}

export interface UsageOrg {
  org_id: string;
  name: string;
  slug: string;
  runs: number;
  input_tokens: number;
  output_tokens: number;
}

export interface UsageTask {
  task_id: string;
  number: number;
  title: string;
  runs: number;
  input_tokens: number;
  output_tokens: number;
}

export interface Usage {
  from: string;
  to: string;
  tz: string;
  totals: UsageTotals;
  daily: UsageDay[];
  by_source: UsageSource[];
  by_org?: UsageOrg[];
  by_agent: UsageAgent[];
  by_model: UsageModel[];
  top_tasks: UsageTask[];
}

export interface Schedule {
  id: string;
  org_id: string;
  name: string;
  cron_expr: string;
  timezone: string;
  is_active: boolean;
  assignee_agent_id: string | null;
  task_title: string;
  task_description: string;
  priority: TaskPriority;
  created_by: "human" | "agent";
  /** Publisher and accounts copied to the tasks this schedule creates. */
  publisher_connector_id: string | null;
  publish_accounts: string[];
  last_run_at: string | null;
  next_run_at: string | null;
  created_at: string;
  updated_at: string;
}

export type ScheduleInput = Pick<
  Schedule,
  | "name"
  | "cron_expr"
  | "timezone"
  | "is_active"
  | "assignee_agent_id"
  | "task_title"
  | "task_description"
  | "priority"
  | "publisher_connector_id"
  | "publish_accounts"
>;

export interface Runner {
  id: string;
  name: string;
  labels: string[];
  /** Orgs this runner serves; empty = every org. */
  org_ids: string[];
  is_active: boolean;
  max_concurrency: number;
  version: string;
  platform: string;
  last_seen_at: string | null;
  created_at: string;
  online: boolean;
  running_jobs: number;
}

export interface RunnerRegistration {
  token: string;
  expires_at: string;
  url: string;
  command: string;
}

export interface RunnerJob {
  id: string;
  task_id: string;
  status: "queued" | "running" | "canceling" | "done" | "failed" | "canceled" | "expired";
  labels: string[];
  runner_id: string | null;
  runner_name: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface AgentToken {
  id: string;
  agent_id: string;
  name: string;
  created_at: string;
  last_used_at: string | null;
}

export interface NewAgentToken {
  id: string;
  name: string;
  /** Shown once. */
  token: string;
  mcp_url: string;
  created_at: string;
}
