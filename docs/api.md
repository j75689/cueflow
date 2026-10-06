# CueFlow REST API (v1)

Base path: `/api/v1` (no trailing slashes). All bodies are JSON. Timestamps are RFC 3339 strings. IDs are UUID strings.
Errors: non-2xx with `{"error": {"code": "invalid_transition", "message": "..."}}`.
Common codes: `not_found`, `invalid_argument`, `invalid_transition`, `version_conflict`, `unauthorized`, `internal`.

Auth: if the server was started with `CUEFLOW_ADMIN_TOKEN`, every `/api/*` request must send
`Authorization: Bearer <token>` (the SSE endpoint also accepts `?token=<token>`). The UI stores the token
in `localStorage["cueflow.token"]` and shows a token prompt on a `401`.

---

## Meta

### `GET /api/v1/meta`
Static info the UI uses to render forms and badges.
```json
{
  "version": "0.1.0",
  "statuses": ["DRAFT","PENDING_REVIEW","APPROVED","REJECTED","QUEUED","PUBLISHED","FAILED"],
  "transitions": {"DRAFT":["PENDING_REVIEW"],"PENDING_REVIEW":["APPROVED","REJECTED"],"APPROVED":["QUEUED"],
                  "QUEUED":["PUBLISHED","FAILED"],"FAILED":["QUEUED"],"REJECTED":[],"PUBLISHED":[]},
  "platforms": [
    {"id":"threads","name":"Threads","max_chars":500,"supports_thread":true,"max_hashtags":1,"requires_media":false},
    {"id":"instagram","name":"Instagram","max_chars":2200,"supports_thread":false,"max_hashtags":30,"requires_media":true},
    {"id":"x","name":"X","max_chars":280,"supports_thread":true,"max_hashtags":3,"requires_media":false},
    {"id":"linkedin","name":"LinkedIn","max_chars":3000,"supports_thread":false,"max_hashtags":5,"requires_media":false}
  ],
  "connector_types": [ConnectorSpec, ...]
}
```

`ConnectorSpec` - plugin descriptor. The UI renders the create/edit form from `fields`.
```json
{
  "type": "postiz_mcp",
  "kind": "publisher",            // publisher | source | search | llm | notifier | runtime
  "name": "Postiz (MCP)",
  "description": "Publish through a Postiz MCP server",
  "fields": [
    {"key":"endpoint_url","label":"MCP endpoint","type":"string","required":true,"secret":false,
     "placeholder":"http://postiz:5000/api/mcp","help":"...","default":null,"options":null}
  ]
}
```
Field `type` is one of: `string`, `password`, `text` (multiline), `number`, `bool`, `select` (uses `options`),
`string_list` (array of strings), `kv` (object of string→string), `json` (arbitrary JSON).
Fields with `"secret": true` are write-only: the API never returns their value (see Connectors).

### `GET /api/v1/health`
`{"status":"ok","db":"ok","time":"..."}`

### `GET /api/v1/stats`
```json
{"posts_by_status":{"DRAFT":0,"PENDING_REVIEW":3,...},"connectors":{"total":4,"active":3},
 "pipelines":{"total":2,"active":1},"last_run":PipelineRun|null}
```

---

## Posts

`Post`:
```json
{
  "id":"uuid","group_id":"uuid|null","pipeline_id":"uuid|null","run_id":"uuid|null",
  "publisher_connector_id":"uuid|null",
  "title":"string","content":"string",
  "thread_parts":["part 1","part 2"],     // Threads/X split; empty = single post
  "hashtags":["ai","golang"],             // without '#'
  "image_prompt":"string",
  "platforms":["threads"],
  "media_urls":["https://..."],
  "accounts":["acc-id"],                  // per-account publishers (CueSocial): accounts to post to
  "topic":"string",                       // Threads topic tag (CueSocial); others ignore it
  "link_url":"https://...",               // link preview card (CueSocial, Threads)
  "status":"PENDING_REVIEW",
  "review_note":"string",                 // reject reason
  "source_context":{"topic":"...","items":[{"title":"...","url":"...","source":"Hacker News"}],"search":[{"title":"...","url":"..."}]},
  "error_message":"string",
  "external_post_ids":{"threads":"123"},
  "attempts":0,"version":3,
  "warnings":["threads: part 2 exceeds 500 characters"],  // computed by server from platform rules
  "scheduled_at":"...|null","published_at":"...|null","created_at":"...","updated_at":"..."
}
```

