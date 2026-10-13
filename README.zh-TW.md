# CueFlow

> English: [README.md](README.md) · 雲端部署：[deploy/cloudrun](deploy/cloudrun/README.md)

**開源、可自託管的 Agent 運營控制台。** 把任務交給看板上的一組 AI agent：它們用各自的工具完成任務，需要你決定時會來問，有風險的動作會等你核准。Agent 可以用內建的 LLM 迴圈，或把工作交給你自己機器上（*Runner*）的外部 agent（Claude Code、Codex、Gemini CLI⋯）；任何 [MCP](https://modelcontextprotocol.io) 伺服器都能當它的工具，每個工具可設為允許、需核准或禁止；任務可以定時重複。可以在自己的機器上跑（家裡的 NAS、Home Lab 或筆電上的 Docker，不需要 Public IP、網域或 Webhook），也能部署到 Google Cloud Run，沒工作時縮到零。

```
 你 · 排程 · MCP client ─▶ 任務 ─▶ Agent ─▶ 工具（內建 · MCP 伺服器 · Runner 上自己的 CLI）
                                     │
                     需要你決定 / 有風險的工具呼叫 ─▶ 你在對話串裡核准 ─▶ 它繼續
```

內建一套**內容工作流程**當範例：agent 收集素材（RSS、信箱、網路搜尋），依各平台風格寫草稿，每一篇都先交給人審核，核准後才透過 MCP 交給 [Postiz](https://github.com/gitroomhq/postiz-app) 發布。沒有 `drafting` 能力的 agent 完全不會碰到這些。

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
- **Agents（角色）**：每個角色有自己的人設、能力、LLM、資料來源與記憶。全新安裝只有一位：🧭 Chief（總管），負責規劃、委派給你新增的 agent，沒有合適的人選時自己動手。可以自己新增角色（文案、研究員、編輯⋯），每個角色都能修改。預設的名稱與指示是英文，回覆與寫稿用的語言由各自的 `language` 設定決定（預設英文 `en`，要用繁體中文請改成 `zh-TW`）。
- **Schedules（排程）**：定時為 agent 建立任務；agent 也能自己建立排程，最密每 5 分鐘一次。
- **外部 agent（ACP）**：agent 可以改由 Claude Code、Codex、Gemini CLI、OpenCode 或 Goose 執行任務，透過 [Agent Client Protocol](https://agentclientprotocol.com) 溝通，取代內建的 LLM 迴圈。新增一個「External agent (ACP)」runtime connector，再把它設為 agent 的 *Runtime* 即可。它透過每次執行專屬的 MCP 端點使用 CueFlow 的工具；shell 與檔案存取預設拒絕，除非 Runner 的擁有者允許（要當 coding agent 用請見 [deploy/runner](deploy/runner/README.md)，英文），寫的草稿一樣要進審核。用 Claude Code 時可以使用 Claude 訂閱方案（`claude setup-token`，把 token 貼進 connector）。

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
- **Agent 無法發文。** 沒有任何 agent 工具能發文；發布器也被包了一層，除非發布 worker 針對「那一篇已核准的貼文」授權，否則 `Publish` 一律失敗。
- **一切都是 connector 插件。** 發布器、資料來源、搜尋引擎、LLM、通知都實作簡單的 Go 介面，並附設定 schema 自行註冊，UI 會自動產生設定表單。見 [docs/connectors.md](docs/connectors.md)（英文）。
- **預設私有。** 在自己機器上只做主動連線，伺服器只綁 `127.0.0.1:3000`；在雲端，儀表板放在登入（Cloudflare Access）後面，機器之間的呼叫都帶 Google 簽發的身分。
- **哪裡都能跑。** 自己的硬體用 Docker Compose；或 Cloud Run 搭配 Cloud Tasks 與 Cloud Scheduler 負責工作與時鐘，沒工作時不佔資源。Agent 也能跑在你自己的機器上（[Runner](#在自己的機器上跑-agentrunner)）。
- **媒體不礙事。** 圖片與影片可放資料庫、磁碟或 Google Cloud Storage，用簽名網址提供，沒在用的舊檔可以自動清掉。
- **機密加密儲存。** 使用 AES-256-GCM，並綁定 connector 那一列資料。
- **發布不怕當機。** 發布使用租約；worker 若在發布途中掛掉，貼文會標成 `FAILED` 並提醒「重試前請先確認平台」，而不是默默重複發文。
- **單一 binary。** Go 的 API、排程與 worker，加上內嵌的 Next.js 控制台。

## 內建 connector

| 類型 | 型別 |
|---|---|
| 發布器 | `postiz_mcp`：透過 MCP 連 Postiz（Streamable HTTP / SSE / stdio），可逐篇選擇頻道 · `mcp_publisher`：連任何符合[發布器約定](docs/publisher-mcp.md)（英文）的 MCP 伺服器，發到每篇貼文勾選的 Threads/Instagram 帳號（串文、輪播、影片與 Reels；影片直接上傳給伺服器） |
| 資料來源 | `rss`（RSS/Atom，以 GUID 去重）· `imap`（信箱資料夾，唯讀） |
| 搜尋 | `searxng`（自託管）· `tavily` |
| LLM | `openai`（OpenAI、OpenRouter、Azure OpenAI、DeepSeek、Ollama、vLLM、LocalAI 等 OpenAI 相容服務）· `anthropic`（Claude API）· `anthropic_bedrock`（Amazon Bedrock 上的 Claude）· `anthropic_vertex`（Google Vertex AI 上的 Claude）· `gemini_vertex`（Google Vertex AI 上的 Gemini） |
| 通知 | `smtp`（審核提醒與摘要信） |
| Agent 工具 | `mcp_server`：任何 MCP server（HTTP 或 stdio），每個工具可設為允許、詢問（每次呼叫由你核准）或禁止 |
| 圖片生成 | `openai_image`（OpenAI / Azure / 相容服務）· `gemini_image`（AI Studio key）· `vertex_image`（Vertex AI，ADC）· `comfyui`（自架） |

### LLM 認證方式

| Connector | 可用的認證方式 |
|---|---|
| Anthropic Claude | API key · OAuth access token（自動加上必要的 OAuth beta header）· 給閘道用的一般 Bearer token · 指定 `ant auth login` 設定檔 · 或全部留空，使用伺服器環境（`ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_PROFILE`、預設設定檔） |
| Amazon Bedrock 上的 Claude | Bedrock API key · AWS access key + secret（+ session token）· AWS profile · 或留空，使用預設的 AWS 憑證鏈（環境變數、`~/.aws`、IAM role） |
| Google Vertex AI 上的 Claude | 服務帳號金鑰 JSON · 或留空，使用 Application Default Credentials |
| Google Vertex AI 上的 Gemini | 同上（例如 `gcloud auth application-default login`）。需要已開啟計費的 GCP 專案；費用由 GCP 計算，不吃 Google AI 訂閱額度 |
| OpenAI 相容 | API key（Bearer；Azure 用 `api-key` header）· 給閘道用的自訂 header · 本機服務不需要。預設值：OpenAI、OpenRouter、Azure OpenAI、DeepSeek、Ollama、vLLM、LocalAI |

在 UI 填的憑證會依 connector 分別加密保存，優先於伺服器環境變數。Docker 想改用環境變數或憑證檔，見 `deploy/.env.example` 與 `deploy/docker-compose.yml` 裡註解掉的 `volumes:` 範例。

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
     或 *MCP publisher*：endpoint 填符合[發布器約定](docs/publisher-mcp.md)（英文）的伺服器網址，加上 Bearer token 或機密標頭（例如 Cloudflare Access service token）；之後在審核頁為每篇貼文勾選要發的 Threads/Instagram 帳號。
   - 一個 LLM：例如 *Anthropic Claude*（model `claude-opus-5-5`），或指向 Ollama 的 *OpenAI-compatible*。
   - 一個以上的資料來源，例如 RSS feed。
   - 選用：SearXNG / Tavily 用於網路搜尋，SMTP 用於審核提醒。
3. 二選一：
   - **Command**：直接告訴 agent 團隊要做什麼。
   - **Schedules**：定時執行。*Agent 任務*排程每次開一張任務給 agent（它會搜尋、讀原文、自己判斷）；*簡單排程*是固定流程：收集素材 → 一次 LLM 呼叫 → 產生草稿（cron 例如 `0 8 * * *`、主題、平台、instructions，見 [docs/zh-TW/prompt-templates.md](docs/zh-TW/prompt-templates.md)）。按 **Run now** 試跑。
4. **Review**：修改草稿，核准或退回。核准的貼文幾秒內就會交給 Postiz；失敗時會顯示原因並提供 **Retry** 按鈕。

### 映像版本

| `CUEFLOW_IMAGE_TARGET` | 內容 |
|---|---|
| `slim`（預設） | 只有 CueFlow（distroless），內建 agent 與 LLM connector |
| `agents` | 加上 Node.js 與 Claude Code、Codex 的 ACP 橋接器，可使用外部 agent |

在 `deploy/.env` 設定後執行 `docker compose up -d --build`。

### 網頁搜尋

compose 內含 SearXNG（請在 `deploy/.env` 設定 `SEARXNG_SECRET`）。新增 *SearXNG* connector，URL 填 `http://searxng:8080`；沒有指定搜尋 connector 的 agent 會使用第一個啟用中的。

### 不接 Postiz 先試用

```bash
cd deploy
docker compose --profile mock up -d cueflow cueflow-db mock-postiz
```

建立一個 *Postiz (MCP)* connector，endpoint 填 `http://mock-postiz:4007/api/mcp`，API key 填 `dev-key`。收到的貼文會列在 <http://localhost:4008/posts>。

### 圖片與影片

貼文用 `/media/{id}` 引用媒體（圖片最大 10 MB；MP4 最大 100 MB）。可以在編輯器上傳、用圖片生成 connector 產生，或讓 agent 附上工具的產物（例如渲染好的 Reel）。

- **位元組放哪**：`CUEFLOW_MEDIA_STORAGE` 留空（資料庫）、`file:///data/media` 或 `gcs://bucket/prefix`。`cueflow media migrate --to <target>` 搬移既有媒體，網址不變。
- **刪除**：`DELETE /api/v1/media/{id}` 刪除該項目與檔案；只要還有尚未發布或退回的貼文在用，就回 `409`。`cueflow media prune --older-than 90d [--dry-run]` 一次清掉所有舊的、沒人用的；雲端模式可用 `CUEFLOW_MEDIA_RETENTION_DAYS` 讓它定時執行。
- **受信任的渲染服務傳大檔**：有自己 Google 身分的服務可以把影片直接寫進 bucket 的 `uploads/`，再呼叫 `POST /internal/media/register` 讓 CueFlow 收編，位元組不經過 CueFlow（[docs/api.md](docs/api.md#media)，英文）。

### 讓其他 agent 操作 CueFlow（MCP）

CueFlow 在 `/mcp` 提供 MCP 端點，讓外部 agent（Claude Code、Codex、Cursor⋯）幫你操作看板：查看 agent 與任務、
建立並指派任務、留言（也就是回覆正在等你的 agent）、移動、啟動或停止任務，以及查看待審核的草稿。它不能核准或發布貼文，
這一步仍然只在後台進行。

在 `deploy/.env` 設定 `CUEFLOW_MCP_TOKEN`（`openssl rand -hex 32`），重啟後以 Claude Code 為例：

```bash
claude mcp add --transport http cueflow http://localhost:3000/mcp \
  --header "Authorization: Bearer $CUEFLOW_MCP_TOKEN"
```

### 在自己的機器上跑 agent（Runner）

**Runner** 是你自己的機器，像 CI 的 self-hosted runner 一樣替 agent 執行任務。它只對外連線，在本機用*自己的*憑證執行
agent CLI（Claude Code、Codex⋯），agent 仍然透過 CueFlow 的工具作業，所以草稿照樣進審核、不會自動發布。
本機的 CueFlow 或別處的 CueFlow 都能用。

1. 後台 **Runners → Add runner**，複製畫面上的指令。
2. 在那台機器上（需要 `cueflow` 執行檔與 agent CLI）：
   ```bash
   cueflow runner register --url http://localhost:3000 --token cfreg_... --name mbp
   cueflow runner run
   ```
3. 在 **Connectors** 新增 **Runner pool**（標籤 `claude-code`），再把它選為 agent 的 *Runtime*。

本機政策優先：Runner 自己的 `runner.json` 只能縮小伺服器的要求（允許的 preset、禁用網路、要執行的指令、ACP 登入方式），`cueflow runner status` 可檢查連線，在後台撤銷 Runner 會立即讓它停止。細節與範例見 [docs/api.md](docs/api.md#runners)（英文）。

想讓 agent 碰不到你自己的登入，可以把 Runner 放進容器：它只拿到專屬的 Claude token、另給的 GitHub token 和乾淨的工作目錄，見 [deploy/runner](deploy/runner/README.md)（英文）。

Agent 也可以完全自己控制：把它的 Runtime 設為 **External (self-driven)**，在 agent 編輯頁建立 agent token，再用任何
MCP client 帶著 token 連 `/mcp`。Agent 用 `claim_task` 領自己的任務，並使用同一套工具。詳見 [`docs/api.md`](docs/api.md#runners)。

### 部署到 Google Cloud

同一個 binary 可以跑在 Cloud Run：Supabase（PostgreSQL）、Cloud Storage 放媒體、Cloud Tasks 派送背景工作、Cloud Scheduler 當時鐘（`CUEFLOW_QUEUE=cloudtasks`、`CUEFLOW_TICK=external`），沒工作時縮到零。儀表板用 Cloudflare Access 登入（服務自己驗證 Access token）；Runner、MCP client 等機器用服務網址和各自的 token。SearXNG、MCP 伺服器、影片渲染器這類輔助服務可以跑成私有的 Cloud Run 服務，用 CueFlow 的 Google 身分呼叫（connector 的一個選項）。步驟見 [deploy/cloudrun/README.md](deploy/cloudrun/README.md)（英文）。

### 遠端存取（Docker）

Port 刻意只綁 localhost。想用手機審核的話，請用 SSH tunnel（`ssh -L 3000:localhost:3000 nas`）或 Tailscale 等 VPN，並設定 `CUEFLOW_ADMIN_TOKEN`。

## 設定

環境變數；Docker 的預設值在 `deploy/.env.example`。

### 基本

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `DATABASE_URL` | `postgres://cueflow:cueflow@localhost:5432/cueflow?sslmode=disable` | PostgreSQL |
| `CUEFLOW_SECRET_KEY` | *（必填）* | 加密 connector 機密用的 32 bytes 金鑰（base64 或 hex），請備份 |
| `CUEFLOW_SECRET_KEY_PREVIOUS` | | 換金鑰期間仍可用來解密的舊金鑰，多把用逗號分隔 |
| `CUEFLOW_ADDR` | `127.0.0.1:3000` | 監聽位址 |
| `CUEFLOW_ADMIN_TOKEN` | | 設定後 API 與 UI 需要 `Authorization: Bearer <token>` |
| `CUEFLOW_MCP_TOKEN` | | 啟用給外部 agent 用的 MCP 端點 `/mcp`（Bearer token） |
| `CUEFLOW_PUBLIC_URL` | `http://localhost:3000` | 通知信裡的連結 |
| `CUEFLOW_DIRECT_URL` | `CUEFLOW_PUBLIC_URL` | 機器連到伺服器用的網址（Runner 註冊與 MCP 指令、媒體網址）：它們過不了 Cloudflare Access 這類瀏覽器登入 |
| `CUEFLOW_LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |
| `CUEFLOW_DB_MAX_CONNS` | 驅動預設 | 此程序的資料庫連線上限（小型資料庫建議每個實例約 5） |

### 發布與排程

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `CUEFLOW_DIGEST_CRON` | | 例如 `0 9 * * *`：每天寄一次待審摘要 |
| `CUEFLOW_DISPATCH_INTERVAL` | `3s` | 發布 worker 的輪詢間隔 |
| `CUEFLOW_DISPATCH_LEASE` | `10m` | 一次發布最長可以花多久，超過就視為當機 |
| `CUEFLOW_PUBLISH_TIMEOUT` | `5m` | 單次發布呼叫的逾時（Instagram 可能要等幾分鐘）；要比 lease 短 |
| `CUEFLOW_DISABLE_SCHEDULER` | `false` | 不啟用 cron，只跑 API 與 worker |

### Agent

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `CUEFLOW_AGENT_CONCURRENCY` | `3` | 同時執行的 agent 任務數 |
| `CUEFLOW_AGENT_ALLOW_PRIVATE_URLS` | `false` | 允許 agent 的 `read_url` 存取內網與 loopback 位址 |

### 媒體

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `CUEFLOW_MEDIA_BASE_URL` | `CUEFLOW_PUBLIC_URL` | 把存在 CueFlow 的圖片交給發布器時用的網址前綴，發布器必須連得到（compose 設為 `http://cueflow:3000`） |
| `CUEFLOW_MEDIA_STORAGE` | | 新的圖片／影片存放位置：留空 = 資料庫、`file:///data/media` 或 `gcs://bucket/prefix`。既有媒體用 `cueflow media migrate --to file:///data/media` 搬移 |
| `CUEFLOW_MEDIA_RETENTION_DAYS` | | 搭配 `CUEFLOW_TICK=external`：刪除超過這麼多天、且沒有未完成貼文使用的媒體。留空或 0 為關閉。手動：`cueflow media prune --older-than 90d [--dry-run]` |
| `CUEFLOW_MEDIA_REGISTER_ACCOUNTS` | | 允許呼叫 `POST /internal/media/register`（收編寫進 bucket `uploads/` 的檔案）的 Google 服務帳號，逗號分隔。需要 `CUEFLOW_TICK=external` 與 GCS 媒體 |

### 雲端模式

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `CUEFLOW_QUEUE` | `local` | `cloudtasks` 以 Cloud Tasks 派送背景工作（雲端模式，見 [`deploy/cloudrun`](deploy/cloudrun/README.md)） |
| `CUEFLOW_TICK` | `internal` | `external` 由 Cloud Scheduler 透過 `/internal/tick` 驅動時鐘，程序內不輪詢；需搭配 `CUEFLOW_QUEUE=cloudtasks` |
| `CUEFLOW_TASKS_QUEUE` / `CUEFLOW_TASKS_SERVICE_ACCOUNT` / `CUEFLOW_TASKS_TARGET_URL` | | Cloud Tasks 佇列路徑、簽署派送的服務帳號，以及本服務自己的網址 |
| `CUEFLOW_INTERNAL_SERVICE_ACCOUNTS` / `CUEFLOW_INTERNAL_AUDIENCE` | | 允許呼叫 `/internal/*` 的服務帳號（逗號分隔 email）與 ID token 的 audience（預設為目標網址） |
| `CUEFLOW_CF_ACCESS_TEAM_DOMAIN` / `CUEFLOW_CF_ACCESS_AUD` | | 以 Cloudflare Access（Zero Trust）登入儀表板：填 team domain 與應用程式的 AUD。兩者要一起設或都不設。帶有效 Access JWT 的請求等同 admin token，見 [`deploy/cloudrun`](deploy/cloudrun/README.md) |

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
- [部署到 Google Cloud Run](deploy/cloudrun/README.md)（英文）
- 英文文件：[REST API](docs/api.md) · [Connector 插件開發](docs/connectors.md)

## 授權

Apache-2.0
