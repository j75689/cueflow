# CueFlow

> English: [README.md](README.md)

**開源、可自託管的社群 Agent 運營控制台。** CueFlow 會定時喚醒，從 RSS、信箱、網路搜尋收集素材，讓 LLM 依各平台風格寫出草稿，每一篇都先交給人審核，核准後才透過 Model Context Protocol (MCP) 交給 [Postiz](https://github.com/gitroomhq/postiz-app) 發布。不需要 Public IP、網域或 Webhook，家裡的 NAS、Home Lab 或筆電上的 Docker 就能跑。

```
 Cron ─▶ 收集（RSS · IMAP · 搜尋）─▶ Agent（LLM 工具迴圈）─▶ PENDING_REVIEW
                                                              │  人工核准
                                                              ▼
                 Postiz ◀── MCP ── 發布 worker ◀── APPROVED → QUEUED ─▶ PUBLISHED / FAILED
```

## Agent 團隊（Command · Board · Agents）

CueFlow 內建一組可以用自然語言交代事情的 agent，像聊天機器人一樣，但它們會真的去做事，而且整個過程都攤在看板上：

- **Command（指令中心）**：直接說你要什麼，例如「每天早上 8 點整理 AI 新聞並寫成 Threads 草稿」。管理型 agent（🧭 Chief，總管）會規劃步驟、把子任務分派給隊友、設定定時排程，最後回報結果。
- **Board（看板）**：每個需求都是一張任務卡，依序經過 Backlog → 待辦 → 進行中 → 等待中 → 待確認 → 完成。把指派了 agent 的卡片拖到「待辦」，它就會開始工作。每個任務都有一條對話串，記錄 agent 的進度、工具呼叫、提問與成果；回覆就能讓它繼續，用 `@writer` 開頭則可以把任務轉給另一個 agent。
- **Agents（角色）**：每個角色有自己的人設、能力、LLM、資料來源與記憶。預設有四位：🧭 Chief（總管：規劃與委派）、🔎 Researcher（研究員：附來源的研究）、✍️ Writer（文案：各平台草稿）、🧐 Editor（編輯：檢查與修稿）。預設的名稱與指示是英文，但它們回覆與寫稿用的語言由各自的 `language` 設定決定（預設英文 `en`，要用繁體中文請改成 `zh-TW`）。也可以自己新增角色。
- **Schedules（排程）**：定時為 agent 建立任務；agent 也能自己建立排程，最密每 5 分鐘一次。
- **外部 agent（ACP）**：agent 可以改由 Claude Code、Codex、Gemini CLI、OpenCode 或 Goose 執行任務，透過 [Agent Client Protocol](https://agentclientprotocol.com) 溝通，取代內建的 LLM 迴圈。新增一個「External agent (ACP)」runtime connector，再把它設為 agent 的 *Runtime* 即可。外部 agent 透過每次執行專屬的 MCP 端點使用 CueFlow 的工具；shell、改檔、讀取本機檔案一律拒絕，寫的草稿一樣要進審核。用 Claude Code 時可以使用 Claude 訂閱方案：執行 `claude setup-token`，把產生的 token 貼進 connector（預設的 Docker 映像已內建 Claude Code 與 Codex 的橋接器）。

| 能力 | 工具 |
|---|---|
| research | `web_search`、`read_url`（只能讀公開網址）、`search_materials`、`fetch_sources` |
| drafting | `create_post_draft`、`list_posts`、`revise_draft` |
| delegation | `create_subtask`、`list_agents`、`wait_for_subtasks` |
| planning | `list_tasks`、`get_task`、`create_task`、`schedule_task`、`list_schedules`、`pause_schedule` |
| memory | `remember`、`search_memory` |
| （所有 agent） | `add_comment`、`ask_human`、`complete_task` |

Agent 永遠不會發文。它們寫的草稿和其他貼文一樣會進 **Review（審核）**，只有人核准後才會交給 Postiz。

## 特色

- **從架構上保證人工審核。** 貼文遵循嚴格的狀態機 `DRAFT → PENDING_REVIEW → APPROVED | REJECTED → QUEUED → PUBLISHED | FAILED`（另有 `FAILED → QUEUED` 手動重試），每次變動都寫入稽核紀錄，包含審核者修改前後的差異。
- **Agent 無法發文。** 沒有任何 agent 工具能發文；發布器也被包了一層，除非發布 worker 針對「那一篇已核准的貼文」授權，否則 `Publish` 一律失敗（FR-4.2）。
- **一切都是 connector 插件。** 發布器、資料來源、搜尋引擎、LLM、通知都實作簡單的 Go 介面，並附設定 schema 自行註冊，UI 會自動產生設定表單。見 [docs/connectors.md](docs/connectors.md)（英文）。
- **不對外開放。** 只做主動輪詢；伺服器只綁 `127.0.0.1:3000`。
- **機密加密儲存。** 使用 AES-256-GCM，並綁定 connector 那一列資料。
- **發布不怕當機。** 發布使用租約；worker 若在發布途中掛掉，貼文會標成 `FAILED` 並提醒「重試前請先確認平台」，而不是默默重複發文。
- **單一 binary。** Go 的 API、排程與 worker，加上內嵌的 Next.js 控制台。

## 內建 connector

| 類型 | 型別 |
|---|---|
| 發布器 | `postiz_mcp`：透過 MCP 連 Postiz（Streamable HTTP / SSE / stdio） |
| 資料來源 | `rss`（RSS/Atom，以 GUID 去重）· `imap`（信箱資料夾，唯讀） |
| 搜尋 | `searxng`（自託管）· `tavily` |
| LLM | `openai`（OpenAI、OpenRouter、Azure OpenAI、DeepSeek、Ollama、vLLM、LocalAI 等 OpenAI 相容服務）· `anthropic`（Claude API）· `anthropic_bedrock`（Amazon Bedrock 上的 Claude）· `anthropic_vertex`（Google Vertex AI 上的 Claude）· `gemini_vertex`（Google Vertex AI 上的 Gemini） |
| 通知 | `smtp`（審核提醒與摘要信） |

### LLM 認證方式

| Connector | 可用的認證方式 |
|---|---|
| Anthropic Claude | API key · OAuth access token（自動加上必要的 OAuth beta header）· 給閘道用的一般 Bearer token · 指定 `ant auth login` 設定檔 · 或全部留空，使用伺服器環境（`ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_PROFILE`、預設設定檔） |
| Amazon Bedrock 上的 Claude | Bedrock API key · AWS access key + secret（+ session token）· AWS profile · 或留空，使用預設的 AWS 憑證鏈（環境變數、`~/.aws`、IAM role） |
| Google Vertex AI 上的 Claude | 服務帳號金鑰 JSON · 或留空，使用 Application Default Credentials |
| Google Vertex AI 上的 Gemini | 同上：服務帳號金鑰，或 Application Default Credentials（例如本機 `gcloud auth application-default login` 的登入）。需要已開啟計費的 GCP 專案；費用由 GCP 計算，不吃 Gemini / Google AI 訂閱額度 |
| OpenAI 相容 | API key（Bearer；Azure 用 `api-key` header）· 給閘道用的自訂 header · 本機服務不需要。預設值：OpenAI、**OpenRouter**（已預填網址與來源標示 header；使用 `sk-or-...` key，模型名稱如 `anthropic/claude-opus-4.8`）、**Azure OpenAI**（v1 endpoint，或填 API version 走舊版 deployments endpoint）、DeepSeek、Ollama、vLLM、LocalAI |

在 UI 填的憑證會依 connector 分別加密保存，優先於伺服器層級的環境變數。用 Docker 時若要改用環境變數或憑證檔，請在 `deploy/.env` 設定，並參考 `deploy/docker-compose.yml` 裡註解掉的 `volumes:` 範例（掛載 `~/.config/anthropic`、`~/.aws`、`~/.config/gcloud` 或 GCP 服務帳號金鑰）。

## 快速開始（Docker）

```bash
git clone https://github.com/j75689/cueflow && cd cueflow/deploy
cp .env.example .env
# 填入 CUEFLOW_SECRET_KEY（openssl rand -base64 32）與 POSTIZ_JWT_SECRET（openssl rand -hex 32）
docker compose up -d
```

- CueFlow：<http://localhost:3000>
- Postiz：<http://localhost:4007>

接著：

1. **Postiz**：建立帳號，連結你的 Threads / Instagram / X 帳號，並到 *Settings → Developers* 複製 API key。
2. **CueFlow → Connectors**：
   - *Postiz (MCP)*：endpoint 填 `http://postiz:5000/api/mcp`，貼上 API key，按 **Test**。
   - 一個 LLM：例如 *Anthropic Claude*（model `claude-opus-5-5`），或指向 Ollama 的 *OpenAI-compatible*。
   - 一個以上的資料來源，例如 RSS feed。
   - 選用：SearXNG / Tavily 用於網路搜尋，SMTP 用於審核提醒。
3. 二選一：
   - **Command**：直接告訴 agent 團隊要做什麼。
   - **Pipelines**：建立固定格式的內容管線（cron 例如 `0 8 * * *`、時區、主題、平台、instructions，見 [docs/zh-TW/prompt-templates.md](docs/zh-TW/prompt-templates.md)），按 **Run now**。
4. **Review**：修改草稿，核准或退回。核准的貼文幾秒內就會交給 Postiz；失敗時會顯示原因並提供 **Retry** 按鈕。

### 映像版本

| `CUEFLOW_IMAGE_TARGET` | 內容 |
|---|---|
| `slim`（預設） | 只有 CueFlow（distroless），內建 agent 與 LLM connector |
| `agents` | 加上 Node.js 與 Claude Code、Codex 的 ACP 橋接器，可使用外部 agent |

在 `deploy/.env` 設定後執行 `docker compose up -d --build`。

### 不接 Postiz 先試用

```bash
cd deploy
docker compose --profile mock up -d cueflow cueflow-db mock-postiz
```

建立一個 *Postiz (MCP)* connector，endpoint 填 `http://mock-postiz:4007/api/mcp`，API key 填 `dev-key`。收到的貼文會列在 <http://localhost:4008/posts>。

### 遠端存取

Port 刻意只綁 localhost。想用手機審核的話，請用 SSH tunnel（`ssh -L 3000:localhost:3000 nas`）或 Tailscale 等 VPN，並設定 `CUEFLOW_ADMIN_TOKEN`。

## 設定

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `DATABASE_URL` | `postgres://cueflow:cueflow@localhost:5432/cueflow?sslmode=disable` | PostgreSQL |
| `CUEFLOW_SECRET_KEY` | *（必填）* | 加密 connector 機密用的 32 bytes 金鑰（base64 或 hex），請備份 |
| `CUEFLOW_SECRET_KEY_PREVIOUS` | | 換金鑰期間仍可用來解密的舊金鑰，多把用逗號分隔 |
| `CUEFLOW_ADDR` | `127.0.0.1:3000` | 監聽位址 |
| `CUEFLOW_ADMIN_TOKEN` | | 設定後 API 與 UI 需要 `Authorization: Bearer <token>` |
| `CUEFLOW_PUBLIC_URL` | `http://localhost:3000` | 通知信裡的連結 |
| `CUEFLOW_DIGEST_CRON` | | 例如 `0 9 * * *`：每天寄一次待審摘要 |
| `CUEFLOW_DISPATCH_INTERVAL` | `3s` | 發布 worker 的輪詢間隔 |
| `CUEFLOW_DISPATCH_LEASE` | `5m` | 一次發布最長可以花多久，超過就視為當機 |
| `CUEFLOW_PUBLISH_TIMEOUT` | `2m` | 單次發布呼叫的逾時 |
| `CUEFLOW_LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |
| `CUEFLOW_DISABLE_SCHEDULER` | `false` | 不啟用 cron，只跑 API 與 worker |
| `CUEFLOW_AGENT_CONCURRENCY` | `3` | 同時執行的 agent 任務數 |
| `CUEFLOW_AGENT_ALLOW_PRIVATE_URLS` | `false` | 允許 agent 的 `read_url` 存取內網與 loopback 位址 |

### 更換加密金鑰

connector 的憑證都用 `CUEFLOW_SECRET_KEY` 加密；金鑰弄丟就得重新輸入所有憑證，請另外存一份到密碼管理器。要更換金鑰，又不想停機或重填憑證：

1. 產生新金鑰（`cueflow genkey`），設為 `CUEFLOW_SECRET_KEY`，把舊金鑰移到 `CUEFLOW_SECRET_KEY_PREVIOUS`，重啟。之後新資料用新金鑰加密，舊資料也還讀得到。
2. 全部重新加密：`docker compose exec cueflow cueflow rotate-key`（可以先加 `--dry-run` 試跑）。只要有任何一個 connector 解不開，就不會修改任何資料。
3. 拿掉 `CUEFLOW_SECRET_KEY_PREVIOUS`，再重啟一次。

注意：`docker compose down -v` 會刪除 volume，也就是 CueFlow 和 Postiz 的資料庫。

## 開發

需求：Go 1.27+、Node 22+、Docker（跑 PostgreSQL 用）。

```bash
make test-db                      # 在 :55432 啟動一個拋棄式 PostgreSQL
export DATABASE_URL="postgres://cueflow:cueflow@localhost:55432/cueflow?sslmode=disable"
export CUEFLOW_SECRET_KEY=$(go run ./cmd/cueflow genkey)

go run ./cmd/cueflow              # API，:3000
go run ./cmd/mock-postiz-mcp      # 模擬 Postiz，:4007（key 為 dev-key）
cd web && npm install && npm run dev   # 控制台，:3001，/api 轉發到 :3000

make test                         # 單元測試
make test-e2e                     # 對 PostgreSQL 跑完整鏈路
make build                        # 產生內嵌控制台的 bin/cueflow
```

程式碼結構見 [README.md](README.md#layout)。

## 文件

- [整體架構設計](docs/zh-TW/architecture.md)
- [Agentic flow 設計](docs/zh-TW/agentic-flow.md)
- [Prompt 範本](docs/zh-TW/prompt-templates.md)
- 英文文件：[REST API](docs/api.md) · [Connector 插件開發](docs/connectors.md)

## 授權

Apache-2.0
