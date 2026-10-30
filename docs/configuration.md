# Configuration

CueFlow is configured with environment variables. The Docker defaults are in `deploy/.env.example`;
for Cloud Run see [deploy/cloudrun/README.md](../deploy/cloudrun/README.md).
A value that stops startup with an error also stops `cueflow migrate`, `rotate-key` and `media`;
startup warnings are logged by `cueflow serve` only.

## Secrets from files and Secret Manager

The variables that hold a credential, `DATABASE_URL`, `DATABASE_SESSION_URL`, `CUEFLOW_SECRET_KEY`,
`CUEFLOW_SECRET_KEY_PREVIOUS`, `CUEFLOW_ADMIN_TOKEN` and `CUEFLOW_MCP_TOKEN`, can also be given in two
other ways. Each `<NAME>` is read in this order:

1. **`<NAME>` set and not empty.** A value that starts with `gcpsm://` is a Google Secret Manager
   reference: `gcpsm://projects/<project>/secrets/<secret>` (the latest version),
   `gcpsm://projects/<project>/secrets/<secret>/versions/<version>`, or the same with
   `/locations/<location>` after the project for a regional secret (read from that region's endpoint).
   CueFlow reads every reference once at startup, all at the same time, with Application Default
   Credentials (on GCE and Cloud Run, the service account from the metadata server); together they
   must answer within 10 s. The service account needs `roles/secretmanager.secretAccessor` on each
   secret. Any other value is used as is.
2. **`<NAME>_FILE` set** (as in the `postgres` and `cloudflared` images, e.g. a Docker or Kubernetes
   secret at `/run/secrets/<NAME>`): the file's content.
3. Neither: unset.

A Secret Manager payload and a file both lose one trailing newline (`\n` or `\r\n`), since
`echo ... | gcloud secrets create --data-file=-` and `echo ... > file` add one; anything else is kept.
Setting both `<NAME>` and `<NAME>_FILE` (both not empty) stops startup with an error. For
`CUEFLOW_SECRET_KEY_PREVIOUS` the whole value is read first, then split at commas and newlines, so a
file can hold one key per line. It takes one value: with `gcpsm://`, that is one secret holding the old
keys, separated by newlines or commas; a comma-separated list of several `gcpsm://` references is not
supported. A reference that can't be read (a malformed reference, a missing
secret or version, permission denied, an unreachable API, no answer within 10 s, a missing or
unreadable file, or a secret or file that is empty once its trailing newline is gone) stops startup with an error naming the variable and the reference, never the value;
it never falls back to an empty value. The other settings are checked first, so a typo elsewhere is
reported without waiting for Secret Manager. Agents and stdio MCP servers that CueFlow starts don't inherit these variables, nor any other `CUEFLOW_*` or `DATABASE_*` one; that filters their environment and does not isolate them (see [external-agents.md](external-agents.md#external-agents-acp)). There is no refresh: to rotate a secret, add a new
version and restart. With `deploy/docker-compose.yml`, the container's `DATABASE_URL` comes from
`CUEFLOW_DB_URL` (default: the bundled database), never from a `DATABASE_URL` in `.env`; to use
`DATABASE_URL_FILE` there, set `CUEFLOW_DB_URL=` (empty).

