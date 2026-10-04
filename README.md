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
| Publisher | `postiz_mcp` - Postiz over MCP (Streamable HTTP / SSE / stdio) |
| Source | `rss` (RSS/Atom, GUID-deduplicated) · `imap` (mailbox folder, read-only) |
| Search | `searxng` (self-hosted) · `tavily` |
| LLM | `openai` (OpenAI, OpenRouter, Azure OpenAI, DeepSeek, Ollama, vLLM, LocalAI, any OpenAI-compatible) · `anthropic` (Claude API) · `anthropic_bedrock` (Claude on Amazon Bedrock) · `anthropic_vertex` (Claude on Google Vertex AI) |
| Notifier | `smtp` (review reminders and digests) |

### LLM authentication

| Connector | Ways to authenticate |
|---|---|
| Anthropic Claude | API key · OAuth access token (adds the required OAuth beta header) · Bearer token for gateways · named `ant auth login` profile · or blank to use the server environment (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_PROFILE`, the default profile) |
| Claude on Amazon Bedrock | Bedrock API key · AWS access key + secret (+ session token) · AWS profile · or blank to use the default AWS credential chain (env vars, `~/.aws`, IAM role) |
| Claude on Google Vertex AI | Service account key JSON · or blank to use Application Default Credentials |
| OpenAI-compatible | API key (Bearer; `api-key` header for Azure) · extra headers for gateways · none for local servers. Presets: OpenAI, **OpenRouter** (base URL and attribution headers preset; use an `sk-or-...` key and models like `anthropic/claude-opus-4.8`), **Azure OpenAI** (v1 endpoint, or the legacy deployments endpoint with an API version), DeepSeek, Ollama, vLLM, LocalAI |

Credentials entered in the UI are encrypted per connector and take precedence over server-wide environment
variables. To use environment variables or credential files with Docker, set them in `deploy/.env` and see
the commented `volumes:` example in `deploy/docker-compose.yml` (for `~/.config/anthropic`, `~/.aws`, or a GCP
service account key).

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
   - an LLM - e.g. *Anthropic Claude* (model `claude-opus-5-5`) or *OpenAI-compatible* pointing at Ollama.
   - one or more sources - e.g. an RSS feed.
   - optional: SearXNG/Tavily for web search, SMTP for review reminders.
3. Either:
   - **Command**: tell the agent team what you want, or
   - **Pipelines**: create a fixed content pipeline (cron such as `0 8 * * *`, timezone, topic, platforms,
     instructions - see [docs/prompt-templates.md](docs/prompt-templates.md)) and press **Run now**.
4. **Review**: edit drafts, approve or reject. Approved posts are handed to Postiz within seconds; failures
   show the reason and a **Retry** button.

### Try it without Postiz

```bash
cd deploy
docker compose --profile mock up -d cueflow cueflow-db mock-postiz
```

Create a *Postiz (MCP)* connector with endpoint `http://mock-postiz:4007/api/mcp` and API key `dev-key`.
Received posts are listed at <http://localhost:4008/posts>.

### Remote access

Ports are bound to localhost on purpose. To review from your phone, use an SSH tunnel
(`ssh -L 3000:localhost:3000 nas`) or a VPN such as Tailscale, and set `CUEFLOW_ADMIN_TOKEN`.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | `postgres://cueflow:cueflow@localhost:5432/cueflow?sslmode=disable` | PostgreSQL |
| `CUEFLOW_SECRET_KEY` | *(required)* | 32-byte key (base64/hex) for connector secrets |
| `CUEFLOW_ADDR` | `127.0.0.1:3000` | Listen address |
| `CUEFLOW_ADMIN_TOKEN` | | Require `Authorization: Bearer <token>` for the API/UI |
| `CUEFLOW_PUBLIC_URL` | `http://localhost:3000` | Links in emails |
| `CUEFLOW_DIGEST_CRON` | | e.g. `0 9 * * *` - daily "awaiting review" digest |
| `CUEFLOW_DISPATCH_INTERVAL` | `3s` | Publish worker poll interval |
| `CUEFLOW_DISPATCH_LEASE` | `5m` | Max time a publish may take before it is considered crashed |
| `CUEFLOW_PUBLISH_TIMEOUT` | `2m` | Timeout of one publish call |
| `CUEFLOW_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CUEFLOW_DISABLE_SCHEDULER` | `false` | Run API/worker without cron |
| `CUEFLOW_AGENT_CONCURRENCY` | `3` | Agent tasks that may run at the same time |
| `CUEFLOW_AGENT_ALLOW_PRIVATE_URLS` | `false` | Let agents' `read_url` reach LAN/loopback addresses |

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
internal/connectors/*  built-in plugins (postiz, rss, imap, searxng, tavily, openai, anthropic, smtp)
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
