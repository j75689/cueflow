# CueFlow REST API (v1)

Base path: `/api/v1` (no trailing slashes). All bodies are JSON. Timestamps are RFC 3339 strings. IDs are UUID strings.
Errors: non-2xx with `{"error": {"code": "invalid_transition", "message": "..."}}`.
Common codes: `not_found`, `invalid_argument`, `invalid_transition`, `version_conflict`, `unauthorized`, `internal`.

Auth: if the server was started with `CUEFLOW_ADMIN_TOKEN`, every `/api/*` request must send
`Authorization: Bearer <token>` (the SSE endpoint also accepts `?token=<token>`). The UI stores the token
in `localStorage["cueflow.token"]` and shows a token prompt on a `401`.

With `CUEFLOW_CF_ACCESS_TEAM_DOMAIN` and `CUEFLOW_CF_ACCESS_AUD` set, a request is also accepted when it carries a
valid Cloudflare Access JWT for that application (the `Cf-Access-Jwt-Assertion` header, or the `CF_Authorization`
cookie a signed-in browser sends): the token or the login is enough, either one. A JWT for another application,
another team, or an expired one is refused. `/api/v1/health`, `/api/runner/*`, `/mcp*`, `/media/*` and `/internal/*`
are not affected: they are open or authenticate themselves.

Orgs: agents, tasks, schedules, deliverables and runs belong to an org. List and create endpoints for
them take `?org=<id or slug>`; without it they use the default org (the oldest). An unknown org is a
`400 invalid_argument`. Endpoints that address one item by id work in any org. Connectors, source items,
media and usage are global and ignore `org`. Items carry `"org_id":"uuid"`. The UI stores the chosen org in
`localStorage["cueflow.org"]` and also accepts `?org=` in a page URL.

---

## Orgs

### `GET /api/v1/orgs`
`[{"id":"uuid","name":"Default","slug":"default","created_at":"...","updated_at":"..."}]`, oldest first; the
first one is the default org.

### `POST /api/v1/orgs`
`{"name":"CueFlow Dev","slug":"cueflow-dev","seed_agents":true}`. `slug` is optional (derived from the name);
`seed_agents` creates the starter Chief agent. `201` with the org. To add a whole team, import a template
(`POST /agents/template?org=...`).

### `PATCH /api/v1/orgs/{id}`
`{"name":"...","slug":"..."}`, either field optional.

### `DELETE /api/v1/orgs/{id}`
`204`. Only an empty org (no agents, tasks, schedules or deliverables) can be deleted, and never the last
one; otherwise `400 invalid_argument`.

Within an org, agent slugs are unique and task numbers count from `#1`. A subtask always lives in its
parent's org; assigning an agent from another org (task create/update, schedules) is a `400`.

---

## Meta

### `GET /api/v1/meta`
Static info the UI uses to render forms and badges. `modules` lists the feature modules mounted on this
server; `platforms` comes from the `content` module and the `deliverable_*` keys from the `deliverables` module.
```json
{
  "version": "0.1.0",
  "modules": ["content", "deliverables"],
  "connector_types": [ConnectorSpec, ...],
  "agent_capabilities": [AgentCapability, ...],
  "platforms": [
    {"id":"threads","name":"Threads","max_chars":500,"supports_thread":true,"max_hashtags":1,"requires_media":false},
    {"id":"instagram","name":"Instagram","max_chars":2200,"supports_thread":false,"max_hashtags":30,"requires_media":true},
    {"id":"x","name":"X","max_chars":280,"supports_thread":true,"max_hashtags":3,"requires_media":false},
    {"id":"linkedin","name":"LinkedIn","max_chars":3000,"supports_thread":false,"max_hashtags":5,"requires_media":false}
  ]
}
```

`ConnectorSpec` - plugin descriptor. The UI renders the create/edit form from `fields`.
```json
{
  "type": "mcp_publisher",
  "kind": "publisher",            // publisher | source | search | llm | notifier | runtime | image | tools | action
  "name": "MCP publisher",
  "description": "Publishes approved posts through an MCP server that follows the publisher contract",
  "fields": [
    {"key":"endpoint_url","label":"MCP endpoint","type":"string","required":true,"secret":false,
     "placeholder":"https://social.example.com/mcp","help":"...","default":null,"options":null}
  ]
}
```
Field `type` is one of: `string`, `password`, `text` (multiline), `number`, `bool`, `select` (uses `options`),
`string_list` (array of strings), `kv` (object of string→string), `json` (arbitrary JSON).
Fields with `"secret": true` are write-only: the API never returns their value (see Connectors).

