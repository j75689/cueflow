# 設定

CueFlow 以環境變數設定。Docker 的預設值在 `deploy/.env.example`；Cloud Run 見 [deploy/cloudrun/README.md](../../deploy/cloudrun/README.md)（英文）。

## 基本

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

## 發布與排程

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `CUEFLOW_DISPATCH_INTERVAL` | `3s` | Deliverables worker 的輪詢間隔 |
| `CUEFLOW_DISPATCH_LEASE` | `10m` | 對一件已核准 deliverable 的動作（發布貼文、執行 action connector）最長可以花多久，超過就視為 worker 當機 |
| `CUEFLOW_PUBLISH_TIMEOUT` | `5m` | 單次發布呼叫的逾時（Instagram 可能要等幾分鐘）；要比 lease 短 |
| `CUEFLOW_DISABLE_SCHEDULER` | `false` | 不啟用 cron，只跑 API 與 worker |

## Agent

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `CUEFLOW_AGENT_CONCURRENCY` | `3` | 同時執行的 agent 任務數 |
| `CUEFLOW_MODULES` | `content,deliverables` | 要掛載的功能模組，逗號分隔（`all`、`none`，或 `content`、`deliverables` 任意組合）。沒掛載的模組沒有路由、agent 工具、worker 與 cron；資料表保留，儀表板會隱藏它的頁面（`/meta` 會列出掛載了哪些） |
| `CUEFLOW_AGENT_ALLOW_PRIVATE_URLS` | `false` | 允許 agent 的 `read_url` 存取內網與 loopback 位址 |

## 媒體

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `CUEFLOW_MEDIA_BASE_URL` | `CUEFLOW_PUBLIC_URL` | 把存在 CueFlow 的圖片交給發布器時用的網址前綴，發布器必須連得到（compose 設為 `http://cueflow:3000`） |
| `CUEFLOW_MEDIA_STORAGE` | | 新的圖片／影片存放位置：留空 = 資料庫、`file:///data/media` 或 `gcs://bucket/prefix`。既有媒體用 `cueflow media migrate --to file:///data/media` 搬移 |
| `CUEFLOW_MEDIA_RETENTION_DAYS` | | 搭配 `CUEFLOW_TICK=external`：刪除超過這麼多天、且沒有未完成 deliverable 使用的媒體。留空或 0 為關閉。手動：`cueflow media prune --older-than 90d [--dry-run]` |
| `CUEFLOW_MEDIA_REGISTER_ACCOUNTS` | | 允許呼叫 `POST /internal/media/register`（收編寫進 bucket `uploads/` 的檔案）的 Google 服務帳號，逗號分隔。需要 `CUEFLOW_TICK=external` 與 GCS 媒體 |

媒體如何儲存、提供與刪除，見 [modules.md](modules.md#圖片與影片)。

## 雲端模式

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `CUEFLOW_QUEUE` | `local` | `cloudtasks` 以 Cloud Tasks 派送背景工作（雲端模式，見 [`deploy/cloudrun`](../../deploy/cloudrun/README.md)） |
| `CUEFLOW_TICK` | `internal` | `external` 由 Cloud Scheduler 透過 `/internal/tick` 驅動時鐘，程序內不輪詢；需搭配 `CUEFLOW_QUEUE=cloudtasks` |
| `CUEFLOW_TASKS_QUEUE` / `CUEFLOW_TASKS_SERVICE_ACCOUNT` / `CUEFLOW_TASKS_TARGET_URL` | | Cloud Tasks 佇列路徑、簽署派送的服務帳號，以及本服務自己的網址 |
| `CUEFLOW_INTERNAL_SERVICE_ACCOUNTS` / `CUEFLOW_INTERNAL_AUDIENCE` | | 允許呼叫 `/internal/*` 的服務帳號（逗號分隔 email）與 ID token 的 audience（預設為目標網址） |
| `CUEFLOW_CF_ACCESS_TEAM_DOMAIN` / `CUEFLOW_CF_ACCESS_AUD` | | 以 Cloudflare Access（Zero Trust）登入儀表板：填 team domain 與應用程式的 AUD。兩者要一起設或都不設。帶有效 Access JWT 的請求等同 admin token，見 [`deploy/cloudrun`](../../deploy/cloudrun/README.md) |

## 更換加密金鑰

connector 的憑證都用 `CUEFLOW_SECRET_KEY` 加密；金鑰弄丟就得重新輸入所有憑證，請另外存一份到密碼管理器。要更換金鑰，又不想停機或重填憑證：

1. 產生新金鑰（`cueflow genkey`），設為 `CUEFLOW_SECRET_KEY`，把舊金鑰移到 `CUEFLOW_SECRET_KEY_PREVIOUS`，重啟。之後新資料用新金鑰加密，舊資料也還讀得到。
2. 全部重新加密：`docker compose exec cueflow cueflow rotate-key`（可以先加 `--dry-run` 試跑）。只要有任何一個 connector 解不開，就不會修改任何資料。
3. 拿掉 `CUEFLOW_SECRET_KEY_PREVIOUS`，再重啟一次。

注意：`docker compose down -v` 會刪除 volume，也就是 CueFlow 的資料庫。