| Method | Path | Body | Notes |
|---|---|---|---|
| GET | `/posts?status=PENDING_REVIEW&status=FAILED&q=&pipeline_id=&run_id=&limit=50&offset=0` | | → `{"items":[Post],"total":n}`; newest first; `status` repeatable (or comma-separated) |
| GET | `/posts/{id}` | | → `Post` |
| POST | `/posts` | `{title,content,platforms,thread_parts?,hashtags?,media_urls?,accounts?,topic?,link_url?,image_prompt?,scheduled_at?,publisher_connector_id?,submit?:bool}` | manual draft; `submit:true` moves it straight to `PENDING_REVIEW` |
| PATCH | `/posts/{id}` | any of `{title,content,platforms,thread_parts,hashtags,media_urls,accounts,topic,link_url,image_prompt,scheduled_at,publisher_connector_id}` + required `version` | only in `DRAFT`/`PENDING_REVIEW`; `409 version_conflict` if stale; writes `EDITED` audit with diff |
| DELETE | `/posts/{id}` | | only `DRAFT`/`REJECTED` |
| POST | `/posts/{id}/submit` | | `DRAFT → PENDING_REVIEW` |
| POST | `/posts/{id}/approve` | `{scheduled_at?, publisher_connector_id?}` | `PENDING_REVIEW → APPROVED`; worker then moves it to `QUEUED → PUBLISHED/FAILED` |
| POST | `/posts/{id}/reject` | `{reason}` (required) | `PENDING_REVIEW → REJECTED` |
| POST | `/posts/{id}/retry` | | `FAILED → QUEUED` |
| GET | `/posts/{id}/audit` | | → `[AuditLog]` oldest first |

`AuditLog`: `{"id","post_id","action","actor","from_status","to_status","note","payload_diff","created_at"}`
`action` ∈ `GENERATED, CREATED, EDITED, SUBMITTED, APPROVED, REJECTED, QUEUED, DISPATCHED, PUBLISHED, FAILED, RETRIED`.
`actor` ∈ `system_agent`, `system_worker`, `human_reviewer`.
`payload_diff` for `EDITED`: `{"content":{"before":"...","after":"..."}, ...}`.

---

## Connectors (plugins)

`Connector`:
```json
{
  "id":"uuid","name":"My Postiz","kind":"publisher","type":"postiz_mcp","is_active":true,
  "config":{"endpoint_url":"http://...","transport":"streamable_http"},   // non-secret fields only
  "secrets_set":["api_key"],          // secret field keys that currently have a value
  "last_status":"ok|error|null","last_error":"string","last_checked_at":"...|null",
  "created_at":"...","updated_at":"..."
}
```

