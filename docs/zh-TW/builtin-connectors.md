# 內建 connector

CueFlow 對外的一切都是 connector：發布器、資料來源、搜尋引擎、LLM、通知、圖片生成，以及當作 agent 工具的 MCP 伺服器。在後台的 **Connectors** 新增，設定表單由插件的 schema 自動產生。想自己寫一個，見 [connectors.md](../connectors.md)（英文）。

| 類型 | 型別 |
|---|---|
| 發布器 | `mcp_publisher`：連任何符合[發布器約定](../publisher-mcp.md)（英文）的 MCP 伺服器，發到每篇貼文勾選的 Threads/Instagram 帳號（串文、輪播、影片與 Reels；影片直接上傳給伺服器） |
| 資料來源 | `rss`（RSS/Atom，以 GUID 去重）· `imap`（信箱資料夾，唯讀） |
| 搜尋 | `searxng`（自託管）· `tavily` |
| LLM | `openai`（OpenAI、OpenRouter、Azure OpenAI、DeepSeek、Ollama、vLLM、LocalAI 等 OpenAI 相容服務）· `anthropic`（Claude API）· `anthropic_bedrock`（Amazon Bedrock 上的 Claude）· `anthropic_vertex`（Google Vertex AI 上的 Claude）· `gemini_vertex`（Google Vertex AI 上的 Gemini） |
| 通知 | `telegram`（由 bot 發到私訊、群組、頻道或論壇主題）· `smtp`（email）。每個 org 各自選一個接收任務通知（[agentic-flow §6.2](agentic-flow.md#62-結束條件與狀態對應)） |
| 動作 | `mcp_action`：把核准的 deliverable 交給你自己 MCP 伺服器上的一個工具（預設 `deliver`，參數 `{id, kind, title, payload, previous}`）；只有 worker 會呼叫 |
| Agent 工具 | `mcp_server`：任何 MCP server（HTTP 或 stdio），每個工具可設為允許、詢問（每次呼叫由你核准）或禁止。走 HTTP 時可帶機密標頭（加密儲存），例如 Cloudflare Access service token |
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

## MCP 伺服器的標頭

**MCP server** connector（`mcp_server`）走 Streamable HTTP 或 SSE 時，每個請求都會帶上下列標頭；stdio 不送標頭。

| 設定 | 說明 |
|---|---|
| Bearer token | 以 `Authorization: Bearer <token>` 送出，加密儲存 |
| Extra headers | 一般標頭，以明文儲存 |
| Secret headers | 每行一個 `Header-Name: value`，加密儲存。空白行與 `#` 開頭的行會略過 |
| Sign requests with CueFlow's Google identity | 在 `X-Serverless-Authorization` 帶 ID token，給私有 Cloud Run 服務用（見[下方](#呼叫私有服務)） |

伺服器在 Cloudflare Access 後面時，把 service token 填進 **Secret headers**：

```
# Cloudflare Access service token
CF-Access-Client-Id: <client id>
CF-Access-Client-Secret: <client secret>
```

同一個標頭設了不只一次時，**Bearer token** 優先於 **Secret headers**，**Secret headers** 又優先於 **Extra headers**。標頭名稱不分大小寫（`x-env` 與 `X-Env` 是同一個）。Google 身分只設定 `X-Serverless-Authorization`，可以和上面三者並用。Secret headers 有一行沒有 `:`、標頭名稱不合法或值是空的時，connector 會在測試與儲存時以 `mcp server: secret headers: line N: ...` 失敗；錯誤訊息不會引用該行內容。Extra headers 的名稱不合法或值含控制字元時也會失敗，錯誤訊息會指出標頭名稱，但不帶出值。stdio 不會讀取這兩個欄位。

**升級須知。** 加入 Secret headers 之前，同時設了 Bearer token 與名為 `authorization`（`Authorization` 以外的任何大小寫）的 Extra headers 的 `mcp_server` connector，每個請求會隨機送出其中一個；現在一律送 Bearer token。兩個只差在大小寫的 Extra headers（`x-env` 與 `X-Env`）現在只送一個值，但送哪一個並不固定，所以每個名稱請只用一種寫法。stdio 的 Secret environment 有格式錯誤的行時，現在回報為 `mcp server: secret environment: line N: want KEY=VALUE`，不再帶出該行的值。Secret headers 值是空的或名稱不合法、或 Extra headers 名稱不合法的設定，以前可以儲存，但每個請求都會失敗（空值則是照樣送出空的標頭）；現在測試與儲存時就會回報。

## 網頁搜尋

沒有指定搜尋 connector 的 agent 會使用第一個啟用中的。Docker Compose 內含 SearXNG，見 [docker.md](docker.md#網頁搜尋)。

## 呼叫私有服務

指向私有 Cloud Run 服務的 connector 可以用 CueFlow 自己的 Google 身分簽署請求（MCP 與 HTTP connector 的一個選項），所以 SearXNG、MCP 伺服器、影片渲染器這類輔助服務不需要自己的憑證。見 [deploy/cloudrun/README.md](../../deploy/cloudrun/README.md)（英文）。
