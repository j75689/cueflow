# Configuration

CueFlow is configured with environment variables. The Docker defaults are in `deploy/.env.example`;
for Cloud Run see [deploy/cloudrun/README.md](../deploy/cloudrun/README.md).

## Core

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | `postgres://cueflow:cueflow@localhost:5432/cueflow?sslmode=disable` | PostgreSQL |
| `DATABASE_SESSION_URL` | empty | A session-mode connection for `LISTEN` and the migration lock. Set it when `DATABASE_URL` is a transaction-mode pooler (Supabase port 6543, PgBouncer `pool_mode=transaction`); the main pool then runs without named prepared statements. Empty: everything uses `DATABASE_URL` |
| `CUEFLOW_SECRET_KEY` | *(required)* | 32-byte key (base64/hex) for connector secrets. Back it up |
| `CUEFLOW_SECRET_KEY_PREVIOUS` | | Old key(s), comma-separated, still accepted for reading during a rotation |
| `CUEFLOW_ADDR` | `127.0.0.1:3000` | Listen address |
| `CUEFLOW_ADMIN_TOKEN` | | Require `Authorization: Bearer <token>` for the API/UI |
| `CUEFLOW_MCP_TOKEN` | | Enables the operator MCP endpoint `/mcp` for external agents (Bearer token) |
| `CUEFLOW_MCP_REQUEST_TIMEOUT` | `10m` | The longest one request to the MCP endpoints (`/mcp`, `/mcp/run/…`) may stay open (a Go duration, at least `30s`; a value that is not a duration (`600`, `5min`) stops startup with an error). A run's or agent token's tool call (`/mcp/run/…`, `/mcp` with an agent token) still running then is canceled where it stands; the operator's `/mcp` tools are not cut by it. A client that disconnects does not cancel a tool call. Keep it under Cloud Run's 30 min request timeout; 30 min or more logs a warning at startup. A tool connector's call made for such a request (an `mcp_tools` server's tool, say) ends 5 s before the request does, if that comes before its own timeout, and the agent is told which limit ran out; in-process runs keep the connector's own timeout |
| `CUEFLOW_PUBLIC_URL` | `http://localhost:3000` | Links in emails |
| `CUEFLOW_DIRECT_URL` | `CUEFLOW_PUBLIC_URL` | How machines reach the server (runner registration and MCP commands, media URLs): they cannot pass a browser login such as Cloudflare Access |
| `CUEFLOW_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CUEFLOW_DB_MAX_CONNS` | driver default | Cap on this process's main pool (use ~5 per instance on a small database; behind a session pooler every one counts against its client limit) |

## Publishing and scheduling

| Variable | Default | Purpose |
|---|---|---|
| `CUEFLOW_DISPATCH_INTERVAL` | `3s` | Deliverables worker poll interval. A Go duration; anything else keeps the default and logs a warning at startup |
| `CUEFLOW_DISPATCH_LEASE` | `10m` | Max time acting on one approved deliverable (publishing a post, running an action connector) may take before the worker is considered crashed. A Go duration; anything else keeps the default and logs a warning at startup |
| `CUEFLOW_PUBLISH_TIMEOUT` | `5m` | Timeout of one publish call (Instagram can take minutes); keep it below the lease. A Go duration; anything else keeps the default and logs a warning at startup |
| `CUEFLOW_DISABLE_SCHEDULER` | `false` | Run API/worker without cron |

## Agents

| Variable | Default | Purpose |
|---|---|---|
| `CUEFLOW_AGENT_CONCURRENCY` | `3` | Agent tasks that may run at the same time |
| `CUEFLOW_CONTEXT_HANDOFF` | `0.8` | How full a run's context (latest prompt over the model's window) may get before the run hands off to a fresh one; the task page colours the size from here. A number between 0 and 1; anything else keeps 0.8 and logs a warning at startup |
| `CUEFLOW_MODULES` | `content,deliverables` | Feature modules to mount, comma-separated (`all`, `none`, or any of `content`, `deliverables`; `content` needs `deliverables`). A module that is not mounted has no routes, agent tools, workers or cron; its tables stay, and the dashboard hides its pages (`/meta` lists what is mounted) |
| `CUEFLOW_AGENT_ALLOW_PRIVATE_URLS` | `false` | Let agents' `read_url` reach LAN/loopback addresses |

## Media

