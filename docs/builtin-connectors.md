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
| Agent tools | `mcp_server` - any MCP server (HTTP or stdio); each tool set to allow, ask (you approve each call) or deny |
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

## Web search

Agents without a search connector of their own use the first active one. The Docker Compose file includes
SearXNG; see [docker.md](docker.md#web-search).

## Calling private services

A connector that points at a private Cloud Run service can sign its requests with CueFlow's own Google identity
(an option on MCP and HTTP connectors), so helper services such as SearXNG, an MCP server or a video renderer
need no credentials of their own. See [deploy/cloudrun/README.md](../deploy/cloudrun/README.md).