### `GET /api/v1/health`
`{"status":"ok","db":"ok","time":"..."}`

### `GET /api/v1/stats?org=`
Connectors are global; each mounted module adds its own counts for the org (the deliverables module adds
`deliverables_by_status`).
```json
{"connectors":{"total":4,"active":3},
 "deliverables_by_status":{"DRAFT":0,"PENDING_REVIEW":3,"APPROVED":0,"REJECTED":1,"QUEUED":0,"DONE":12,"FAILED":0}}
```

---

## Deliverables

The general form of "an agent produces something, a human approves it, then the system acts". A deliverable
has a `kind` (registered by modules; `document`, a Markdown text, is built in), a JSON `payload` the kind
validates, and a review state machine:
`DRAFT → PENDING_REVIEW → APPROVED | REJECTED → QUEUED → DONE | FAILED`, `FAILED → QUEUED` on retry. After
approval the worker hands the item, under a grant, to an **action connector** (kind `action`, e.g. `mcp_action`):
the item's `action_connector_id`, else the first active one. With no action connector configured an approved
item is `DONE` at once: the review itself was the point. Kinds built in: `document` (Markdown) and, from the
content module, `post`: a social post (content, thread parts, hashtags, platforms, media, accounts, topic, link;
validated with the platform rules) whose action is publishing through the publisher connector named in its
payload, else the first active one. Agents with the `post_deliverables` capability (`create_post_draft`,
`list_posts`, `revise_draft`) write these. `/meta` lists `deliverable_kinds`, `deliverable_statuses`
and `deliverable_transitions`; `/stats` adds `deliverables_by_status`; `GET /tasks/{id}` adds `deliverables`.

| Method | Path | Body | Notes |
|---|---|---|---|
| GET | `/deliverables?status=&kind=&task_id=&q=&limit=&offset=` | | `{"items":[Deliverable],"total":n}` |
| POST | `/deliverables` | `{"kind","title","payload":{...},"task_id"?,"action_connector_id"?,"submit"?}` | `201`; `submit` goes straight to `PENDING_REVIEW` |
| GET | `/deliverables/{id}` | | |
| PATCH | `/deliverables/{id}` | `{"version","title"?,"payload"?,"action_connector_id"?}` | Only `DRAFT` / `PENDING_REVIEW`; `409` on a stale version; the diff is audited |
| DELETE | `/deliverables/{id}` | | `DRAFT` and `REJECTED` only |
| POST | `/deliverables/{id}/submit` `/approve` `/reject` `/retry` | approve: `{"note"?,"action_connector_id"?}`, reject: `{"reason"?}` | Same rules as posts. Approve answers `400 invalid_argument` when the kind refuses the item as it stands (a `post` with no accounts for a publisher that needs them, or no publisher to use) |
| POST | `/deliverables/bulk` | `{"action":"approve"\|"reject","ids":[id,...],"note"?,"reason"?}` | Up to 200 ids, each its own transition: `{"ok":[id],"failed":[{"id","code","message"}]}` with the single endpoints' codes (`invalid_transition`, `not_found`, `invalid_argument`); `200` even when every id failed. A database error stops the batch: that id is `internal`, the rest `not_attempted` |
| GET | `/deliverables/{id}/audit` | | `[{action, actor, from_status, to_status, note, payload_diff, created_at}]` |

```json
{"id":"uuid","org_id":"uuid","task_id":"uuid|null","kind":"document","title":"Launch plan",
 "payload":{"content":"# Plan ..."},"status":"DONE","review_note":"","error_message":"",
 "action_connector_id":"uuid|null","external_ids":{"page":"wiki-1"},"result":{"url":"https://..."},
 "attempts":1,"version":4,"created_by":"system_agent","acted_at":"...","created_at":"...","updated_at":"..."}
```

Agents with the `deliverables` capability get `create_deliverable`, `list_deliverables` and `revise_deliverable`;
nothing they can call acts on an item.

