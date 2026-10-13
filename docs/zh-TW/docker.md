# 用 Docker Compose 執行

`deploy/docker-compose.yml` 會啟動 CueFlow、它的 PostgreSQL、Postiz（含自己的資料庫與 Redis）和 SearXNG。所有 port 都只綁 `127.0.0.1`。

```bash
git clone https://github.com/j75689/cueflow && cd cueflow/deploy
cp .env.example .env
# 填入 CUEFLOW_SECRET_KEY（openssl rand -base64 32）與 POSTIZ_JWT_SECRET（openssl rand -hex 32）
docker compose up -d
```

- CueFlow：<http://localhost:3000>
- Postiz：<http://localhost:4007>

## 初次設定

1. **Postiz**：建立帳號，連結你的 Threads / Instagram / X 帳號，並到 *Settings → Developers* 複製 API key。
2. **CueFlow → Connectors**：
   - *Postiz (MCP)*：endpoint 填 `http://postiz:5000/api/mcp`，貼上 API key，按 **Test**。
     或 *MCP publisher*：endpoint 填符合[發布器約定](../publisher-mcp.md)（英文）的伺服器網址，加上 Bearer token 或機密標頭（例如 Cloudflare Access service token）；之後在審核頁為每篇貼文勾選要發的 Threads/Instagram 帳號。
   - 一個 LLM：例如 *Anthropic Claude*（model `claude-opus-5-5`），或指向 Ollama 的 *OpenAI-compatible*。
   - 一個以上的資料來源，例如 RSS feed。
   - 選用：SearXNG / Tavily 用於網路搜尋，SMTP 用於審核提醒。
3. 二選一：
   - **Command**：直接告訴 agent 團隊要做什麼。
   - **Schedules**：定時執行。*Agent 任務*排程每次開一張任務給 agent（它會搜尋、讀原文、自己判斷）；*簡單排程*是固定流程：收集素材 → 一次 LLM 呼叫 → 產生草稿（cron 例如 `0 8 * * *`、主題、平台、instructions，見 [prompt-templates.md](prompt-templates.md)）。按 **Run now** 試跑。
4. **Review**：修改草稿，核准或退回。核准的貼文幾秒內就會交給 Postiz；失敗時會顯示原因並提供 **Retry** 按鈕。

## 映像版本

| `CUEFLOW_IMAGE_TARGET` | 內容 |
|---|---|
| `slim`（預設） | 只有 CueFlow（distroless），內建 agent 與 LLM connector |
| `agents` | 加上 Node.js 與 Claude Code、Codex 的 ACP 橋接器，可使用外部 agent |

在 `deploy/.env` 設定後執行 `docker compose up -d --build`。另有 `runner` 目標（agents 加上 git 與 gh，以 `cueflow runner run` 啟動），給 [deploy/runner](../../deploy/runner/README.md)（英文）用。

## 網頁搜尋

compose 內含 SearXNG（請在 `deploy/.env` 設定 `SEARXNG_SECRET`）。新增 *SearXNG* connector，URL 填 `http://searxng:8080`；沒有指定搜尋 connector 的 agent 會使用第一個啟用中的。

## 不接 Postiz 先試用

```bash
cd deploy
docker compose --profile mock up -d cueflow cueflow-db mock-postiz
```

建立一個 *Postiz (MCP)* connector，endpoint 填 `http://mock-postiz:4007/api/mcp`，API key 填 `dev-key`。收到的貼文會列在 <http://localhost:4008/posts>。

## 遠端存取

Port 刻意只綁 localhost。想用手機審核的話，請用 SSH tunnel（`ssh -L 3000:localhost:3000 nas`）或 Tailscale 等 VPN，並設定 `CUEFLOW_ADMIN_TOKEN`（[configuration.md](configuration.md)）。

## 資料

資料庫放在 Docker volume 裡，`docker compose down -v` 會把它們刪掉。connector 憑證用 `CUEFLOW_SECRET_KEY` 加密，請在機器之外另存一份，否則還原的資料庫會解不開（[更換金鑰](configuration.md#更換加密金鑰)）。
