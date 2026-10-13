# 內建 connector

CueFlow 對外的一切都是 connector：發布器、資料來源、搜尋引擎、LLM、通知、圖片生成，以及當作 agent 工具的 MCP 伺服器。在後台的 **Connectors** 新增，設定表單由插件的 schema 自動產生。想自己寫一個，見 [connectors.md](../connectors.md)（英文）。

| 類型 | 型別 |
|---|---|
| 發布器 | `postiz_mcp`：透過 MCP 連 Postiz（Streamable HTTP / SSE / stdio），可逐篇選擇頻道 · `mcp_publisher`：連任何符合[發布器約定](../publisher-mcp.md)（英文）的 MCP 伺服器，發到每篇貼文勾選的 Threads/Instagram 帳號（串文、輪播、影片與 Reels；影片直接上傳給伺服器） |
| 資料來源 | `rss`（RSS/Atom，以 GUID 去重）· `imap`（信箱資料夾，唯讀） |
| 搜尋 | `searxng`（自託管）· `tavily` |
| LLM | `openai`（OpenAI、OpenRouter、Azure OpenAI、DeepSeek、Ollama、vLLM、LocalAI 等 OpenAI 相容服務）· `anthropic`（Claude API）· `anthropic_bedrock`（Amazon Bedrock 上的 Claude）· `anthropic_vertex`（Google Vertex AI 上的 Claude）· `gemini_vertex`（Google Vertex AI 上的 Gemini） |
| 通知 | `smtp`（審核提醒與摘要信） |
| Agent 工具 | `mcp_server`：任何 MCP server（HTTP 或 stdio），每個工具可設為允許、詢問（每次呼叫由你核准）或禁止 |
| 圖片生成 | `openai_image`（OpenAI / Azure / 相容服務）· `gemini_image`（AI Studio key）· `vertex_image`（Vertex AI，ADC）· `comfyui`（自架） |
| Runtime | `acp_agent`（在 CueFlow 主機上跑外部 agent）· `runner_pool`（在 [Runner](external-agents.md#在自己的機器上跑-agentrunner) 上跑外部 agent）· `external`（自主驅動的 agent） |

## LLM 認證方式

| Connector | 可用的認證方式 |
|---|---|
| Anthropic Claude | API key · OAuth access token（自動加上必要的 OAuth beta header）· 給閘道用的一般 Bearer token · 指定 `ant auth login` 設定檔 · 或全部留空，使用伺服器環境（`ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_PROFILE`、預設設定檔） |
| Amazon Bedrock 上的 Claude | Bedrock API key · AWS access key + secret（+ session token）· AWS profile · 或留空，使用預設的 AWS 憑證鏈（環境變數、`~/.aws`、IAM role） |
| Google Vertex AI 上的 Claude | 服務帳號金鑰 JSON · 或留空，使用 Application Default Credentials |
| Google Vertex AI 上的 Gemini | 同上（例如 `gcloud auth application-default login`）。需要已開啟計費的 GCP 專案；費用由 GCP 計算，不吃 Google AI 訂閱額度 |
| OpenAI 相容 | API key（Bearer；Azure 用 `api-key` header）· 給閘道用的自訂 header · 本機服務不需要。預設值：OpenAI、OpenRouter、Azure OpenAI、DeepSeek、Ollama、vLLM、LocalAI |

在 UI 填的憑證會依 connector 分別加密保存（AES-256-GCM，綁定 connector 那一列資料），優先於伺服器環境變數。Docker 想改用環境變數或憑證檔，見 `deploy/.env.example` 與 `deploy/docker-compose.yml` 裡註解掉的 `volumes:` 範例。

## 網頁搜尋

沒有指定搜尋 connector 的 agent 會使用第一個啟用中的。Docker Compose 內含 SearXNG，見 [docker.md](docker.md#網頁搜尋)。

## 呼叫私有服務

指向私有 Cloud Run 服務的 connector 可以用 CueFlow 自己的 Google 身分簽署請求（MCP 與 HTTP connector 的一個選項），所以 SearXNG、MCP 伺服器、影片渲染器這類輔助服務不需要自己的憑證。見 [deploy/cloudrun/README.md](../../deploy/cloudrun/README.md)（英文）。