## Connectors (plugins)

`Connector`:
```json
{
  "id":"uuid","name":"My publisher","kind":"publisher","type":"mcp_publisher","is_active":true,
  "config":{"endpoint_url":"https://..."},   // non-secret fields only
  "secrets_set":["auth_token"],       // secret field keys that currently have a value
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

## Agents (roles)

An agent is a configured role (persona + capabilities + connectors) that works tasks on the board.

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
| POST | `/agents/template` | A team template file: `{"version":1,"name","description","agents":[{"name","slug","avatar","role","description","instructions","capabilities","platforms","language","max_turns"}]}` (example: `templates/social-content.json`) | Imports the roles the org does not have yet (matched by slug), so importing twice or over a hand-made team only adds what is missing → `200 {"created":[Agent, ...],"skipped":["writer"]}`; a malformed file or an unknown capability/role/platform is `400` |
| GET | `/agents/template` | | The org's agents as a template file (no ids, connectors or stats), `Content-Disposition: attachment` |
| GET/PATCH/DELETE | `/agents/{id}` | | PATCH = partial; DELETE unassigns its tasks |
| GET | `/agents/{id}/memories` | | → `[{"id","agent_id","content","created_at"}]` newest first |
| POST | `/agents/{id}/memories` | `{content}` | add a note the agent will always see |
| DELETE | `/agents/{id}/memories/{memoryId}` | | |
| GET | `/agents/{id}/tokens` | | → `[{"id","agent_id","name","created_at","last_used_at"}]`; never includes the token itself |
| POST | `/agents/{id}/tokens` | `{name}` | → `201 {"id","name","token":"cfa_...","mcp_url","created_at"}`. The token is shown once. See [Self-driven agents](#self-driven-agents-agent-tokens) |
| DELETE | `/agents/{id}/tokens/{tokenId}` | | Revokes at once |

`runtime_connector_id` may point at a connector of kind `runtime`: `acp_agent` (CueFlow launches the agent on its
own host), `runner_pool` (a registered [Runner](#runners) launches it) or `external` (the agent drives itself).

`/meta` additionally returns:
```json
"agent_capabilities":[{"id":"research","name":"Research","description":"...","tools":["web_search","read_url"]}, ...],
"agent_roles":["manager","researcher","writer","editor","custom"],
"task_statuses":["BACKLOG","TODO","IN_PROGRESS","BLOCKED","WAITING_HUMAN","REVIEW","DONE","CANCELED"],
"task_priorities":["low","normal","high","urgent"]
```
Capabilities come from the core (`research`, `delegation`, `planning`, `memory`) and the mounted modules
(`sources`, `post_deliverables`, `imaging`, `deliverables`). Every agent always has `add_comment`, `ask_human`,
`complete_task`. No capability can publish or act: everything an agent produces waits for a human under Deliverables.

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
| GET | `/tasks/{id}` | | → `Task` + `"subtasks":[Task]`, `"parent":Task|null`, `"deliverables":[{"id","kind","title","status"}]` |
| PATCH | `/tasks/{id}` | any of `{title, description, status, priority, labels, assignee_agent_id, due_at, position}` | `status: IN_PROGRESS` is rejected. Moving to `TODO` with an agent queues a run; moving a running task elsewhere stops the run |
| DELETE | `/tasks/{id}` | | stops a run; deletes subtasks |
| GET | `/tasks/{id}/messages` | | → `[TaskMessage]` oldest first |
| POST | `/tasks/{id}/messages` | `{content}` | human comment. A leading `@slug` reassigns to that agent. If the task has an agent and is not `BACKLOG`/`CANCELED`, it moves to `TODO` and the agent responds |
| POST | `/tasks/{id}/run` | | start now (→ `TODO`) |
| POST | `/tasks/{id}/cancel` | | stop the running agent (→ `WAITING_HUMAN`) |
| GET | `/tasks/{id}/runs` | | → `[AgentRun]` newest first |
| GET | `/tasks/{id}/files` | | → `{"files":[TaskFile],"total"}` by name, at most 500: the task's own files and its ancestors' (the nearest wins for a shared name). `TaskFile`: `{"id","task_id","task_number","name","mime_type","size_bytes","created_by","created_at","updated_at","own","description"}`; `own` is false for an ancestor's file, `description` is the start of the first line |
| GET | `/tasks/{id}/files/content?name=&offset=` | | One page of a file this task can read: `{"name","task_id","size_bytes","mime_type","offset","next_offset","text","binary"}`, up to 64 KB from byte `offset`, cut at character boundaries (an offset inside a character moves to the next one). `next_offset` is `null` at the end; `binary` is true, with empty `text`, for bytes that aren't UTF-8 |
| GET | `/tasks/{id}/files/download?name=` | | The file as an attachment (`application/octet-stream`, `Content-Length` from `size_bytes`), streamed |
| DELETE | `/tasks/{id}/files?name=` | | Deletes one of the task's own files and its bytes. `403` for an ancestor's file, `404` if there is none |

Files are addressed by task and `name` (which may contain `/`), never by file id, and are never served on `/media`. A task reads its own files and its ancestors'; it changes only its own. Changes are announced as `task.file` events (`{"task_id","name","action":"written|deleted"}`) in the task's org.
| GET | `/tasks/{id}/approvals` | | → `[ToolApproval]` oldest first: `{"id","task_id","agent_id","connector_id","server_name","tool","called_as","arguments":{},"status":"pending|approved|rejected","result","is_error","note","created_at","decided_at","result_file"}`. `tool` is the server's name for the tool and `called_as` the name the agent called it by; `result_file` is the task file a result over 8,000 characters was saved to, or `""` |
| POST | `/tasks/{id}/approvals/{approvalId}` | `{decision:"approve"|"reject", note?}` | approve answers at once (the approval has an empty `result` until the call finishes) and the tool runs out of band (a queued `approval` job in the cloud, a goroutine otherwise) and posts its result to the thread, so a render that takes minutes does not hold the request; either way the agent resumes once nothing is pending. `400` if already decided |

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

## Runners

A Runner is a machine you own that works agents' tasks: `cueflow runner run` polls CueFlow for jobs (every 20 s by default, each claim answered at once; `poll_interval: 0` in `runner.json` or `CUEFLOW_RUNNER_POLL_INTERVAL=0` long-polls instead)
(outbound HTTP only), launches the agent CLI locally (Claude Code, Codex, ...) with that machine's own
credentials, and reports back. Point an agent at runners with a `runner_pool` runtime connector
(`labels`, `preset`, `model`, `timeout_minutes`, `queue_timeout_minutes`, `allow_web`). `model` (also a field of
`acp_agent`) is chosen through the agent's ACP `model` session option, by id or display name; the run fails and
lists the available models if the agent does not offer it. A runner's local `models` map pins its own and wins.
A runner takes a job only if it
has every label the connector asks for and serves the job's org. The agent still works through CueFlow's
per-run MCP tools, so review, permissions and "agents never publish" hold. Runners are global (not per org);
a runner's `org_ids` can restrict it (empty = every org).

`Runner`:
```json
{"id":"uuid","name":"mbp","labels":["claude-code","mac"],"org_ids":[],"is_active":true,"max_concurrency":1,
 "version":"0.2.0","platform":"darwin/arm64","last_seen_at":"...","created_at":"...","online":true,"running_jobs":0}
