# 設定

CueFlow 以環境變數設定。Docker 的預設值在 `deploy/.env.example`；Cloud Run 見 [deploy/cloudrun/README.md](../../deploy/cloudrun/README.md)（英文）。
會讓啟動失敗的值，也會讓 `cueflow migrate`、`rotate-key` 與 `media` 失敗；啟動時的警告只有 `cueflow serve` 會記錄。

## 從檔案與 Secret Manager 讀取機密

存放憑證的變數 `DATABASE_URL`、`DATABASE_SESSION_URL`、`CUEFLOW_SECRET_KEY`、`CUEFLOW_SECRET_KEY_PREVIOUS`、
`CUEFLOW_ADMIN_TOKEN` 與 `CUEFLOW_MCP_TOKEN` 還有兩種給法。每個 `<NAME>` 依下列順序讀取：

1. **`<NAME>` 有設且不為空。** 以 `gcpsm://` 開頭的值是 Google Secret Manager 參照：
   `gcpsm://projects/<project>/secrets/<secret>`（最新版本）、
   `gcpsm://projects/<project>/secrets/<secret>/versions/<version>`，或在 project 後加上
   `/locations/<location>` 的區域 secret（從該區域的端點讀取）。CueFlow 在啟動時用 Application Default
   Credentials（在 GCE 與 Cloud Run 上就是 metadata server 提供的 service account）同時讀取所有參照各一次，
   全部須在 10 秒內回應。該 service account 需要每個 secret 的 `roles/secretmanager.secretAccessor`。
   其他值照原樣使用。
2. **`<NAME>_FILE` 有設**（與 `postgres`、`cloudflared` image 的慣例相同，例如掛在 `/run/secrets/<NAME>`
   的 Docker 或 Kubernetes secret）：使用該檔案內容。
3. 都沒設：視為未設定。

