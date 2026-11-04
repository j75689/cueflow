# Built-in connectors

Everything CueFlow talks to is a connector: publishers, sources, search engines, LLMs, notifiers, image
generators and MCP servers used as agent tools. Connectors are added under **Connectors** in the dashboard;
their forms are generated from the plugin's config schema. To write a new one, see
[connectors.md](connectors.md).

| Kind | Types |
|---|---|
| Publisher | `mcp_publisher` - any MCP server that follows the [publisher contract](publisher-mcp.md), posting to the Threads/Instagram accounts picked per post (threads, carousels, videos and reels; videos are uploaded straight to the server) |
| Source | `rss` (RSS/Atom, GUID-deduplicated) · `imap` (mailbox folder, read-only) |
| Search | `searxng` (self-hosted) · `tavily` |
| LLM | `openai` (OpenAI, OpenRouter, Azure OpenAI, DeepSeek, Ollama, vLLM, LocalAI, any OpenAI-compatible) · `anthropic` (Claude API) · `anthropic_bedrock` (Claude on Amazon Bedrock) · `anthropic_vertex` (Claude on Google Vertex AI) · `gemini_vertex` (Gemini on Google Vertex AI) |
| Notifier | `telegram` (a bot messages a chat, group, channel or forum topic) · `smtp` (email). Each org picks one for its task alerts ([agentic-flow §6.2](agentic-flow.md#62-how-runs-end)) |
| Action | `mcp_action` - hands approved deliverables to one tool of an MCP server you run (`deliver` by default, arguments `{id, kind, title, payload, previous}`); only the worker calls it |
| Agent tools | `mcp_server` - any MCP server (HTTP or stdio); each tool set to allow, ask (you approve each call) or deny. Over HTTP it can send secret headers (stored encrypted), such as a Cloudflare Access service token |
| Image generation | `openai_image` (OpenAI / Azure / compatible) · `gemini_image` (AI Studio key) · `vertex_image` (Vertex AI, ADC) · `comfyui` (self-hosted) |
| Runtime | `acp_agent` (an external agent on the CueFlow host) · `runner_pool` (an external agent on a [runner](external-agents.md#run-agents-on-your-own-machine-runners)) · `external` (a self-driven agent) |

## LLM authentication

| Connector | Ways to authenticate |
|---|---|
| Anthropic Claude | API key · OAuth access token (adds the required OAuth beta header) · Bearer token for gateways · named `ant auth login` profile · or blank to use the server environment (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_PROFILE`, the default profile) |
| Claude on Amazon Bedrock | Bedrock API key · AWS access key + secret (+ session token) · AWS profile · or blank to use the default AWS credential chain (env vars, `~/.aws`, IAM role) |
| Claude on Google Vertex AI | Service account key JSON · or blank to use Application Default Credentials |
| Gemini on Google Vertex AI | Same as above (for example `gcloud auth application-default login`). Needs a GCP project with billing enabled; billed by GCP, not by Google AI subscriptions |
| OpenAI-compatible | API key (Bearer; `api-key` header for Azure) · extra headers for gateways · none for local servers. Presets: OpenAI, OpenRouter, Azure OpenAI, DeepSeek, Ollama, vLLM, LocalAI |

Credentials entered in the UI are encrypted per connector (AES-256-GCM, bound to the connector row) and win
over server environment variables. For environment variables or credential files in Docker, see
`deploy/.env.example` and the commented `volumes:` example in `deploy/docker-compose.yml`.

## MCP server headers

The **MCP server** connector (`mcp_server`) sends these on every request over Streamable HTTP or SSE; stdio
sends no headers.

| Setting | Meaning |
|---|---|
| Bearer token | Sent as `Authorization: Bearer <token>`, stored encrypted |
| Extra headers | Plain headers, stored as they are |
| Secret headers | `Header-Name: value`, one per line, stored encrypted. Blank lines and lines starting with `#` are skipped |
| Sign requests with CueFlow's Google identity | An ID token in `X-Serverless-Authorization`, for a private Cloud Run service (see [below](#calling-private-services)) |

For a server behind Cloudflare Access, put the service token in **Secret headers**:

```
# Cloudflare Access service token
CF-Access-Client-Id: <client id>
CF-Access-Client-Secret: <client secret>
```

When the same header is set more than once, **Bearer token** wins over **Secret headers**, which wins over
**Extra headers**. Names match regardless of case (`x-env` and `X-Env` are one header). The Google identity
sets only `X-Serverless-Authorization`, so it can be combined with all three. A line without a `:` stops the
connector with `mcp server: secret headers: line N: want "Header-Name: value"`; the error never quotes the line.

**Upgrading.** Before Secret headers was added, an `mcp_server` connector with a bearer token and an Extra headers
entry such as `authorization` (any case but `Authorization`) sent one or the other at random on each request; now the
bearer token is always sent. Two Extra headers that differ only in case (`x-env` and `X-Env`) now send one value,
but which one is not fixed, so keep one spelling per name. A malformed Secret environment (stdio) line is
now reported as `mcp server: secret environment: line N: want KEY=VALUE`, without its value.

## Web search

Agents without a search connector of their own use the first active one. The Docker Compose file includes
SearXNG; see [docker.md](docker.md#web-search).

## Calling private services

A connector that points at a private Cloud Run service can sign its requests with CueFlow's own Google identity
(an option on MCP and HTTP connectors), so helper services such as SearXNG, an MCP server or a video renderer
need no credentials of their own. See [deploy/cloudrun/README.md](../deploy/cloudrun/README.md).