```
`online` = polled within the last 90 s.

| Method | Path | Body | Notes |
|---|---|---|---|
| GET | `/runners` | | → `[Runner]` |
| POST | `/runners/registrations` | `{labels?, org_ids?, ttl_minutes?}` | → `201 {"token":"cfreg_...","expires_at","url","command"}`. One-time token (default 1 h, max 24 h); `command` is the `cueflow runner register ...` line to run on the machine |
| PATCH | `/runners/{id}` | `{name?, labels?, org_ids?, is_active?, max_concurrency?}` | |
| DELETE | `/runners/{id}` | | Revokes: the token stops working and its running jobs are canceled |
| GET | `/tasks/{id}/runner-jobs` | | → jobs of the task: `id`, `status` (`queued`, `running`, `canceling`, `done`, `failed`, `canceled`, `expired`), `labels`, `runner_name`, timestamps |

### Runner protocol (`/api/runner/v1`, protocol 1)

Authenticated by the runner's own token (`Authorization: Bearer cfr_...`), not by `CUEFLOW_ADMIN_TOKEN`; a
reverse proxy that guards the admin API must let this path through. A runner speaking another protocol gets
`426`. Tokens are stored as SHA-256 only.

| Method | Path | Notes |
|---|---|---|
| POST | `/register` | `{token, name, version, platform, labels, concurrency, protocol}` trades a registration token for a runner token (`401` if used/expired). Labels set at registration win; the runner's own are used only when the registration has none |
| POST | `/jobs/claim` | `{protocol, wait?}`; holds the request up to `wait` seconds (0–25) for work to arrive, 25 when `wait` is absent (the long poll of older runners) → `200 {id, task_id, title, preset, prompt, mcp_path, timeout_seconds, allow_web}` or `204`. `401` once the runner is revoked or disabled. The runner reaches the tool server at its registered URL + `mcp_path` |
| POST | `/jobs/{id}/events` | `{events:[{kind: message\|tool_call\|tool_result\|system, content, data}]}` → thread messages; also extends the lease. → `{"cancel":bool}` |
| POST | `/jobs/{id}/heartbeat` | Extends the 2-minute lease (send every 30 s) → `{"cancel":bool}`; `cancel` means stop the agent |
| POST | `/jobs/{id}/complete` | `{stop_reason, final_text, agent_name, tool_calls, input_tokens, output_tokens, error, timed_out, canceled}` → `{"accepted":bool}` |

Local policy always wins: the runner's own config (`allow_presets`, `deny_web`, `commands`) can only narrow what
the server asks for, and shell/file tools are refused as for any external agent, unless the machine's owner set
`allow_shell` (below). If no runner takes a job within
`queue_timeout_minutes`, or a runner stops heartbeating, the task fails with that reason.

The runner's `runner.json` (Linux `~/.config/cueflow/`, macOS `~/Library/Application Support/cueflow/`, or
`CUEFLOW_RUNNER_CONFIG`) can restrict `allow_presets`, set `deny_web`, map a preset to a `commands` path, pick an
ACP `auth_methods` entry per preset and provide credentials in `env`; the server cannot widen any of it. For a
coding agent, `allow_shell: true` (or `register --allow-shell`, or `CUEFLOW_RUNNER_ALLOW_SHELL=true`) lets agents
run commands and edit files in the directory the runner was started in; it is off by default and applies to every job
the runner takes, so give it a runner of its own in a container ([deploy/runner](../deploy/runner/README.md)). For
example, Google Antigravity's official ACP server (from the
[ACP registry](https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json)) is
`"commands": {"custom": "/path/to/agy_acp_server.par"}, "auth_methods": {"custom": "oauth-personal"}`, with the
agent's runner pool set to the `custom` preset. `cueflow runner status` checks the connection; revoking a runner in
the dashboard stops it at once.

---

## Media

Images stored by CueFlow. Posts reference them in `media_urls` as `/media/{id}`. Bytes live in the database by
default; set `CUEFLOW_MEDIA_STORAGE=file:///data/media` to keep new media on disk, and move existing media with
`cueflow media migrate --to file:///data/media` (or `--to db` to move it back). The URLs do not change.