Secret Manager 的內容與檔案都會去掉結尾的一個換行（`\n` 或 `\r\n`），因為
`echo ... | gcloud secrets create --data-file=-` 與 `echo ... > file` 都會多一個；其餘內容保留。
`<NAME>` 與 `<NAME>_FILE` 同時設定（都不為空）會讓啟動失敗並報錯。`CUEFLOW_SECRET_KEY_PREVIOUS` 會先讀出
整個值，再以逗號與換行切開，所以檔案可以一行一把金鑰。它只接受一個值：用 `gcpsm://` 時，是一個存放所有舊金鑰
（以換行或逗號分隔）的 secret；不支援以逗號列出多個 `gcpsm://` 參照。讀不到的參照（格式錯誤、secret 或版本不存在、
權限不足、API 連不上、10 秒內沒有回應、檔案不存在或無法讀取，或去掉結尾換行後為空的 secret 或檔案）會讓啟動失敗，錯誤訊息指出變數與參照，
但不會印出值；不會退回空值。其他設定會先檢查，所以別處的打字錯誤不必等 Secret Manager 就會回報。CueFlow 啟動的 agent 與 stdio MCP 伺服器不會繼承這些變數，也不會繼承其他 `CUEFLOW_*` 或 `DATABASE_*` 變數；這只是過濾它們的環境變數，並不是隔離（見 [external-agents.md](external-agents.md#外部-agentacp)）。
不會自動更新：要輪替機密，請新增版本後重新啟動。使用 `deploy/docker-compose.yml` 時，container 的
`DATABASE_URL` 來自 `CUEFLOW_DB_URL`（預設為內建的資料庫），不會取用 `.env` 裡的 `DATABASE_URL`；要在那裡改用
`DATABASE_URL_FILE`，請設 `CUEFLOW_DB_URL=`（空值）。

## 基本

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `DATABASE_URL` | `postgres://cueflow:cueflow@localhost:5432/cueflow?sslmode=disable` | PostgreSQL |
| `DATABASE_SESSION_URL` | 空 | 給 `LISTEN` 與 migration 鎖用的 session 模式連線。當 `DATABASE_URL` 是 transaction 模式的 pooler（Supabase 的 6543、PgBouncer `pool_mode=transaction`）時設定；主連線池就不使用具名 prepared statement。留空：全部走 `DATABASE_URL` |
| `CUEFLOW_SECRET_KEY` | *（必填）* | 加密 connector 機密用的 32 bytes 金鑰（base64 或 hex），請備份 |
| `CUEFLOW_SECRET_KEY_PREVIOUS` | | 換金鑰期間仍可用來解密的舊金鑰，多把用逗號或換行分隔 |
| `CUEFLOW_ADDR` | `127.0.0.1:3000` | 監聽位址 |
| `CUEFLOW_ADMIN_TOKEN` | | 設定後 API 與 UI 需要 `Authorization: Bearer <token>` |
| `CUEFLOW_MCP_TOKEN` | | 啟用給外部 agent 用的 MCP 端點 `/mcp`（Bearer token） |
| `CUEFLOW_MCP_REQUEST_TIMEOUT` | `10m` | 單一 MCP 請求（`/mcp`、`/mcp/run/…`），也就是一次工具呼叫的期限（Go duration，至少 `30s`；不是 duration 的值（`600`、`5min`）或超出範圍的值（`99999999999h`）會讓啟動失敗並報錯）。對 run 或 agent token 的工具呼叫（`/mcp/run/…`、用 agent token 的 `/mcp`），它限制兩件事：工具連接器的呼叫（例如 `mcp_tools` 伺服器的工具）若自身逾時晚於期限，會在期限前 5 秒停止，並告訴 agent 是哪個上限到了；以及（僅限 run 的呼叫，`/mcp/run/…`）等待同一 run 前一個工具呼叫結束的時間。內建工具（`claim_task`、`create_subtask` 等）不會被它中斷，會執行到底，狀態變更不會寫到一半（30 秒內寫不進去的 thread 訊息會被丟棄並記錄在 log；一次工具呼叫會有幾筆這樣的寫入，各自有 30 秒）；若卡在連接器以外的地方（例如資料庫停住），該請求會一直開著，直到它結束或被 Cloud Run 的 30 分鐘請求逾時切斷；在那之前完成的回應仍會送到 agent，而要到期限後才會開始的工具呼叫不會執行。client 斷線不會取消工具呼叫。期限只屬於單一請求，不會跨跳延續：agent A 的連接器呼叫若連到 agent B，B 再呼叫 CueFlow，B 的每個工具呼叫都是獨立的請求、各有完整時間；只有 A 的那次連接器呼叫受 A 剩餘時間限制，並以上述錯誤結束，而不是請求被切斷。這個設定不會給 task 或 run 整體期限：呼叫多次工具的 run 只逐次受限。請小於 Cloud Run 的 30 分鐘請求逾時；設 30 分鐘以上會在啟動時警告。程序內的執行仍照連接器自己的逾時 |
| `CUEFLOW_CONNECTOR_HTTP_TIMEOUT` | `90s` | 連接器用共用 HTTP client 發出的單一請求逾時（搜尋、ComfyUI、RSS、Telegram、MCP action 伺服器、`gemini_vertex`、`anthropic_vertex` 與 `vertex_image` 取得 OAuth token 等）。須為 > 0 的 Go duration；`0`、負值、不是 duration 的值（`90`、`90sec`）或超出範圍的值（`99999999999h`）會讓啟動失敗並報錯。模型呼叫與 MCP 發布器不受它限制，會跑到各自的期限為止（發布照 `CUEFLOW_PUBLISH_TIMEOUT`） |
| `CUEFLOW_PUBLIC_URL` | `http://localhost:3000` | 通知信裡的連結 |
| `CUEFLOW_DIRECT_URL` | `CUEFLOW_PUBLIC_URL` | 機器連到伺服器用的網址（Runner 註冊與 MCP 指令、媒體網址）：它們過不了 Cloudflare Access 這類瀏覽器登入 |
| `CUEFLOW_LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |
| `CUEFLOW_DB_MAX_CONNS` | 驅動預設 | 此程序主連線池的上限（小型資料庫建議每個實例約 5；走 session pooler 時每一條都算進它的 client 上限） |

## 發布與排程

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `CUEFLOW_DISPATCH_INTERVAL` | `3s` | Deliverables worker 的輪詢間隔。須為至少 `100ms` 的 Go duration，其他值會沿用預設值，並在啟動時記一筆警告 |
| `CUEFLOW_DISPATCH_LEASE` | `10m` | 對一件已核准 deliverable 的動作（發布貼文、執行 action connector）最長可以花多久，超過就視為 worker 當機。須為至少 `1s` 的 Go duration，其他值會沿用預設值，並在啟動時記一筆警告。發布逾時用預設的 `5m` 時，`1s` 到 `5m`（含）之間的 lease 都會觸發下面那筆發布逾時警告：把 lease 調到逾時之上，或把 `CUEFLOW_PUBLISH_TIMEOUT` 設得比 lease 短 |
| `CUEFLOW_PUBLISH_TIMEOUT` | `5m` | 單次發布呼叫的逾時（Instagram 可能要等幾分鐘）；要比 lease 短，否則啟動時會記一筆警告（兩個值仍照用）。須為至少 `1s` 的 Go duration，其他值會沿用預設值，並在啟動時記一筆警告。逾時設得太小時一律沿用 `5m`，即使 lease 比它短也一樣，這時也會記那筆警告 |
| `CUEFLOW_DISABLE_SCHEDULER` | `false` | 不啟用 cron，只跑 API 與 worker |

## Agent

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `CUEFLOW_AGENT_CONCURRENCY` | `3` | 同時執行的 agent 任務數 |
| `CUEFLOW_CONTEXT_HANDOFF` | `0.8` | run 的 context（最新一次 prompt 占模型上限的比例）到多滿時交接給新的 run；任務頁從這個比例起會變色顯示。須為 0 到 1 之間的數字，其他值會沿用 0.8，並在啟動時記一筆警告 |
| `CUEFLOW_MODULES` | `content,deliverables` | 要掛載的功能模組，逗號分隔（`all`、`none`，或 `content`、`deliverables` 任意組合；`content` 需要 `deliverables`）。沒掛載的模組沒有路由、agent 工具、worker 與 cron；資料表保留，儀表板會隱藏它的頁面（`/meta` 會列出掛載了哪些） |
| `CUEFLOW_AGENT_ALLOW_PRIVATE_URLS` | `false` | 允許 agent 的 `read_url` 存取內網與 loopback 位址 |

## 媒體

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `CUEFLOW_MEDIA_BASE_URL` | `CUEFLOW_PUBLIC_URL` | 把存在 CueFlow 的圖片交給發布器時用的網址前綴，發布器必須連得到（compose 設為 `http://cueflow:3000`） |
| `CUEFLOW_MEDIA_STORAGE` | | 新的圖片／影片與任務檔案存放位置：留空 = 資料庫、`file:///data/media` 或 `gcs://bucket/prefix`。既有媒體用 `cueflow media migrate --to file:///data/media` 搬移 |
| `CUEFLOW_MEDIA_RETENTION_DAYS` | | 搭配 `CUEFLOW_TICK=external`：刪除超過這麼多天、且沒有未完成 deliverable 使用的媒體。留空或 0 為關閉。手動：`cueflow media prune --older-than 90d [--dry-run]` |
| `CUEFLOW_MEDIA_REGISTER_ACCOUNTS` | | 允許呼叫 `POST /internal/media/register`（收編寫進 bucket `uploads/` 的檔案）的 Google 服務帳號，逗號分隔。需要 `CUEFLOW_TICK=external` 與 GCS 媒體 |

媒體如何儲存、提供與刪除，見 [modules.md](modules.md#圖片與影片)。

## 雲端模式

| 環境變數 | 預設值 | 用途 |
|---|---|---|
| `CUEFLOW_QUEUE` | `local` | `cloudtasks` 以 Cloud Tasks 派送背景工作（雲端模式，見 [`deploy/cloudrun`](../../deploy/cloudrun/README.md)） |
| `CUEFLOW_EVENTS` | `postgres` | 後台即時更新如何送到每個實例：`postgres`（`LISTEN/NOTIFY`，每個實例一條 session 連線）或 `pubsub`（Google Pub/Sub，不占資料庫連線；見 [`deploy/cloudrun`](../../deploy/cloudrun/README.md#live-updates-through-pubsub-optional)）。`PUBSUB_EMULATOR_HOST` 可指向模擬器 |
| `CUEFLOW_PUBSUB_TOPIC` | 空 | `CUEFLOW_EVENTS=pubsub` 用的 topic，格式 `projects/PROJECT/topics/TOPIC` |
| `CUEFLOW_PUBSUB_DELETE_TIMEOUT` | `8s` | 關機時等待刪除本實例 Pub/Sub subscription 的時間（Go duration，須 > 0；不是 duration 的值（`8`、`5sec`）或超出範圍的值（`99999999999h`）會讓啟動失敗並報錯）。請小於 Cloud Run 收到 SIGTERM 後的 10 秒寬限；設 10 秒以上會在啟動時警告。刪除逾時只會記錄 log，subscription 一天後會自行過期 |
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
