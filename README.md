# CueFlow

**Open-source, self-hosted Agent Ops Console.** Give a team of AI agents tasks on a kanban board. They work
the tasks with their own tools, ask you when they need a decision, and anything risky waits for your approval.
An agent can run on a built-in LLM loop or hand the work to an external agent (Claude Code, Codex, Gemini CLI...)
on a machine you own; any [MCP](https://modelcontextprotocol.io) server can be its tools, each tool set to allow,
ask or deny; tasks can recur on a schedule. Run it at home (a NAS, a home lab or a laptop with Docker, no public
IP, domain or webhook needed) or on Google Cloud Run, where it scales to zero between jobs.

Traditional Chinese: [README.zh-TW.md](README.zh-TW.md) · All docs: [docs/](docs/)

```
 You · Schedule · MCP client ─▶ Task ─▶ Agent ─▶ tools (built-in · MCP servers · its own CLI on a runner)
                                         │
                       needs a decision / risky tool call ─▶ you approve in the thread ─▶ it continues
```

Everything an agent produces is a **deliverable** that waits for a human: a document, or, with the built-in content
module, a social post that agents draft from collected material (RSS, mailboxes, web search) and that, once approved,
is published through any MCP server following the four-tool [publisher contract](docs/publisher-mcp.md). Agents
never act on their own output: no agent tool can, and publishers and action connectors refuse unless the worker
attached a grant for that exact, human-approved item.

## How it works

- **Command**: tell the team what you want, e.g. "Every morning at 8, summarize AI news and draft a
  Threads post". The manager agent (🧭 Chief) plans, delegates subtasks, sets up schedules and reports back.
- **Board**: every request is a task card, Backlog → To do → In progress → Waiting → Review → Done. Each task
  has a thread with the agent's progress, tool calls, questions and result; reply to continue, or `@writer` to
  hand it to another agent.
- **Agents**: roles with their own persona, capabilities, LLM, sources and memory. A new install has only the
  Chief; add a writer, a researcher, an editor... and edit any of them.
- **Schedules**: recurring tasks for an agent (agents can create them too).
- **External agents and runners**: an agent can run its tasks in Claude Code, Codex, Gemini CLI, OpenCode or
  Goose over ACP, on the server or on a machine you own, and other agents can drive the board through
  CueFlow's own MCP endpoint. See [docs/external-agents.md](docs/external-agents.md).

Capabilities and tools, the task state machine, delegation and guardrails: [docs/agentic-flow.md](docs/agentic-flow.md).

## Highlights

- **Human-in-the-loop by construction.** Deliverables follow a strict state machine
  (`DRAFT → PENDING_REVIEW → APPROVED | REJECTED → QUEUED → DONE | FAILED`) and every change is in an
  audit log, including diffs of reviewer edits.
- **Everything is a connector plugin.** Publishers, sources, search engines, LLMs, notifiers and image
  generators implement small Go interfaces and self-register with a config schema; the UI renders their forms.
- **Private by default.** At home it only makes outbound connections and binds to `127.0.0.1:3000`; in the
  cloud the dashboard sits behind Cloudflare Access and machine-to-machine calls carry Google-signed identities.
  Connector secrets are encrypted at rest (AES-256-GCM).
- **Crash-safe actions.** The worker uses leases; one that dies mid-action leaves the item `FAILED`
  with a "check before retrying" note instead of silently acting twice.
- **Single binary, runs anywhere.** Go API + scheduler + worker with the Next.js dashboard embedded. Docker
  Compose on your own hardware, or Cloud Run with Cloud Tasks and Cloud Scheduler, so nothing runs between jobs.

## Quick start (Docker)

```bash
git clone https://github.com/j75689/cueflow && cd cueflow/deploy
cp .env.example .env
# fill in CUEFLOW_SECRET_KEY (openssl rand -base64 32) and SEARXNG_SECRET (openssl rand -hex 32)
docker compose up -d
```

CueFlow is at <http://localhost:3000>. Then:

1. **Connectors**: an LLM (e.g. *Anthropic Claude*, or *OpenAI-compatible* pointing at Ollama); a source such
   as an RSS feed; a publisher: *MCP publisher* pointing at a server that follows the
   [publisher contract](docs/publisher-mcp.md), or the bundled mock (`docker compose --profile mock up -d`,
   endpoint `http://mock-publisher:4010/mcp`, token `dev-token`) to try the flow without posting anywhere.
2. **Command**: tell the team what you want, or set up a **Schedule** for recurring work.
3. **Deliverables**: edit, pick the accounts, approve or reject. Approved posts reach the publisher within seconds.

Image variants, web search, the mock publisher, remote access: [docs/docker.md](docs/docker.md).

## Connectors

Publishers (any MCP server that follows the [publisher contract](docs/publisher-mcp.md)), sources
(RSS, IMAP), search (SearXNG, Tavily), LLMs (OpenAI-compatible, Claude on the API, Bedrock or Vertex AI, Gemini
on Vertex AI), SMTP notifications, image generation (OpenAI, Gemini, Vertex, ComfyUI) and any MCP server as
agent tools. Full list and authentication options: [docs/builtin-connectors.md](docs/builtin-connectors.md).
Writing one: [docs/connectors.md](docs/connectors.md).

## Going further

- **Configuration**: every environment variable, and how to rotate the secret key:
  [docs/configuration.md](docs/configuration.md). Back up `CUEFLOW_SECRET_KEY`; without it stored credentials
  are unreadable.
- **Runners, external agents, MCP**: [docs/external-agents.md](docs/external-agents.md).
- **Modules**: what `deliverables` and `content` add, switching them with `CUEFLOW_MODULES`, and where images and
  videos are stored: [docs/modules.md](docs/modules.md).
- **Google Cloud Run**: Supabase, Cloud Storage, Cloud Tasks and Cloud Scheduler, scaling to zero:
  [deploy/cloudrun/README.md](deploy/cloudrun/README.md).

## Documentation

- [Architecture](docs/architecture.md), with the package layout in §2.1
- [Agentic flow](docs/agentic-flow.md)
- [REST API](docs/api.md)
- [Configuration](docs/configuration.md)
- [Running with Docker Compose](docs/docker.md)
- [Built-in connectors](docs/builtin-connectors.md) · [Writing a connector plugin](docs/connectors.md) · [Publisher MCP contract](docs/publisher-mcp.md)
- [External agents, runners and MCP](docs/external-agents.md)
- [Modules: deliverables, content, images and videos](docs/modules.md)
- [Deploying on Google Cloud Run](deploy/cloudrun/README.md) · [A runner in a container](deploy/runner/README.md)
- Traditional Chinese: [README.zh-TW.md](README.zh-TW.md) · [docs/zh-TW/](docs/zh-TW/)

## Development and contributing

Go 1.27+, Node 22+ and Docker (for PostgreSQL); `make test-db`, `go run ./cmd/cueflow`, `cd web && npm run dev`.
The full setup, the rules that are not negotiable and the commit convention are in
[CONTRIBUTING.md](CONTRIBUTING.md). Security problems go through [SECURITY.md](SECURITY.md), never a public issue.

## License

Apache-2.0
