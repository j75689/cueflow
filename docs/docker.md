# Running with Docker Compose

`deploy/docker-compose.yml` starts CueFlow, its PostgreSQL, Postiz (with its own database and Redis) and
SearXNG. Every port is bound to `127.0.0.1`.

```bash
git clone https://github.com/j75689/cueflow && cd cueflow/deploy
cp .env.example .env
# fill in CUEFLOW_SECRET_KEY (openssl rand -base64 32) and POSTIZ_JWT_SECRET (openssl rand -hex 32)
docker compose up -d
```

- CueFlow: <http://localhost:3000>
- Postiz: <http://localhost:4007>

## First setup

1. **Postiz**: create an account, connect your Threads / Instagram / X channels, and copy the API key from
   *Settings → Developers*.
2. **CueFlow → Connectors**:
   - *Postiz (MCP)* - endpoint `http://postiz:5000/api/mcp`, paste the API key, press **Test**.
     Or *MCP publisher* - the endpoint of a server that follows the [publisher contract](publisher-mcp.md), with a
     bearer token or secret headers (such as a Cloudflare Access service token); you then pick the Threads/Instagram
     accounts for each post in the review editor.
   - an LLM - e.g. *Anthropic Claude* (model `claude-opus-5-5`) or *OpenAI-compatible* pointing at Ollama.
   - one or more sources - e.g. an RSS feed.
   - optional: SearXNG/Tavily for web search, SMTP for review reminders.
3. Either:
   - **Command**: tell the agent team what you want, or
   - **Schedules**: run work on a timer. An *agent task* schedule opens a task for an agent each time
     (it researches, reads originals and decides); a *simple schedule* is a fixed collect → one LLM call →
     drafts flow (cron such as `0 8 * * *`, topic, platforms, instructions - see
     [prompt-templates.md](prompt-templates.md)). Press **Run now** to try one.
4. **Review**: edit drafts, approve or reject. Approved posts are handed to Postiz within seconds; failures
   show the reason and a **Retry** button.

## Image variants

| `CUEFLOW_IMAGE_TARGET` | Contents |
|---|---|
| `slim` (default) | CueFlow only (distroless). Built-in agents and LLM connectors |
| `agents` | + Node.js and the ACP adapters for Claude Code and Codex, for external agents |

Set it in `deploy/.env`, then `docker compose up -d --build`. The `runner` target (agents plus git and gh,
started as `cueflow runner run`) is for [deploy/runner](../deploy/runner/README.md).

## Web search

The compose file includes SearXNG (set `SEARXNG_SECRET` in `deploy/.env`). Add a *SearXNG* connector with URL
`http://searxng:8080`; agents without a search connector of their own use the first active one.

## Try it without Postiz

```bash
cd deploy
docker compose --profile mock up -d cueflow cueflow-db mock-postiz
```

Create a *Postiz (MCP)* connector with endpoint `http://mock-postiz:4007/api/mcp` and API key `dev-key`.
Received posts are listed at <http://localhost:4008/posts>.

## Remote access

Ports are bound to localhost on purpose. To review from your phone, use an SSH tunnel
(`ssh -L 3000:localhost:3000 nas`) or a VPN such as Tailscale, and set `CUEFLOW_ADMIN_TOKEN`
([configuration.md](configuration.md)).

## Data

The databases live in Docker volumes; `docker compose down -v` deletes them. Connector credentials are
encrypted with `CUEFLOW_SECRET_KEY`: keep a copy of it outside the machine, or a restored database is
unreadable ([rotating the key](configuration.md#rotating-the-secret-key)).
