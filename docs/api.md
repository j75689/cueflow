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
  "kind": "publisher",            // publisher | source | search | llm | notifier
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
| POST | `/posts` | `{title,content,platforms,thread_parts?,hashtags?,media_urls?,image_prompt?,scheduled_at?,publisher_connector_id?,submit?:bool}` | manual draft; `submit:true` moves it straight to `PENDING_REVIEW` |
| PATCH | `/posts/{id}` | any of `{title,content,platforms,thread_parts,hashtags,media_urls,image_prompt,scheduled_at,publisher_connector_id}` + required `version` | only in `DRAFT`/`PENDING_REVIEW`; `409 version_conflict` if stale; writes `EDITED` audit with diff |
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
| POST | `/connectors/{id}/fetch` | | `source` kind only, pulls now → `{"new_items":3}` |
| GET | `/connectors/{id}/items?limit=50` | | `source` kind only → `[SourceItem]` |

`SourceItem`: `{"id","connector_id","external_id","title","url","author","content","published_at","fetched_at","consumed_at"}`

---

## Pipelines (scheduled agent jobs)

`Pipeline`:
```json
{
  "id":"uuid","name":"Daily AI digest","description":"","is_active":true,
  "cron_expr":"0 8 * * *","timezone":"Asia/Taipei",
  "topic":"AI tooling news","instructions":"Write as a senior engineer...","language":"zh-TW",
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

## Live updates

### `GET /api/v1/events` (Server-Sent Events)
Event names and `data` payloads:
- `post.updated` → `{"id":"uuid","status":"QUEUED"}`
- `post.deleted` → `{"id":"uuid"}`
- `run.updated` → `{"id":"uuid","pipeline_id":"uuid","status":"SUCCEEDED"}`
- `connector.updated` → `{"id":"uuid"}`
A `: ping` comment is sent every 20s.