| Method | Path | Body | Notes |
|---|---|---|---|
| GET | `/connectors?kind=source` | | → `[Connector]` |
| POST | `/connectors` | `{name,type,is_active?,config:{...all fields incl. secrets}}` | server splits secret fields and encrypts them (AES-256-GCM) |
| GET | `/connectors/{id}` | | |
| PATCH | `/connectors/{id}` | `{name?,is_active?,config?}` | secret keys omitted or `""` keep their stored value; send `null` to clear |
| DELETE | `/connectors/{id}` | | |
| POST | `/connectors/{id}/test` | | → `{"ok":true,"message":"Connected, 4 tools","details":{...}}` |
| GET | `/connectors/{id}/tools` | | MCP-backed connectors only → `[{"name","description","agent_allowed":false,"reason":"publishing tool"}]` |
| GET | `/connectors/{id}/tool-permissions` | | MCP tool servers only → `{"tools":[{"name","description","input_schema","read_only","destructive","risky","default","permission"}]}` (`permission` is the stored value or the default) |
| PUT | `/connectors/{id}/tool-permissions` | `{permissions:{"tool":"allow|ask|deny"}}` | replaces the stored permissions; tools left out use the default |
| GET | `/connectors/{id}/accounts` | | Per-account publishers only (connector type with `"accounts":true` in `/meta`; `"accounts_required":true` when every post must name its accounts) → `[{"id","platform","name"}]`; `400` for other connectors, `502` if the service is unreachable |
| POST | `/connectors/{id}/fetch` | | `source` kind only, pulls now → `{"new_items":3}` |
| GET | `/connectors/{id}/items?limit=50` | | `source` kind only → `[SourceItem]` |

`SourceItem`: `{"id","connector_id","external_id","title","url","author","content","published_at","fetched_at","consumed_at"}`

---

## Pipelines (simple schedules)

The dashboard calls these *simple schedules* and lists them on the Schedules page; the API keeps the
name `pipelines`.

`Pipeline`:
```json
{
  "id":"uuid","name":"Daily AI digest","description":"","is_active":true,
  "cron_expr":"0 8 * * *","timezone":"Asia/Taipei",
  "topic":"AI tooling news","instructions":"Write as a senior engineer...","language":"en",
  "platforms":["threads","instagram"],
  "llm_connector_id":"uuid","source_connector_ids":["uuid"],
  "search_connector_id":"uuid|null","search_queries":["LLM agents"],
  "publisher_connector_id":"uuid|null","notifier_connector_id":"uuid|null",
  "lookback_hours":24,"max_items":20,
  "last_run_at":"...|null","next_run_at":"...|null","created_at":"...","updated_at":"..."
}
```

| Method | Path | Body | Notes |
|---|---|---|---|
| GET | `/pipelines` | | → `[Pipeline]` |
| POST | `/pipelines` | Pipeline fields (no id/timestamps) | cron validated (5 fields, or `@daily`, `@every 1h`) |
| GET/PATCH/DELETE | `/pipelines/{id}` | | |
| POST | `/pipelines/{id}/run` | | async → `202 {"run_id":"uuid"}` |
| GET | `/runs?pipeline_id=&limit=50` | | → `[PipelineRun]` newest first |
| GET | `/runs/{id}` | | |

`PipelineRun`: `{"id","pipeline_id","pipeline_name","trigger":"cron|manual","status":"RUNNING|SUCCEEDED|FAILED",
"started_at","finished_at","error","stats":{"items_fetched":12,"items_used":5,"posts_created":2,"tool_calls":3},
"log":[{"at":"...","level":"info","msg":"fetched 12 items from Hacker News"}]}`

---

## Agents (roles)

An agent is a configured role (persona + capabilities + connectors) that works tasks on the board.
`Post` objects also carry `"task_id": "uuid|null"` (the task whose agent drafted it).

`Agent`:
```json
{
  "id":"uuid","name":"Chief","slug":"chief","avatar":"🧭","role":"manager",
  "description":"Short summary shown to other agents for delegation",
  "instructions":"Persona / standing orders (system prompt)",
  "capabilities":["planning","delegation","research","memory"],
  "llm_connector_id":"uuid|null",          // null = first active LLM connector
  "search_connector_id":"uuid|null",       // null = first active search connector
  "runtime_connector_id":"uuid|null",      // external ACP agent runtime; null = built-in LLM loop
  "source_connector_ids":["uuid"],         // sources this agent may read/fetch
  "tool_connector_ids":["uuid"],           // MCP tool servers this agent may use (kind "tools")
  "language":"en","platforms":["threads","instagram"],
  "max_turns":12,"is_active":true,
  "stats":{"open_tasks":2,"running":1},
  "created_at":"...","updated_at":"..."
}
```
`role` ∈ `manager | researcher | writer | editor | custom` (cosmetic grouping). `slug` is unique, `[a-z0-9-]`, used for `@mentions`.