| Variable | Default | Purpose |
|---|---|---|
| `CUEFLOW_MEDIA_BASE_URL` | `CUEFLOW_PUBLIC_URL` | Prefix for images stored in CueFlow when handing them to publishers; must be reachable from the publisher (compose sets `http://cueflow:3000`) |
| `CUEFLOW_MEDIA_STORAGE` | | Where new images/videos and task files are kept: empty = the database, `file:///data/media`, or `gcs://bucket/prefix`. Move existing media with `cueflow media migrate --to file:///data/media` |
| `CUEFLOW_MEDIA_RETENTION_DAYS` | | With `CUEFLOW_TICK=external`: delete media older than this many days that no unfinished deliverable uses. Off when empty or 0. Manual: `cueflow media prune --older-than 90d [--dry-run]` |
| `CUEFLOW_MEDIA_REGISTER_ACCOUNTS` | | Comma-separated Google service accounts allowed to call `POST /internal/media/register` (adopt a file written to the bucket's `uploads/`). Needs `CUEFLOW_TICK=external` and GCS media |

See [modules.md](modules.md#images-and-videos) for how media is stored, served and deleted.

## Cloud mode

| Variable | Default | Purpose |
|---|---|---|
| `CUEFLOW_QUEUE` | `local` | `cloudtasks` delivers background work through Cloud Tasks (cloud mode, see [`deploy/cloudrun`](../deploy/cloudrun/README.md)) |
| `CUEFLOW_EVENTS` | `postgres` | How dashboard live updates reach every instance: `postgres` (`LISTEN/NOTIFY`, one session connection per instance) or `pubsub` (Google Pub/Sub, no database connection; see [`deploy/cloudrun`](../deploy/cloudrun/README.md#live-updates-through-pubsub-optional)). `PUBSUB_EMULATOR_HOST` points it at the emulator |
| `CUEFLOW_PUBSUB_TOPIC` | empty | The topic for `CUEFLOW_EVENTS=pubsub`, as `projects/PROJECT/topics/TOPIC` |
| `CUEFLOW_PUBSUB_DELETE_TIMEOUT` | `8s` | How long shutdown waits for deleting this instance's Pub/Sub subscription (a Go duration, > 0; a value that is not a duration (`600`, `5min`) stops startup with an error). Keep it under Cloud Run's 10 s grace after SIGTERM; 10 s or more logs a warning at startup. A delete that times out is only logged: the subscription expires on its own a day later |
| `CUEFLOW_TICK` | `internal` | `external` lets Cloud Scheduler drive the clock via `/internal/tick`; nothing is polled in-process. Requires `CUEFLOW_QUEUE=cloudtasks` |
| `CUEFLOW_TASKS_QUEUE` / `CUEFLOW_TASKS_SERVICE_ACCOUNT` / `CUEFLOW_TASKS_TARGET_URL` | | Cloud Tasks queue path, the service account that signs deliveries, and this service's own URL |
| `CUEFLOW_INTERNAL_SERVICE_ACCOUNTS` / `CUEFLOW_INTERNAL_AUDIENCE` | | Who may call `/internal/*` (comma-separated emails) and the ID-token audience (default: the target URL) |
| `CUEFLOW_CF_ACCESS_TEAM_DOMAIN` / `CUEFLOW_CF_ACCESS_AUD` | | Cloudflare Access (Zero Trust) login for the dashboard: your team domain and the application's AUD tag. Both or neither. Requests with a valid Access JWT are accepted like the admin token; see [`deploy/cloudrun`](../deploy/cloudrun/README.md) |

## Rotating the secret key

Connector credentials are encrypted with `CUEFLOW_SECRET_KEY`; losing that key means re-entering every
credential, so keep a copy in a password manager. To change it without downtime or re-entering anything:

1. Generate a new key (`cueflow genkey`). Set it as `CUEFLOW_SECRET_KEY`, move the old one to
   `CUEFLOW_SECRET_KEY_PREVIOUS`, and restart. New data uses the new key; old data stays readable.
2. Re-encrypt everything: `docker compose exec cueflow cueflow rotate-key` (try `--dry-run` first). It changes
   nothing if any connector can't be decrypted.
3. Remove `CUEFLOW_SECRET_KEY_PREVIOUS` and restart.

Note: `docker compose down -v` deletes the volumes, i.e. the CueFlow database.
