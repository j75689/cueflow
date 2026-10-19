# Running with Docker Compose

`deploy/docker-compose.yml` starts CueFlow, its PostgreSQL and SearXNG. Every port is bound to `127.0.0.1`.
Publishing goes through an MCP server of your choice (or the bundled mock, below).

```bash
git clone https://github.com/j75689/cueflow && cd cueflow/deploy
cp .env.example .env
# fill in CUEFLOW_SECRET_KEY (openssl rand -base64 32) and SEARXNG_SECRET (openssl rand -hex 32)
docker compose up -d
```

- CueFlow: <http://localhost:3000>
- SearXNG: <http://localhost:8888>

## First setup

1. **CueFlow → Connectors**:
   - *MCP publisher* - the endpoint of a server that follows the [publisher contract](publisher-mcp.md), with a
     bearer token or secret headers (such as a Cloudflare Access service token), then press **Test**; you pick the
     accounts for each post in the review editor. To try the flow without posting anywhere, use the
     [mock publisher](#the-mock-publisher) below.
   - an LLM - e.g. *Anthropic Claude* (model `claude-opus-5-5`) or *OpenAI-compatible* pointing at Ollama.
   - one or more sources - e.g. an RSS feed.
   - optional: SearXNG/Tavily for web search, SMTP for review reminders.
2. Either:
   - **Command**: tell the agent team what you want, or
   - **Schedules**: run work on a timer. A schedule opens a task for an agent each time (it researches,
     reads originals and decides). Press **Run now** to try one.
3. **Deliverables**: edit drafts, pick the accounts, approve or reject. Approved posts are handed to the publisher
   within seconds; failures show the reason and a **Retry** button.

## Image variants

| `CUEFLOW_IMAGE_TARGET` | Contents |
|---|---|
| `slim` (default) | CueFlow only (distroless). Built-in agents and LLM connectors |
| `agents` | + Node.js and the ACP adapters for Claude Code and Codex, for external agents |

Set it in `deploy/.env`, then `docker compose up -d --build`. The `runner` target (agents plus git and gh,
started as `cueflow runner run`) is for [deploy/runner](../deploy/runner/README.md).

Each release also publishes all three, as `ghcr.io/j75689/cueflow:<version>` / `:latest` (slim, amd64 only),
`:<version>-agents` / `:agents` and `:<version>-runner` / `:runner` (both also arm64); the tags and how to build a runner with your own tools are in [runner-image.md](runner-image.md).

## Web search

The compose file includes SearXNG (set `SEARXNG_SECRET` in `deploy/.env`). Add a *SearXNG* connector with URL
`http://searxng:8080`; agents without a search connector of their own use the first active one.

## The mock publisher

```bash
cd deploy
docker compose --profile mock up -d cueflow cueflow-db mock-publisher
```

`mock-publisher` follows the publisher contract but posts nowhere. Create an *MCP publisher* connector with
endpoint `http://mock-publisher:4010/mcp` and bearer token `dev-token`; it offers a Threads, an Instagram and a
YouTube account plus a test-tagged one. Received posts are listed at <http://localhost:4010/posts>. Outside
Docker, `make mock` runs the same server on `:4010`.

## Remote access

Ports are bound to localhost on purpose. To review from your phone, use an SSH tunnel
(`ssh -L 3000:localhost:3000 nas`) or a VPN such as Tailscale, and set `CUEFLOW_ADMIN_TOKEN`
([configuration.md](configuration.md)).

## Data

The databases live in Docker volumes; `docker compose down -v` deletes them. Connector credentials are
encrypted with `CUEFLOW_SECRET_KEY`: keep a copy of it outside the machine, or a restored database is
unreadable ([rotating the key](configuration.md#rotating-the-secret-key)).