| Method | Path | Body | Notes |
|---|---|---|---|
| GET | `/agents` | | → `[Agent]` |
| POST | `/agents` | Agent fields (no id/stats/timestamps); `slug` derived from name if omitted | |
| GET/PATCH/DELETE | `/agents/{id}` | | PATCH = partial; DELETE unassigns its tasks |
| GET | `/agents/{id}/memories` | | → `[{"id","agent_id","content","created_at"}]` newest first |
| POST | `/agents/{id}/memories` | `{content}` | add a note the agent will always see |
| DELETE | `/agents/{id}/memories/{memoryId}` | | |

`/meta` additionally returns:
```json
"agent_capabilities":[{"id":"research","name":"Research","description":"...","tools":["web_search","read_url","search_materials","fetch_sources"]}, ...],
"agent_roles":["manager","researcher","writer","editor","custom"],
"task_statuses":["BACKLOG","TODO","IN_PROGRESS","BLOCKED","WAITING_HUMAN","REVIEW","DONE","CANCELED"],
"task_priorities":["low","normal","high","urgent"]
```
Capabilities: `research`, `drafting`, `delegation`, `planning`, `memory`. Every agent always has
`add_comment`, `ask_human`, `complete_task`. No capability can publish: drafts go to the review queue.

---

## Tasks (kanban board)

Statuses: `BACKLOG` (idea) → `TODO` (ready; an assigned agent picks it up automatically) → `IN_PROGRESS`
(agent running; system-set only) → `BLOCKED` (waiting for its subtasks) / `WAITING_HUMAN` (agent asked a
question or a run failed) → `REVIEW` (agent finished; human checks) → `DONE`; `CANCELED` is hidden by default.
Suggested board columns: Backlog | To do | In progress | Waiting (BLOCKED + WAITING_HUMAN) | Review | Done.

`Task`:
```json
{
  "id":"uuid","number":12,"title":"...","description":"...","status":"TODO","priority":"normal",
  "labels":["threads"],
  "assignee_agent_id":"uuid|null","assignee":{"id":"uuid","name":"Writer","slug":"writer","avatar":"✍️"}|null,
  "created_by":"human|agent","creator_agent_id":"uuid|null",
  "parent_id":"uuid|null","subtask_counts":{"total":3,"done":1},
  "result":"final summary from the agent","due_at":"...|null","position":1024.0,
  "running":false,"attempts":1,"schedule_id":"uuid|null","version":3,
  "publisher_connector_id":"uuid|null","publish_accounts":["acc-id"],   // defaults for drafts made for this task (subtasks inherit)
  "created_at":"...","updated_at":"...","started_at":"...|null","completed_at":"...|null"
}
```

