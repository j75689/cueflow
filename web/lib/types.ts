// Types mirror docs/api.md (the source of truth).

export type PostStatus =
  | "DRAFT"
  | "PENDING_REVIEW"
  | "APPROVED"
  | "REJECTED"
  | "QUEUED"
  | "PUBLISHED"
  | "FAILED";

export type ConnectorKind = "publisher" | "source" | "search" | "llm" | "notifier";

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
}

export interface Platform {
  id: string;
  name: string;
  max_chars: number;
  supports_thread: boolean;
  max_hashtags: number;
  requires_media: boolean;
}

export interface Meta {
  version: string;
  statuses: PostStatus[];
  transitions: Record<string, PostStatus[]>;
  platforms: Platform[];
  connector_types: ConnectorSpec[];
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
  "id" | "created_at" | "updated_at" | "last_run_at" | "next_run_at"
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
