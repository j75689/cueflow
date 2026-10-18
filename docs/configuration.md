# Configuration

CueFlow is configured with environment variables. The Docker defaults are in `deploy/.env.example`;
for Cloud Run see [deploy/cloudrun/README.md](../deploy/cloudrun/README.md).

## Core

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | `postgres://cueflow:cueflow@localhost:5432/cueflow?sslmode=disable` | PostgreSQL |
| `CUEFLOW_SECRET_KEY` | *(required)* | 32-byte key (base64/hex) for connector secrets. Back it up |
| `CUEFLOW_SECRET_KEY_PREVIOUS` | | Old key(s), comma-separated, still accepted for reading during a rotation |
| `CUEFLOW_ADDR` | `127.0.0.1:3000` | Listen address |
| `CUEFLOW_ADMIN_TOKEN` | | Require `Authorization: Bearer <token>` for the API/UI |
| `CUEFLOW_MCP_TOKEN` | | Enables the operator MCP endpoint `/mcp` for external agents (Bearer token) |
| `CUEFLOW_PUBLIC_URL` | `http://localhost:3000` | Links in emails |
| `CUEFLOW_DIRECT_URL` | `CUEFLOW_PUBLIC_URL` | How machines reach the server (runner registration and MCP commands, media URLs): they cannot pass a browser login such as Cloudflare Access |
| `CUEFLOW_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CUEFLOW_DB_MAX_CONNS` | driver default | Cap on this process's database connections (use ~5 per instance on a small database) |

## Publishing and scheduling

| Variable | Default | Purpose |
|---|---|---|
| `CUEFLOW_DISPATCH_INTERVAL` | `3s` | Deliverables worker poll interval |
| `CUEFLOW_DISPATCH_LEASE` | `10m` | Max time acting on one approved deliverable (publishing a post, running an action connector) may take before the worker is considered crashed |
| `CUEFLOW_PUBLISH_TIMEOUT` | `5m` | Timeout of one publish call (Instagram can take minutes); keep it below the lease |
| `CUEFLOW_DISABLE_SCHEDULER` | `false` | Run API/worker without cron |

## Agents

| Variable | Default | Purpose |
|---|---|---|
| `CUEFLOW_AGENT_CONCURRENCY` | `3` | Agent tasks that may run at the same time |
| `CUEFLOW_MODULES` | `content,deliverables` | Feature modules to mount, comma-separated (`all`, `none`, or any of `content`, `deliverables`). A module that is not mounted has no routes, agent tools, workers or cron; its tables stay, and the dashboard hides its pages (`/meta` lists what is mounted) |
| `CUEFLOW_AGENT_ALLOW_PRIVATE_URLS` | `false` | Let agents' `read_url` reach LAN/loopback addresses |

## Media

| Variable | Default | Purpose |
|---|---|---|
| `CUEFLOW_MEDIA_BASE_URL` | `CUEFLOW_PUBLIC_URL` | Prefix for images stored in CueFlow when handing them to publishers; must be reachable from the publisher (compose sets `http://cueflow:3000`) |
| `CUEFLOW_MEDIA_STORAGE` | | Where new images/videos are kept: empty = the database, `file:///data/media`, or `gcs://bucket/prefix`. Move existing media with `cueflow media migrate --to file:///data/media` |
| `CUEFLOW_MEDIA_RETENTION_DAYS` | | With `CUEFLOW_TICK=external`: delete media older than this many days that no unfinished deliverable uses. Off when empty or 0. Manual: `cueflow media prune --older-than 90d [--dry-run]` |
| `CUEFLOW_MEDIA_REGISTER_ACCOUNTS` | | Comma-separated Google service accounts allowed to call `POST /internal/media/register` (adopt a file written to the bucket's `uploads/`). Needs `CUEFLOW_TICK=external` and GCS media |

See [modules.md](modules.md#images-and-videos) for how media is stored, served and deleted.

## Cloud mode

| Variable | Default | Purpose |
|---|---|---|
| `CUEFLOW_QUEUE` | `local` | `cloudtasks` delivers background work through Cloud Tasks (cloud mode, see [`deploy/cloudrun`](../deploy/cloudrun/README.md)) |
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