| Method | Path | Body | Notes |
|---|---|---|---|
| GET | `/tasks?status=&assignee=<agentId|none>&parent_id=&q=&all=1` | | → `{"items":[Task]}` ordered by status then `position`; `DONE`/`CANCELED` older than 14 days omitted unless `all=1` |
| POST | `/tasks` | `{title, description?, assignee_agent_id?, priority?, labels?, due_at?, status?, parent_id?}` | default status: `TODO` if assigned else `BACKLOG`. This is also the "command" entry point (assign to the manager agent) |
| GET | `/tasks/{id}` | | → `Task` + `"subtasks":[Task]`, `"parent":Task|null`, `"posts":[{"id","title","status","platforms"}]` |
| PATCH | `/tasks/{id}` | any of `{title, description, status, priority, labels, assignee_agent_id, due_at, position}` | `status: IN_PROGRESS` is rejected. Moving to `TODO` with an agent queues a run; moving a running task elsewhere stops the run |
| DELETE | `/tasks/{id}` | | stops a run; deletes subtasks |
| GET | `/tasks/{id}/messages` | | → `[TaskMessage]` oldest first |
| POST | `/tasks/{id}/messages` | `{content}` | human comment. A leading `@slug` reassigns to that agent. If the task has an agent and is not `BACKLOG`/`CANCELED`, it moves to `TODO` and the agent responds |
| POST | `/tasks/{id}/run` | | start now (→ `TODO`) |
| POST | `/tasks/{id}/cancel` | | stop the running agent (→ `WAITING_HUMAN`) |
| GET | `/tasks/{id}/runs` | | → `[AgentRun]` newest first |
| GET | `/tasks/{id}/approvals` | | → `[ToolApproval]` oldest first: `{"id","task_id","agent_id","connector_id","server_name","tool","arguments":{},"status":"pending|approved|rejected","result","is_error","note","created_at","decided_at"}` |
| POST | `/tasks/{id}/approvals/{approvalId}` | `{decision:"approve"|"reject", note?}` | approve runs the tool now and posts its result to the thread; either way the agent resumes once nothing is pending. `400` if already decided |

`TaskMessage`: `{"id","task_id","seq","author_type":"human|agent|system|tool","agent_id":"uuid|null",
"agent":{"name","avatar","slug"}|null,"kind":"message|tool_call|tool_result|status|question|result","content":"...",
"data":{...},"created_at"}`. `tool_call.data = {"tool":"web_search","arguments":{...}}`;
`tool_result.data = {"tool":"web_search","is_error":false}` (content is a truncated result).

`AgentRun`: `{"id","task_id","agent_id","status":"RUNNING|SUCCEEDED|FAILED|CANCELED","turns","tool_calls",
"input_tokens","output_tokens","error","started_at","finished_at"}`

---

## Schedules (recurring tasks)

A schedule creates a new task for an agent on a cron expression (agents with `planning` can create them).

`Schedule`: `{"id","name","cron_expr","timezone","is_active","assignee_agent_id","task_title","task_description","publisher_connector_id","publish_accounts",
"priority","created_by":"human|agent","last_run_at","next_run_at","created_at","updated_at"}`

| Method | Path | Body | Notes |
|---|---|---|---|
| GET | `/schedules` | | → `[Schedule]` |
| POST | `/schedules` | Schedule fields | runs at most every 5 minutes |
| GET/PATCH/DELETE | `/schedules/{id}` | | |
| POST | `/schedules/{id}/run` | | spawn a task now → `{"task_id":"uuid"}` |

---

## Media

Images stored by CueFlow. Posts reference them in `media_urls` as `/media/{id}`.

| Method | Path | Body | Notes |
|---|---|---|---|
| POST | `/media` | multipart field `file` | PNG, JPEG, WebP or GIF up to 10 MB → `201 {"id","url":"/media/{id}","mime_type","size_bytes","source":"upload","prompt":""}` |
| POST | `/media/generate` | `{prompt, aspect?, connector_id?}` | `aspect` one of `1:1`, `4:5`, `3:4`, `9:16`, `16:9`, `4:3` (default `1:1`); `connector_id` defaults to the first active `image` connector (`400` if there is none). `502 upstream_error` when the provider fails. Same response with `"source":"generated"` |
| GET | `/media/{id}` | | Outside `/api/v1` and without auth (publishers fetch it); the random id is the capability. Long-lived cache headers |

---

## Usage