Old media can be pruned: `cueflow media prune --older-than 90d [--dry-run]` deletes media older than that which no unfinished post uses. With `CUEFLOW_MEDIA_RETENTION_DAYS` set (and `CUEFLOW_TICK=external`) the same pruning runs on every tick.

**Registering a file written straight to the bucket** (GCS storage only): a trusted service of yours (a video renderer, for example) writes `gs://<bucket>/uploads/<file>` itself and then calls `POST /internal/media/register` with `{"object":"uploads/<file>"}` and a Google ID token (audience = the CueFlow URL) of an account listed in `CUEFLOW_MEDIA_REGISTER_ACCOUNTS`. CueFlow checks the type and size from the first bytes, moves the object into the media store inside Google Storage (the bytes do not pass through CueFlow) and answers `201` like `POST /media`. `400` for a missing object, a name outside `uploads/`, or a file that is not a supported image or MP4; `401/403` for a bad token. Give the bucket a lifecycle rule that deletes `uploads/` after a day to clean up refused files.

| Method | Path | Body | Notes |
|---|---|---|---|
| POST | `/media` | multipart field `file` | PNG, JPEG, WebP or GIF up to 10 MB, or MP4 up to 100 MB → `201 {"id","url","mime_type","size_bytes","source":"upload","prompt":""}`; `url` is `/media/{id}` for images and `/media/{id}.mp4` for videos |
| POST | `/media/generate` | `{prompt, aspect?, connector_id?}` | `aspect` one of `1:1`, `4:5`, `3:4`, `9:16`, `16:9`, `4:3` (default `1:1`); `connector_id` defaults to the first active `image` connector (`400` if there is none). `502 upstream_error` when the provider fails. Same response with `"source":"generated"` |
| GET | `/media/{id}` | | Outside `/api/v1` and without auth (publishers fetch it); the random id is the capability. Long-lived cache headers; Range requests supported (video seeking) |
| DELETE | `/media/{id}` | | Removes the row and its file (`{id}` may end in `.mp4`) → `204`. `409 in_use` while a post that is not `PUBLISHED` or `REJECTED` still lists it in `media_urls`; `404` when unknown |

