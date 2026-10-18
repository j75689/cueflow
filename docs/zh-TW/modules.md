# 模組

CueFlow 的核心不綁任何一種工作：org、agent、task、看板、排程、核准、runner、connector 與 MCP 端點。跟特定領域有關的東西都是模組。模組帶著自己的 REST 路由、agent 能力與工具、worker、cron 和儀表板頁面，掛載時才註冊進核心。

`CUEFLOW_MODULES` 決定掛哪些模組，逗號分隔（`all`、`none`，或 `content`、`deliverables` 任意組合；預設全部）。沒掛載的模組沒有路由、工具、worker 與 cron；資料表保留，儀表板會隱藏它的頁面。`GET /api/v1/meta` 的 `modules` 列出掛載了哪些。

| 模組 | 內容 | 程式碼 |
|---|---|---|
| `deliverables` | agent 產出的型別化項目，人審核後再交給某個動作執行 | `internal/deliverable` |
| `content` | 以 deliverable 形式存在的社群貼文、資料來源 connector、發布器、圖片與影片 | `internal/content` |

自己寫一個模組：實作 `api.Module`（`internal/api/module.go`），需要的話加上選用的 hook（`MetaProvider`、`StatsProvider`、`TaskDetailer`、`MCPProvider`），在 `Attach()` 用 `crew.RegisterCapability` / `crew.RegisterTool` 註冊能力與工具，再到 `cmd/cueflow/main.go` 掛上去。

## deliverables

Deliverable 是 agent 寫進審核佇列的型別化項目（內建 `document`，其他模組可以加 kind）。狀態流程是 `DRAFT → PENDING_REVIEW → APPROVED | REJECTED`；核准的項目變成 `QUEUED`，由 worker 執行（`DONE` 或 `FAILED`，失敗的可以重試）。人核准之前什麼都不會執行。

- **執行**：kind 可以自己執行（content 模組的 `post` 會發布）；否則 worker 呼叫 org 的 `action` connector（`mcp_action`，見 [connectors.md](../connectors.md)（英文））。兩者都沒有的 org，核准就算完成：審核本身就是目的。
- **Agent**：能力 `deliverables`（工具 `create_deliverable`、`list_deliverables`、`revise_deliverable`）。
- **儀表板**：Deliverables 頁（審核、編輯、核准、退回、重試），以及 task 頁上它產出的 deliverable。
- **API 與 MCP**：`/api/v1/deliverables`（[api.md](../api.md)（英文）），以及 operator MCP 的 `list_deliverables`。
- **設定**：`CUEFLOW_DISPATCH_INTERVAL`、`CUEFLOW_DISPATCH_LEASE`（[configuration.md](configuration.md)）。

## content

建在 deliverables 之上的社群內容：貼文是 kind 為 `post` 的 deliverable，會依各平台規則檢查，核准後透過發布器 connector（任何符合[發布器約定](../publisher-mcp.md)（英文）的 MCP 伺服器）發布。請和 `deliverables` 一起掛：少了它，草稿沒有審核頁，也沒有東西會發布它們。

- **Agent**：能力 `post_deliverables`（`create_post_draft`、`list_posts`、`revise_draft`）、`imaging`（`generate_image`）與 `sources`（`search_materials`、`fetch_sources`）。
- **Connector**：資料來源 connector（RSS、IMAP）與發布器提供的帳號（`/connectors/{id}/fetch`、`/items`、`/accounts`）。
- **團隊**：[`templates/social-content.json`](../../templates/social-content.json) 是用這些能力的研究員、文案與編輯（[templates/README.md](../../templates/README.md)（英文））。

### 圖片與影片

貼文用 `/media/{id}` 引用媒體（圖片最大 10 MB；MP4 最大 100 MB）。媒體有三個來源：在審核編輯器上傳、用圖片生成 connector 產生（審核者或有 `imaging` 能力的 agent），或由 agent 附上工具的產物（例如渲染好的影片）。

#### 位元組放哪

`CUEFLOW_MEDIA_STORAGE` 決定新檔案的存放位置：

| 值 | 儲存 |
|---|---|
| 留空 | 資料庫（小型安裝夠用） |
| `file:///data/media` | 目錄，例如 Docker volume |
| `gcs://bucket/prefix` | Google Cloud Storage；`/media/{id}` 回短效的簽名網址（IAM 簽署，不需金鑰檔） |

`cueflow media migrate --to <target>` 在後端之間搬移既有媒體；網址不變，因為每一列都記錄位元組在哪個後端。

把貼文交給發布器時，CueFlow 會把 `/media/{id}` 換成 `CUEFLOW_MEDIA_BASE_URL` 底下的絕對網址，發布器必須連得到（Compose 設為 `http://cueflow:3000`）。

#### 刪除

- `DELETE /api/v1/media/{id}` 刪除該項目與檔案；只要還有尚未完成或退回的 deliverable 在用，就回 `409`。
- `cueflow media prune --older-than 90d [--dry-run]` 一次清掉所有舊的、沒人用的。
- `CUEFLOW_MEDIA_RETENTION_DAYS` 讓雲端模式每次 tick 都跑這個 prune。

#### 受信任的服務傳大檔

你自己的服務若會產生大檔（例如影片渲染器），不該把檔案推過 CueFlow。使用 GCS 儲存時，它可以用自己的 Google 身分直接寫進 bucket 的 `uploads/`，再呼叫 `POST /internal/media/register` 讓 CueFlow 收編，位元組不經過 CueFlow。該帳號必須列在 `CUEFLOW_MEDIA_REGISTER_ACCOUNTS`。細節見 [api.md](../api.md#media) 與 [deploy/cloudrun/README.md](../../deploy/cloudrun/README.md#services-that-write-media-to-the-bucket)（英文）。
