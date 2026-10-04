# CueFlow

**Open-source, self-hosted Agent Ops Console for social media.** CueFlow wakes up on a schedule,
collects material (RSS, mailboxes, web search), has an LLM write platform-specific drafts, puts every
draft in front of a human reviewer, and only then publishes it through [Postiz](https://github.com/gitroomhq/postiz-app)
over the Model Context Protocol (MCP).

> CueFlow 是給個人創作者、技術團隊與獨立運營者的開源自託管「社群 Agent 運營控制台」：
> 定時收集 RSS / 信箱 / 網路搜尋 → LLM 依平台風格產生草稿 → **人工審核** → 透過 MCP 交給 Postiz 發布到 Threads、Instagram、X、LinkedIn。
> 不需要 Public IP、網域或 Webhook，家裡的 NAS / Home Lab / 本機 Docker 就能跑。

```
 Cron ─▶ Ingest (RSS · IMAP · Search) ─▶ Agent (LLM tool loop) ─▶ PENDING_REVIEW
                                                                     │  human approves
                                                                     ▼
                         Postiz ◀── MCP ── Dispatch worker ◀── APPROVED → QUEUED ─▶ PUBLISHED / FAILED
```

## Highlights

- **Human-in-the-loop by construction.** Posts follow a strict state machine
  `DRAFT → PENDING_REVIEW → APPROVED | REJECTED → QUEUED → PUBLISHED | FAILED` (plus `FAILED → QUEUED` for manual retry).
  Every change is recorded in an audit log, including before/after diffs of reviewer edits.
- **The agent cannot publish.** It only sees `web_search` and `submit_drafts`. Publishers are wrapped so
  `Publish` fails unless the dispatch worker attached a grant for that exact, human-approved post (FR-4.2).
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
| LLM | `openai` (OpenAI, DeepSeek, Ollama, vLLM, LocalAI, any OpenAI-compatible) · `anthropic` (Claude) |
| Notifier | `smtp` (review reminders and digests) |

## Quick start (Docker)

```bash
git clone https://github.com/j75689/cueflow && cd cueflow
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
3. **CueFlow → Pipelines**: create a pipeline (cron such as `0 8 * * *`, timezone, topic, platforms,
   instructions - see [docs/prompt-templates.md](docs/prompt-templates.md)) and press **Run now**.
4. **Review**: edit drafts, approve or reject. Approved posts are handed to Postiz within seconds; failures
   show the reason and a **Retry** button.

### Try it without Postiz

```bash
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
internal/agent         LLM tool loop and prompts
internal/pipeline      ingest -> generate -> store -> notify
internal/dispatch      publish worker (the only caller of publishers)
internal/scheduler     cron heartbeat
internal/store         PostgreSQL repositories; migrations in internal/db/migrations
internal/api           REST + SSE (docs/api.md), serves the embedded UI
web/                   Next.js + Tailwind dashboard (static export)
```

## Documentation

- [REST API](docs/api.md)
- [Writing a connector plugin](docs/connectors.md)
- [Prompt templates](docs/prompt-templates.md)

## License

Apache-2.0