## Core

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | `postgres://cueflow:cueflow@localhost:5432/cueflow?sslmode=disable` | PostgreSQL |
| `DATABASE_SESSION_URL` | empty | A session-mode connection for `LISTEN` and the migration lock. Set it when `DATABASE_URL` is a transaction-mode pooler (Supabase port 6543, PgBouncer `pool_mode=transaction`); the main pool then runs without named prepared statements. Empty: everything uses `DATABASE_URL` |
| `CUEFLOW_SECRET_KEY` | *(required)* | 32-byte key (base64/hex) for connector secrets. Back it up |
| `CUEFLOW_SECRET_KEY_PREVIOUS` | | Old key(s), separated by commas or newlines, still accepted for reading during a rotation |
| `CUEFLOW_ADDR` | `127.0.0.1:3000` | Listen address |
| `CUEFLOW_ADMIN_TOKEN` | | Require `Authorization: Bearer <token>` for the API/UI |
| `CUEFLOW_MCP_TOKEN` | | Enables the operator MCP endpoint `/mcp` for external agents (Bearer token) |
| `CUEFLOW_MCP_REQUEST_TIMEOUT` | `10m` | The deadline of one request to the MCP endpoints (`/mcp`, `/mcp/run/…`), that is one tool call (a Go duration, at least `30s`; a value that is not a duration (`600`, `5min`) or is out of range (`99999999999h`) stops startup with an error). For a run's or agent token's tool call (`/mcp/run/…`, `/mcp` with an agent token) it bounds two things. First, a tool connector's call (an `mcp_tools` server's tool, say): it ends 5 s before the deadline if that comes before its own timeout, and the agent is told which limit ran out. Second, for a run's call (`/mcp/run/…`) only, the wait for the run's previous tool call to finish. Built-in tools (`claim_task`, `create_subtask`, …) are not cut by it: they run to the end and never stop halfway through their state changes (a thread message that can't be written within 30 s is dropped and logged; a tool call makes a few such writes, each with its own 30 s). One that hangs on something other than a connector (a stalled database, say) holds its request until it ends or Cloud Run's 30 min request timeout cuts it; an answer that comes before that reaches the agent, and a tool call that would only start after the deadline is not run. A client that disconnects does not cancel a tool call. The deadline is per request and does not carry across hops. Say agent A's connector call reaches agent B, which calls back into CueFlow: each of B's tool calls is its own request with the full time, and only A's connector call is capped by what is left of A's request, ending with that error rather than a dropped request. A task or run gets no overall deadline from this setting: a run that makes many tool calls is bounded per call only. Keep it under Cloud Run's 30 min request timeout; 30 min or more logs a warning at startup. In-process runs keep the connector's own timeout |
| `CUEFLOW_CONNECTOR_HTTP_TIMEOUT` | `90s` | Timeout of one HTTP request made by a connector with the shared client (search, ComfyUI, RSS, Telegram, an MCP action server, the OAuth token fetch of `gemini_vertex`, `anthropic_vertex` and `vertex_image`, …). A Go duration, > 0; `0`, a negative value, a value that is not a duration (`90`, `90sec`) or is out of range (`99999999999h`) stops startup with an error. Model calls and the MCP publisher do not use it: they run until their own deadline (a publish until `CUEFLOW_PUBLISH_TIMEOUT`) |
| `CUEFLOW_PUBLIC_URL` | `http://localhost:3000` | Links in emails |
| `CUEFLOW_DIRECT_URL` | `CUEFLOW_PUBLIC_URL` | How machines reach the server (runner registration and MCP commands, media URLs): they cannot pass a browser login such as Cloudflare Access |
| `CUEFLOW_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CUEFLOW_DB_MAX_CONNS` | driver default | Cap on this process's main pool (use ~5 per instance on a small database; behind a session pooler every one counts against its client limit) |

## Publishing and scheduling

| Variable | Default | Purpose |
|---|---|---|
| `CUEFLOW_DISPATCH_INTERVAL` | `3s` | Deliverables worker poll interval. A Go duration of at least `100ms`; anything else keeps the default and logs a warning at startup |
| `CUEFLOW_DISPATCH_LEASE` | `10m` | Max time acting on one approved deliverable (publishing a post, running an action connector) may take before the worker is considered crashed. A Go duration of at least `1s`; anything else keeps the default and logs a warning at startup. With the default `5m` publish timeout, any lease from `1s` up to `5m` logs the publish-timeout warning below: raise the lease above the timeout, or set `CUEFLOW_PUBLISH_TIMEOUT` below the lease |
| `CUEFLOW_PUBLISH_TIMEOUT` | `5m` | Timeout of one publish call (Instagram can take minutes); keep it below the lease, or startup logs a warning (both values are still used). A Go duration of at least `1s`; anything else keeps the default and logs a warning at startup. A too-low timeout falls back to `5m` even when the lease is shorter, so it then logs that warning too |
| `CUEFLOW_DISABLE_SCHEDULER` | `false` | Run API/worker without cron |

## Agents

| Variable | Default | Purpose |
|---|---|---|
| `CUEFLOW_AGENT_CONCURRENCY` | `3` | Default for how many of an org's agent tasks may run at the same time, counted across every instance; an org can set its own (1–64) under Manage orgs. Not a per-process cap. Tasks of external (self-driven) agents don't count |
| `CUEFLOW_CONTEXT_HANDOFF` | `0.8` | How full a run's context (latest prompt over the model's window) may get before the run hands off to a fresh one; the task page colours the size from here. A number between 0 and 1; anything else keeps 0.8 and logs a warning at startup |
| `CUEFLOW_MODULES` | `content,deliverables` | Feature modules to mount, comma-separated (`all`, `none`, or any of `content`, `deliverables`; `content` needs `deliverables`). A module that is not mounted has no routes, agent tools, workers or cron; its tables stay, and the dashboard hides its pages (`/meta` lists what is mounted) |
| `CUEFLOW_AGENT_ALLOW_PRIVATE_URLS` | `false` | Let agents' `read_url` reach LAN/loopback addresses |