---

## Usage

### `GET /api/v1/usage?days=30&tz=Asia/Taipei`
Token usage across agent runs. `days` (1–365, default 30) is the look-back window ending
now; `tz` (IANA, default UTC) sets the day boundaries for `daily`.
```json
{
  "from":"...","to":"...","tz":"Asia/Taipei",
  "totals":{"runs":42,"failed_runs":3,"input_tokens":812345,"output_tokens":90211,"tool_calls":310,
            "unreported_runs":5,"cache_read_tokens":700000,"cache_write_tokens":60000},
  "daily":[{"date":"2026-10-04","runs":7,"input_tokens":120000,"output_tokens":15000}],
  "by_source":[{"source":"agent","runs":42,"input_tokens":812345,"output_tokens":90211}],
  "by_org":[{"org_id":"uuid","name":"Default","slug":"default","runs":42,"input_tokens":812345,"output_tokens":90211}],
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
- `input_tokens` is the whole prompt of every model call, whether or not the provider served it from its
  prompt cache. Every row (totals, `daily` and each `by_*` and `top_tasks` row) also carries
  `cache_read_tokens` and `cache_write_tokens` (omitted above for brevity): the parts of it read from and
  written to the cache, which providers bill at different rates (Claude: about 0.1× and 1.25×), so a
  long cached run reports many input tokens but costs far less than the same count uncached.
- `unreported_runs`: finished runs that reported no token usage (e.g. external ACP agents that don't report
  usage), so totals may undercount.
- `by_model.connector_name` is `""` when the connector was deleted; `runtime` is `acp` for external agents.

`AgentRun` additionally carries `"connector_id":"uuid|null","model":"...","runtime":"builtin|acp"`, and
`"cache_read_tokens"` / `"cache_write_tokens"` (the parts of its `input_tokens` read from and written to the
prompt cache).

---

## MCP (external agents)

`POST /mcp` is a stateless Streamable HTTP MCP server for external agents acting for the operator. It is
off unless `CUEFLOW_MCP_TOKEN` is set (404 `mcp_disabled`) and requires `Authorization: Bearer <token>`
(401 otherwise). It is independent of `CUEFLOW_ADMIN_TOKEN`.

| Tool | Does |
|---|---|
| `list_orgs` | Orgs: slug, name, whether it is the default |
| `list_agents` | The org's crew: slug, name, role, description, active |
| `list_tasks` | Tasks filtered by `status[]`, `assignee` (slug or `none`), `query`, `all`, `limit` |
| `get_task` | One task (`#12`, `12` or id) with description, subtasks, its deliverables and the last 30 thread messages |
| `create_task` | `title`, `description`, `assignee` slug, `status` `TODO` (default) or `BACKLOG`, `priority`, `labels` |
| `update_task` | Title, description, priority, labels, assignee (`none` unassigns), status `BACKLOG`/`TODO`/`REVIEW`/`DONE`/`CANCELED` |
| `comment_task` | Operator message in the thread, same rules as `POST /tasks/{id}/messages` (`@slug` hand-off, resumes the agent) |
| `run_task` / `stop_task` | Same as `POST /tasks/{id}/run` and `/cancel` |
| `list_deliverables` | Deliverables by status and kind (default `PENDING_REVIEW`), read-only |