### `GET /api/v1/usage?days=30&tz=Asia/Taipei`
Token usage across agent runs and pipeline runs. `days` (1–365, default 30) is the look-back window ending
now; `tz` (IANA, default UTC) sets the day boundaries for `daily`.
```json
{
  "from":"...","to":"...","tz":"Asia/Taipei",
  "totals":{"runs":42,"failed_runs":3,"input_tokens":812345,"output_tokens":90211,"tool_calls":310,
            "unreported_runs":5},
  "daily":[{"date":"2026-10-04","runs":7,"input_tokens":120000,"output_tokens":15000}],
  "by_source":[{"source":"agent","runs":30,"input_tokens":700000,"output_tokens":80000},
               {"source":"pipeline","runs":12,"input_tokens":112345,"output_tokens":10211}],
  "by_agent":[{"agent_id":"uuid","name":"Writer","avatar":"✍️","slug":"writer","runs":12,
               "input_tokens":300000,"output_tokens":40000}],
  "by_model":[{"connector_id":"uuid|null","connector_name":"Gemini on Vertex · gemini-3.1-flash-lite",
               "connector_type":"gemini_vertex","runtime":"builtin|acp","model":"google/gemini-3.1-flash-lite",
               "runs":20,"input_tokens":400000,"output_tokens":50000}],
  "top_tasks":[{"task_id":"uuid","number":12,"title":"...","runs":3,"input_tokens":90000,"output_tokens":9000}]
}
```
- Every list is sorted by `input_tokens + output_tokens` descending (`daily` by date ascending, every day of
  the window present, zero-filled). `top_tasks` has at most 10 rows.
- `unreported_runs`: finished runs that reported no token usage (e.g. external ACP agents that don't report
  usage), so totals may undercount.
- `by_model.connector_name` is `""` when the connector was deleted; `runtime` is `acp` for external agents.

`AgentRun` additionally carries `"connector_id":"uuid|null","model":"...","runtime":"builtin|acp"`.

---

## MCP (external agents)

`POST /mcp` is a stateless Streamable HTTP MCP server for external agents acting for the operator. It is
off unless `CUEFLOW_MCP_TOKEN` is set (404 `mcp_disabled`) and requires `Authorization: Bearer <token>`
(401 otherwise). It is independent of `CUEFLOW_ADMIN_TOKEN`.

| Tool | Does |
|---|---|
| `list_agents` | The crew: slug, name, role, description, active |
| `list_tasks` | Tasks filtered by `status[]`, `assignee` (slug or `none`), `query`, `all`, `limit` |
| `get_task` | One task (`#12`, `12` or id) with description, subtasks, drafts and the last 30 thread messages |
| `create_task` | `title`, `description`, `assignee` slug, `status` `TODO` (default) or `BACKLOG`, `priority`, `labels` |
| `update_task` | Title, description, priority, labels, assignee (`none` unassigns), status `BACKLOG`/`TODO`/`REVIEW`/`DONE`/`CANCELED` |
| `comment_task` | Operator message in the thread, same rules as `POST /tasks/{id}/messages` (`@slug` hand-off, resumes the agent) |
| `run_task` / `stop_task` | Same as `POST /tasks/{id}/run` and `/cancel` |
| `list_drafts` | Posts by status (default `PENDING_REVIEW`), read-only |

Tasks created here have `created_by: "human"`. No tool approves, schedules or publishes posts. Mistakes
(unknown task or agent, a status owned by the runner) come back as tool errors (`isError: true`).

---

## Live updates

### `GET /api/v1/events` (Server-Sent Events)
Event names and `data` payloads:
- `post.updated` → `{"id":"uuid","status":"QUEUED"}`
- `post.deleted` → `{"id":"uuid"}`
- `run.updated` → `{"id":"uuid","pipeline_id":"uuid","status":"SUCCEEDED"}`
- `connector.updated` → `{"id":"uuid"}`
- `task.updated` → `{"id":"uuid","status":"IN_PROGRESS"}`
- `task.deleted` → `{"id":"uuid"}`
- `task.message` → `{"task_id":"uuid","id":"uuid"}`
- `agent.updated` → `{"id":"uuid"}`
- `schedule.updated` → `{"id":"uuid"}`
A `: ping` comment is sent every 20s.
