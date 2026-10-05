# CueFlow

**Open-source, self-hosted Agent Ops Console for social media.** CueFlow wakes up on a schedule,
collects material (RSS, mailboxes, web search), has an LLM write platform-specific drafts, puts every
draft in front of a human reviewer, and only then publishes it through [Postiz](https://github.com/gitroomhq/postiz-app)
over the Model Context Protocol (MCP). No public IP, domain or webhook required: it runs on a NAS,
a home lab or a laptop with Docker.

Traditional Chinese version: [README.zh-TW.md](README.zh-TW.md)

```
 Cron ─▶ Ingest (RSS · IMAP · Search) ─▶ Agent (LLM tool loop) ─▶ PENDING_REVIEW
                                                                     │  human approves
                                                                     ▼
                         Postiz ◀── MCP ── Dispatch worker ◀── APPROVED → QUEUED ─▶ PUBLISHED / FAILED
```

## Agent team (Command · Board · Agents)

CueFlow ships with a small team of agents you can talk to in plain language - like a chat bot that
actually does the work, but on a board you can watch:

- **Command**: tell the team what you want, e.g. "Every morning at 8, summarize AI news and draft a
  Threads post". The manager agent (🧭 Chief) works out the plan, delegates subtasks to teammates, sets up recurring
  schedules, and reports back.
- **Board**: every request is a task card - Backlog → To do → In progress → Waiting → Review → Done.
  Drag a card to *To do* with an agent assigned and it starts working. Each task has a thread: the
  agent's progress, tool calls, questions and result. Reply to continue, or start with `@writer` to hand
  it to another agent.
- **Agents**: roles with their own persona, capabilities, LLM, sources and memory. Defaults:
  🧭 Chief (plans and delegates), 🔎 Researcher (research with sources), ✍️ Writer (platform drafts),
  🧐 Editor (reviews and revises drafts). Agents write in their configured language (default English).
  Add your own.
- **Schedules**: recurring tasks for an agent (agents can create them too, at most every 5 minutes).
- **External agents (ACP)**: an agent can run its tasks in Claude Code, Codex, Gemini CLI, OpenCode or Goose
  instead of the built-in LLM loop, over the [Agent Client Protocol](https://agentclientprotocol.com). Add an
  *External agent (ACP)* runtime connector and pick it as the agent's *Runtime*. The external agent gets
  CueFlow's tools through a per-run MCP server; shell, file edits and local file access are refused, and its
  drafts still go to Review. With Claude Code you can use a Claude subscription: run `claude setup-token` and
  paste the token into the connector (the default Docker image ships the Claude Code and Codex adapters).

| Capability | Tools |
|---|---|
| research | `web_search`, `read_url` (public addresses only), `search_materials`, `fetch_sources` |
| drafting | `create_post_draft`, `list_posts`, `revise_draft` |
| delegation | `create_subtask`, `list_agents`, `wait_for_subtasks` |
| planning | `list_tasks`, `get_task`, `create_task`, `schedule_task`, `list_schedules`, `pause_schedule` |
| memory | `remember`, `search_memory` |
| (always) | `add_comment`, `ask_human`, `complete_task` |

Agents never publish. Drafts they write land in **Review** like any other post, and only a human approval
sends them to Postiz.

## Highlights

- **Human-in-the-loop by construction.** Posts follow a strict state machine
  `DRAFT → PENDING_REVIEW → APPROVED | REJECTED → QUEUED → PUBLISHED | FAILED` (plus `FAILED → QUEUED` for manual retry).
  Every change is recorded in an audit log, including before/after diffs of reviewer edits.
- **Agents cannot publish.** No agent tool can publish, and publishers are wrapped so `Publish` fails
  unless the dispatch worker attached a grant for that exact, human-approved post (FR-4.2).
- **Everything is a connector plugin.** Publishers, sources, search engines, LLMs and notifiers implement
  small Go interfaces and self-register with a config schema; the UI renders their forms automatically.
  See [docs/connectors.md](docs/connectors.md).
- **Zero public exposure.** Outbound polling only; the server binds to `127.0.0.1:3000`.
- **Secrets encrypted at rest** with AES-256-GCM (bound to the connector row).
- **Crash-safe publishing.** Dispatch uses leases; a worker that dies mid-publish leaves the post `FAILED`
  with a "check before retrying" note instead of silently double-posting.
- **Single binary.** Go API + scheduler + worker, with the Next.js dashboard embedded.

## Built-in connectors

| Kind | Types |
|---|---|
| Publisher | `postiz_mcp` - Postiz over MCP (Streamable HTTP / SSE / stdio), channels optionally picked per post · `cuesocial` - CueSocial over MCP, posting to the Threads/Instagram accounts picked per post |
| Source | `rss` (RSS/Atom, GUID-deduplicated) · `imap` (mailbox folder, read-only) |
| Search | `searxng` (self-hosted) · `tavily` |
| LLM | `openai` (OpenAI, OpenRouter, Azure OpenAI, DeepSeek, Ollama, vLLM, LocalAI, any OpenAI-compatible) · `anthropic` (Claude API) · `anthropic_bedrock` (Claude on Amazon Bedrock) · `anthropic_vertex` (Claude on Google Vertex AI) · `gemini_vertex` (Gemini on Google Vertex AI) |
| Notifier | `smtp` (review reminders and digests) |

### LLM authentication

| Connector | Ways to authenticate |
|---|---|
| Anthropic Claude | API key · OAuth access token (adds the required OAuth beta header) · Bearer token for gateways · named `ant auth login` profile · or blank to use the server environment (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_PROFILE`, the default profile) |
| Claude on Amazon Bedrock | Bedrock API key · AWS access key + secret (+ session token) · AWS profile · or blank to use the default AWS credential chain (env vars, `~/.aws`, IAM role) |
| Claude on Google Vertex AI | Service account key JSON · or blank to use Application Default Credentials |
| Gemini on Google Vertex AI | Same as above: a service account key, or Application Default Credentials such as your local `gcloud auth application-default login`. Needs a GCP project with billing enabled; usage is billed by GCP, not by Gemini/Google AI subscriptions |
| OpenAI-compatible | API key (Bearer; `api-key` header for Azure) · extra headers for gateways · none for local servers. Presets: OpenAI, **OpenRouter** (base URL and attribution headers preset; use an `sk-or-...` key and models like `anthropic/claude-opus-4.8`), **Azure OpenAI** (v1 endpoint, or the legacy deployments endpoint with an API version), DeepSeek, Ollama, vLLM, LocalAI |

Credentials entered in the UI are encrypted per connector and take precedence over server-wide environment
variables. To use environment variables or credential files with Docker, set them in `deploy/.env` and see
the commented `volumes:` example in `deploy/docker-compose.yml` (for `~/.config/anthropic`, `~/.aws`, `~/.config/gcloud`, or a
GCP service account key).

## Quick start (Docker)

```bash
git clone https://github.com/j75689/cueflow && cd cueflow/deploy
cp .env.example .env
# fill in CUEFLOW_SECRET_KEY (openssl rand -base64 32) and POSTIZ_JWT_SECRET (openssl rand -hex 32)
docker compose up -d
```

- CueFlow: <http://localhost:3000>
- Postiz: <http://localhost:4007>

Then:

1. **Postiz**: create an account, connect your Threads / Instagram / X channels, and copy the API key from
   *Settings → Developers*.
2. **CueFlow → Connectors**:
   - *Postiz (MCP)* - endpoint `http://postiz:5000/api/mcp`, paste the API key, press **Test**.
     Or *CueSocial* - endpoint `https://<your CueSocial>/mcp` plus a Cloudflare Access service token; you then
     pick the Threads/Instagram accounts for each post in the review editor.
   - an LLM - e.g. *Anthropic Claude* (model `claude-opus-5-5`) or *OpenAI-compatible* pointing at Ollama.
   - one or more sources - e.g. an RSS feed.
   - optional: SearXNG/Tavily for web search, SMTP for review reminders.
3. Either:
   - **Command**: tell the agent team what you want, or
   - **Pipelines**: create a fixed content pipeline (cron such as `0 8 * * *`, timezone, topic, platforms,
     instructions - see [docs/prompt-templates.md](docs/prompt-templates.md)) and press **Run now**.
4. **Review**: edit drafts, approve or reject. Approved posts are handed to Postiz within seconds; failures
   show the reason and a **Retry** button.

### Images

| `CUEFLOW_IMAGE_TARGET` | Contents |
|---|---|
| `slim` (default) | CueFlow only (distroless). Built-in agents and LLM connectors |
| `agents` | + Node.js and the ACP adapters for Claude Code and Codex, for external agents |

Set it in `deploy/.env`, then `docker compose up -d --build`.

### Web search

The compose file includes a SearXNG instance (set `SEARXNG_SECRET` in `deploy/.env`). In CueFlow add a
*SearXNG* connector with URL `http://searxng:8080`; agents without a search connector of their own use
the first active one. Its own page is at <http://localhost:8888>.

### Try it without Postiz

```bash
cd deploy
docker compose --profile mock up -d cueflow cueflow-db mock-postiz
```

Create a *Postiz (MCP)* connector with endpoint `http://mock-postiz:4007/api/mcp` and API key `dev-key`.
Received posts are listed at <http://localhost:4008/posts>.

### Control CueFlow from other agents (MCP)

CueFlow serves an MCP endpoint at `/mcp` so an external agent (Claude Code, Codex, Cursor...) can run
the board for you: list agents and tasks, create and assign tasks, comment (answering an agent that is
waiting on you), move, start or stop tasks, and read the review queue. It cannot approve or publish posts;
that stays in the dashboard.

Set `CUEFLOW_MCP_TOKEN` in `deploy/.env` (`openssl rand -hex 32`), restart, then for example:

```bash
claude mcp add --transport http cueflow http://localhost:3000/mcp \
  --header "Authorization: Bearer $CUEFLOW_MCP_TOKEN"
```

### Remote access

Ports are bound to localhost on purpose. To review from your phone, use an SSH tunnel
(`ssh -L 3000:localhost:3000 nas`) or a VPN such as Tailscale, and set `CUEFLOW_ADMIN_TOKEN`.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | `postgres://cueflow:cueflow@localhost:5432/cueflow?sslmode=disable` | PostgreSQL |
| `CUEFLOW_SECRET_KEY` | *(required)* | 32-byte key (base64/hex) for connector secrets. Back it up |
| `CUEFLOW_SECRET_KEY_PREVIOUS` | | Old key(s), comma-separated, still accepted for reading during a rotation |
| `CUEFLOW_ADDR` | `127.0.0.1:3000` | Listen address |
| `CUEFLOW_ADMIN_TOKEN` | | Require `Authorization: Bearer <token>` for the API/UI |
| `CUEFLOW_MCP_TOKEN` | | Enables the operator MCP endpoint `/mcp` for external agents (Bearer token) |
| `CUEFLOW_PUBLIC_URL` | `http://localhost:3000` | Links in emails |
| `CUEFLOW_DIGEST_CRON` | | e.g. `0 9 * * *` - daily "awaiting review" digest |
| `CUEFLOW_DISPATCH_INTERVAL` | `3s` | Publish worker poll interval |
| `CUEFLOW_DISPATCH_LEASE` | `10m` | Max time a publish may take before it is considered crashed |
| `CUEFLOW_PUBLISH_TIMEOUT` | `5m` | Timeout of one publish call (Instagram can take minutes); keep it below the lease |
| `CUEFLOW_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CUEFLOW_DISABLE_SCHEDULER` | `false` | Run API/worker without cron |
| `CUEFLOW_AGENT_CONCURRENCY` | `3` | Agent tasks that may run at the same time |
| `CUEFLOW_AGENT_ALLOW_PRIVATE_URLS` | `false` | Let agents' `read_url` reach LAN/loopback addresses |

### Rotating the secret key

Connector credentials are encrypted with `CUEFLOW_SECRET_KEY`; losing that key means re-entering every
credential, so keep a copy in a password manager. To change it without downtime or re-entering anything:

1. Generate a new key (`cueflow genkey`). Set it as `CUEFLOW_SECRET_KEY`, move the old one to
   `CUEFLOW_SECRET_KEY_PREVIOUS`, and restart. New data uses the new key; old data stays readable.
2. Re-encrypt everything: `docker compose exec cueflow cueflow rotate-key` (try `--dry-run` first). It changes
   nothing if any connector can't be decrypted.
3. Remove `CUEFLOW_SECRET_KEY_PREVIOUS` and restart.

Note: `docker compose down -v` deletes the volumes, i.e. the CueFlow and Postiz databases.

## Development

Requirements: Go 1.27+, Node 22+, Docker (for PostgreSQL).

```bash
make test-db                      # disposable PostgreSQL on :55432
export DATABASE_URL="postgres://cueflow:cueflow@localhost:55432/cueflow?sslmode=disable"
export CUEFLOW_SECRET_KEY=$(go run ./cmd/cueflow genkey)

go run ./cmd/cueflow              # API on :3000
go run ./cmd/mock-postiz-mcp      # mock Postiz on :4007 (key dev-key)
cd web && npm install && npm run dev   # dashboard on :3001, proxies /api to :3000

make test                         # unit tests
make test-e2e                     # full chain against PostgreSQL
make build                        # bin/cueflow with the dashboard embedded
```

### Layout

```
cmd/cueflow            server binary (serve | migrate | genkey | healthcheck)
cmd/mock-postiz-mcp    Postiz MCP stand-in for development
internal/domain        post state machine, platform rules
internal/connector     plugin registry, interfaces, config specs
internal/connectors/*  built-in plugins (postiz, cuesocial, rss, imap, searxng, tavily, openai, anthropic, smtp)
internal/agent         pipeline draft generator (LLM tool loop)
internal/crew          agent team runner: task claiming, tools, delegation, default roles
internal/pipeline      ingest -> generate -> store -> notify
internal/dispatch      publish worker (the only caller of publishers)
internal/scheduler     cron heartbeat
internal/store         PostgreSQL repositories; migrations in internal/db/migrations
internal/api           REST + SSE (docs/api.md), serves the embedded UI
web/                   Next.js + Tailwind dashboard (static export)
deploy/                docker-compose.yml and .env.example
```

## Documentation

- [Architecture](docs/architecture.md)
- [Agentic flow](docs/agentic-flow.md)
- [REST API](docs/api.md)
- [Writing a connector plugin](docs/connectors.md)
- [Prompt templates](docs/prompt-templates.md)
- Traditional Chinese docs: [README.zh-TW.md](README.zh-TW.md) · [docs/zh-TW/](docs/zh-TW/)

## License

Apache-2.0