Every tool except `list_orgs` takes an optional `org` slug (default org when blank); task numbers and agent
slugs are resolved within it. Tasks created here have `created_by: "human"`. No tool approves, schedules or publishes posts. Mistakes
(unknown task or agent, a status owned by the runner) come back as tool errors (`isError: true`).

### Self-driven agents (agent tokens)

`/mcp` also accepts an **agent token** (`cfa_...`, from `POST /agents/{id}/tokens`), independent of
`CUEFLOW_MCP_TOKEN`. The caller then *is* that agent and sees only its own tasks. Give the agent the
`external` runtime connector ("External (self-driven)"): CueFlow never starts it, and its tasks wait in
`TODO` until it claims them.

| Tool | Does |
|---|---|
| `list_my_tasks` | Tasks assigned to the agent (default `TODO`, `IN_PROGRESS`, `WAITING_HUMAN`, `BLOCKED`) |
| `claim_task` | Claims the most urgent `TODO` task (or `task`) → `IN_PROGRESS` for 30 min, opens an agent run, returns the agent's instructions and the task brief |
| `get_task` | The claimed task's brief with the latest thread |
| every tool the agent's capabilities give a built-in agent (`add_comment`, `ask_human`, `create_post_draft`, `revise_draft`, `complete_task`, ...) | Same behaviour and limits, scoped to the claimed task; takes an optional `task` (number or id; required when several are in progress) |

Each call extends the claim. `complete_task` moves the task to Review, `ask_human` to Waiting for human, as for
built-in agents; a claim that goes quiet for 30 minutes returns the task to `TODO` and fails its run.
Revoking the token cuts access immediately. An agent token cannot reach the operator tools above.

---

## Internal endpoints (cloud mode only)

With `CUEFLOW_TICK=external` (and `CUEFLOW_QUEUE=cloudtasks`) the server also serves two endpoints for Google
Cloud. They require `Authorization: Bearer <Google ID token>` whose audience is `CUEFLOW_INTERNAL_AUDIENCE` and whose
email is one of `CUEFLOW_INTERNAL_SERVICE_ACCOUNTS` (`401` invalid or missing, `403` not allowed). They are not served
at all in the local deployment, and are not covered by `CUEFLOW_ADMIN_TOKEN`.

| Method | Path | Notes |
|---|---|---|
| POST | `/internal/tick` | Cloud Scheduler, every minute: fires due schedules (and the cron rows modules register) once, closes abandoned work, runs the modules' sweeps, and enqueues waiting work. `204` |
| POST | `/internal/work/{kind}/{id}` | Cloud Tasks delivery; `kind` is `crew`, `schedule`, `approval` or a kind a module registers (`deliver` for the deliverables module). `204` when done (also when there was nothing to do); `500` makes Cloud Tasks retry; `404` unknown kind; `400` bad id |

`GET /media/{id}` answers `302` to a short-lived signed URL when the media is stored in GCS.

---

## Live updates

### `GET /api/v1/events?org=` (Server-Sent Events)
With `org` (id or slug) the stream carries that org's events plus the global ones (`connector.*`, `org.*`,
`runner.*`); without it, every event of every org. An org that does not exist gets the global events only. Every
event except the global ones has `org_id` in its `data` (shown below for the first one only).

Event names and `data` payloads:
- `deliverable.updated` → `{"id":"uuid","status":"QUEUED","org_id":"uuid"}`
- `deliverable.deleted` → `{"id":"uuid"}`
- `connector.updated` → `{"id":"uuid"}`
- `task.updated` → `{"id":"uuid","status":"IN_PROGRESS"}`
- `task.deleted` → `{"id":"uuid"}`
- `task.message` → `{"task_id":"uuid","id":"uuid"}`
- `agent.updated` → `{"id":"uuid"}`
- `org.updated` → `{"id":"uuid"}` (created, renamed or deleted)
- `schedule.updated` → `{"id":"uuid"}`
- `runner.updated` → `{"id":"uuid"}` (registered, edited or revoked)

Events travel through PostgreSQL `LISTEN/NOTIFY` (channel `cueflow_events`), so several CueFlow instances on one
database all deliver every event to their own clients.
A `: ping` comment is sent every 20s.