## Media

| Variable | Default | Purpose |
|---|---|---|
| `CUEFLOW_MEDIA_BASE_URL` | `CUEFLOW_PUBLIC_URL` | Prefix for images stored in CueFlow when handing them to publishers; must be reachable from the publisher (compose sets `http://cueflow:3000`) |
| `CUEFLOW_MEDIA_STORAGE` | | Where new images/videos and task files are kept: empty = the database, `file:///data/media`, or `gcs://bucket/prefix`. Move existing media with `cueflow media migrate --to file:///data/media` |
| `CUEFLOW_MEDIA_RETENTION_DAYS` | | With `CUEFLOW_TICK=external`: delete media older than this many days that no unfinished deliverable uses. Off when empty or 0. Manual: `cueflow media prune --older-than 90d [--dry-run]` |
| `CUEFLOW_MEDIA_REGISTER_ACCOUNTS` | | Comma-separated Google service accounts allowed to call `POST /internal/media/register` (adopt a file written to the bucket's `uploads/`), in any mode. Needs `CUEFLOW_MEDIA_STORAGE=gcs://...`: startup fails without it. The caller's ID token must be made out to one of `CUEFLOW_INTERNAL_AUDIENCE` |

See [modules.md](modules.md#images-and-videos) for how media is stored, served and deleted.

## Cloud mode

| Variable | Default | Purpose |
|---|---|---|
| `CUEFLOW_QUEUE` | `local` | `cloudtasks` delivers background work through Cloud Tasks (cloud mode, see [`deploy/cloudrun`](../deploy/cloudrun/README.md)) |
| `CUEFLOW_EVENTS` | `postgres` | How dashboard live updates reach every instance: `postgres` (`LISTEN/NOTIFY`, one session connection per instance) or `pubsub` (Google Pub/Sub, no database connection; see [`deploy/cloudrun`](../deploy/cloudrun/README.md#live-updates-through-pubsub-optional)). `PUBSUB_EMULATOR_HOST` points it at the emulator |
| `CUEFLOW_PUBSUB_TOPIC` | empty | The topic for `CUEFLOW_EVENTS=pubsub`, as `projects/PROJECT/topics/TOPIC` |
| `CUEFLOW_PUBSUB_DELETE_TIMEOUT` | `8s` | How long shutdown waits for deleting this instance's Pub/Sub subscription (a Go duration, > 0; a value that is not a duration (`8`, `5sec`) or is out of range (`99999999999h`) stops startup with an error). Keep it under Cloud Run's 10 s grace after SIGTERM; 10 s or more logs a warning at startup. A delete that times out is only logged: the subscription expires on its own a day later |
| `CUEFLOW_TICK` | `internal` | `external` lets Cloud Scheduler drive the clock via `/internal/tick`; nothing is polled in-process. Requires `CUEFLOW_QUEUE=cloudtasks` |
| `CUEFLOW_TASKS_QUEUE` / `CUEFLOW_TASKS_SERVICE_ACCOUNT` / `CUEFLOW_TASKS_TARGET_URL` | | Cloud Tasks queue path, the service account that signs deliveries, and this service's own URL |
| `CUEFLOW_INTERNAL_SERVICE_ACCOUNTS` / `CUEFLOW_INTERNAL_AUDIENCE` | | Who may call `/internal/tick` and `/internal/work/*` (comma-separated emails), and the ID-token audiences every `/internal/*` route accepts: comma-separated, default `CUEFLOW_TASKS_TARGET_URL` and `CUEFLOW_DIRECT_URL` (both `CUEFLOW_PUBLIC_URL` by default; listed once if equal). A caller's token is usually made out to the URL it calls, so set it to list each URL CueFlow is reached under when there are others (e.g. `https://cueflow.example.com,http://10.0.0.2:3000`). Cloud Tasks deliveries are signed for the first. `CUEFLOW_INTERNAL_AUDIENCE` also applies to media register in single mode |
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
