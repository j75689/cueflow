# CueFlow

**開源、可自託管的 Agent 運營控制台。** 把任務交給看板上的一組 AI agent：它們用各自的工具完成任務，需要你決定時會來問，有風險的動作會等你核准。Agent 可以用內建的 LLM 迴圈，或把工作交給你自己機器上的外部 agent（Claude Code、Codex、Gemini CLI⋯）；任何 [MCP](https://modelcontextprotocol.io) 伺服器都能當它的工具，每個工具可設為允許、需核准或禁止；任務可以定時重複。可以在自己的機器上跑（家裡的 NAS、Home Lab 或筆電上的 Docker，不需要 Public IP、網域或 Webhook），也能部署到 Google Cloud Run，沒工作時縮到零。

> English: [README.md](README.md) · 全部文件：[docs/zh-TW/](docs/zh-TW/)、[docs/](docs/)（英文）

```
 你 · 排程 · MCP client ─▶ 任務 ─▶ Agent ─▶ 工具（內建 · MCP 伺服器 · Runner 上自己的 CLI）
                                     │
                     需要你決定 / 有風險的工具呼叫 ─▶ 你在對話串裡核准 ─▶ 它繼續
```

Agent 產出的每樣東西都是一份 **deliverable**，等人審核：可以是一份文件，或是內建內容模組裡的社群貼文：agent 從收集到的素材（RSS、信箱、網路搜尋）寫草稿，核准後才交給發布器，也就是任何符合四個工具的[發布器約定](docs/publisher-mcp.md)（英文）的 MCP 伺服器。Agent 永遠不會對自己的產出動手：沒有任何 agent 工具能發文，發布器與 action connector 也只接受 worker 針對「那一份已核准的項目」給的授權。

## 運作方式

- **Command（指令中心）**：直接說你要什麼，例如「每天早上 8 點整理 AI 新聞並寫成 Threads 草稿」。管理型 agent（🧭 Chief，總管）會規劃步驟、分派子任務、設定排程，最後回報結果。
- **Board（看板）**：每個需求都是一張任務卡，依序經過 Backlog → 待辦 → 進行中 → 等待中 → 待確認 → 完成。每個任務都有一條對話串，記錄 agent 的進度、工具呼叫、提問與成果；回覆就能讓它繼續，用 `@writer` 開頭可以把任務轉給另一個 agent。
- **Agents（角色）**：每個角色有自己的人設、能力、LLM、資料來源與記憶。全新安裝只有 Chief；自己新增文案、研究員、編輯⋯每個角色都能修改。回覆與寫稿的語言由各自的 `language` 設定決定（預設 `en`，要用繁體中文請改成 `zh-TW`）。
- **Schedules（排程）**：定時為 agent 建立任務；agent 也能自己建立排程。
- **外部 agent 與 Runner**：agent 可以改由 Claude Code、Codex、Gemini CLI、OpenCode 或 Goose 透過 ACP 執行任務，跑在伺服器上或你自己的機器上；其他 agent 也能透過 CueFlow 自己的 MCP 端點操作看板。見 [docs/zh-TW/external-agents.md](docs/zh-TW/external-agents.md)。

能力與工具、任務狀態機、委派與防護：[docs/zh-TW/agentic-flow.md](docs/zh-TW/agentic-flow.md)。

## 特色

- **從架構上保證人工審核。** Deliverable 遵循嚴格的狀態機（`DRAFT → PENDING_REVIEW → APPROVED | REJECTED → QUEUED → DONE | FAILED`），每次變動都寫入稽核紀錄，包含審核者修改前後的差異。
- **一切都是 connector 插件。** 發布器、資料來源、搜尋引擎、LLM、通知、圖片生成都實作簡單的 Go 介面，並附設定 schema 自行註冊，UI 會自動產生設定表單。
- **預設私有。** 在自己機器上只做主動連線，伺服器只綁 `127.0.0.1:3000`；在雲端，儀表板放在 Cloudflare Access 後面，機器之間的呼叫都帶 Google 簽發的身分。connector 機密以 AES-256-GCM 加密儲存。
- **動作不怕當機。** worker 使用租約；若在執行途中掛掉，項目會標成 `FAILED` 並提醒「重試前請先確認」，而不是默默做第二次。
- **單一 binary，哪裡都能跑。** Go 的 API、排程與 worker，加上內嵌的 Next.js 控制台。自己的硬體用 Docker Compose；或 Cloud Run 搭配 Cloud Tasks 與 Cloud Scheduler，沒工作時不佔資源。

## 快速開始（Docker）

```bash
git clone https://github.com/j75689/cueflow && cd cueflow/deploy
cp .env.example .env
# 填入 CUEFLOW_SECRET_KEY（openssl rand -base64 32）與 SEARXNG_SECRET（openssl rand -hex 32）
docker compose up -d
```

CueFlow 在 <http://localhost:3000>。接著：

1. **Connectors**：一個 LLM（例如 *Anthropic Claude*，或指向 Ollama 的 *OpenAI-compatible*）；一個資料來源，例如 RSS feed；一個發布器：*MCP publisher* 指向符合[發布器約定](docs/publisher-mcp.md)（英文）的伺服器，或用內附的 mock（`docker compose --profile mock up -d`，endpoint `http://mock-publisher:4010/mcp`、token `dev-token`）先跑通流程、不會真的發出去。
2. **Command**：直接告訴 agent 團隊要做什麼，或建立 **Schedule** 定時執行。
3. **Deliverables**：修改、勾選帳號、核准或退回。核准的貼文幾秒內就會交給發布器。

映像版本、網頁搜尋、mock 發布器、遠端存取：[docs/zh-TW/docker.md](docs/zh-TW/docker.md)。

## Connector

發布器（任何符合[發布器約定](docs/publisher-mcp.md)（英文）的 MCP 伺服器）、資料來源（RSS、IMAP）、搜尋（SearXNG、Tavily）、LLM（OpenAI 相容服務、API / Bedrock / Vertex AI 上的 Claude、Vertex AI 上的 Gemini）、SMTP 通知、圖片生成（OpenAI、Gemini、Vertex、ComfyUI），以及任何 MCP 伺服器當 agent 工具。完整清單與認證方式：[docs/zh-TW/builtin-connectors.md](docs/zh-TW/builtin-connectors.md)。自己寫一個：[docs/connectors.md](docs/connectors.md)（英文）。

## 更進一步

- **設定**：所有環境變數，以及如何更換加密金鑰：[docs/zh-TW/configuration.md](docs/zh-TW/configuration.md)。請備份 `CUEFLOW_SECRET_KEY`，沒有它存好的憑證就解不開。
- **Runner、外部 agent、MCP**：[docs/zh-TW/external-agents.md](docs/zh-TW/external-agents.md)。
- **模組**：`deliverables` 與 `content` 各加了什麼、用 `CUEFLOW_MODULES` 開關，以及圖片與影片存哪：[docs/zh-TW/modules.md](docs/zh-TW/modules.md)。
- **Google Cloud Run**：Supabase、Cloud Storage、Cloud Tasks 與 Cloud Scheduler，縮到零：[deploy/cloudrun/README.md](deploy/cloudrun/README.md)（英文）。

## 文件

- [整體架構設計](docs/zh-TW/architecture.md)，套件結構在 §2.1
- [Agentic flow 設計](docs/zh-TW/agentic-flow.md)
- [設定](docs/zh-TW/configuration.md)
- [用 Docker Compose 執行](docs/zh-TW/docker.md)
- [內建 connector](docs/zh-TW/builtin-connectors.md)
- [外部 agent、Runner 與 MCP](docs/zh-TW/external-agents.md)
- [模組：deliverables、content、圖片與影片](docs/zh-TW/modules.md)
- 英文文件：[REST API](docs/api.md) · [Connector 插件開發](docs/connectors.md) · [發布器 MCP 約定](docs/publisher-mcp.md) · [部署到 Google Cloud Run](deploy/cloudrun/README.md) · [容器裡的 Runner](deploy/runner/README.md)

## 開發與參與貢獻

需要 Go 1.27+、Node 22+、Docker（跑 PostgreSQL 用）；`make test-db`、`go run ./cmd/cueflow`、`cd web && npm run dev`。完整的開發設定、不可妥協的規則與 commit 慣例在 [CONTRIBUTING.md](CONTRIBUTING.md)（英文）。安全性問題請走 [SECURITY.md](SECURITY.md) 的私下通報，不要開公開 issue。

## 授權

Apache-2.0
