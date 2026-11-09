# 用 Docker Compose 執行

`deploy/docker-compose.yml` 會啟動 CueFlow、它的 PostgreSQL 和 SearXNG。所有 port 都只綁 `127.0.0.1`。發布透過你自己選的 MCP 伺服器（或下面內附的 mock）。

```bash
git clone https://github.com/j75689/cueflow && cd cueflow/deploy
cp .env.example .env
# 填入 CUEFLOW_SECRET_KEY（openssl rand -base64 32）與 SEARXNG_SECRET（openssl rand -hex 32）
docker compose up -d
```

- CueFlow：<http://localhost:3000>
- SearXNG：<http://localhost:8888>

## 初次設定

1. **CueFlow → Connectors**：
   - *MCP publisher*：endpoint 填符合[發布器約定](../publisher-mcp.md)（英文）的伺服器網址，加上 Bearer token 或機密標頭（例如 Cloudflare Access service token），按 **Test**；之後在審核頁為每篇貼文勾選要發的帳號。想先跑通流程、不真的發出去，用下面的 [mock 發布器](#mock-發布器)。
   - 一個 LLM：例如 *Anthropic Claude*（model `claude-opus-5-5`），或指向 Ollama 的 *OpenAI-compatible*。
   - 一個以上的資料來源，例如 RSS feed。
   - 選用：SearXNG / Tavily 用於網路搜尋，SMTP 用於審核提醒。
2. 二選一：
   - **Command**：直接告訴 agent 團隊要做什麼。
   - **Schedules**：定時執行。每次到點就開一張任務給 agent（它會搜尋、讀原文、自己判斷）。按 **Run now** 試跑。
3. **Deliverables**：修改草稿、勾選帳號，核准或退回。核准的貼文幾秒內就會交給發布器；失敗時會顯示原因並提供 **Retry** 按鈕。

## 映像版本

| `CUEFLOW_IMAGE_TARGET` | 內容 |
|---|---|
| `slim`（預設） | 只有 CueFlow（distroless），內建 agent 與 LLM connector |
| `agents` | 加上 Node.js 與 Claude Code、Codex 的 ACP 橋接器，可使用外部 agent |

在 `deploy/.env` 設定後執行 `docker compose up -d --build`。另有 `runner` 目標（agents 加上 git 與 gh，以 `cueflow runner run` 啟動），給 [deploy/runner](../../deploy/runner/README.md)（英文）用。

每個發佈版本也會把三種映像推到 ghcr：`ghcr.io/j75689/cueflow:<版本>`／`:latest`（slim，只有 amd64）、`:<版本>-agents`／`:agents`、`:<版本>-runner`／`:runner`（這兩個也有 arm64）。標籤說明，以及怎麼做一個裝了自己工具的 runner，見 [runner-image.md](runner-image.md)。

## 不用 PostgreSQL（SQLite）

在小型機器上，CueFlow 可以把所有資料放在一個 SQLite 檔案，而不是 PostgreSQL，但只限 single 模式（見 [configuration.md](configuration.md#基本) 的 `DATABASE_URL`）。為它準備一個 compose 檔，例如 `deploy/docker-compose.sqlite.yml`；若有用到 `docker-compose.yml` 裡 `cueflow` 服務的其他變數，請自行加上。它和 `docker-compose.yml` 一樣從這份 checkout 建置映像：v1.7.0 以前發佈的映像沒有 SQLite 後端。

```yaml
name: cueflow
services:
  cueflow:
    build:
      context: ..
      dockerfile: Dockerfile
      target: ${CUEFLOW_IMAGE_TARGET:-slim}
    image: cueflow:${CUEFLOW_IMAGE_TARGET:-slim}
    restart: unless-stopped
    environment:
      DATABASE_URL: sqlite:///data/cueflow.db
      CUEFLOW_SECRET_KEY: ${CUEFLOW_SECRET_KEY:?set CUEFLOW_SECRET_KEY in .env}
      CUEFLOW_PUBLIC_URL: ${CUEFLOW_PUBLIC_URL:-http://localhost:3000}
      TZ: ${TZ:-UTC}
    volumes:
      - ./data:/data
    ports:
      - "127.0.0.1:3000:3000"
```

```bash
cd deploy
mkdir -p data && sudo chown 65532:65532 data   # 映像以 nonroot 使用者（uid 65532）執行
docker compose -f docker-compose.sqlite.yml up -d --build
```

CueFlow 啟動時會建立並 migrate `deploy/data/cueflow.db`；SQLite 會在旁邊放 `cueflow.db-wal` 與 `cueflow.db-shm`，所以備份要整個目錄一起，並在 CueFlow 停止時進行。除非設了 `CUEFLOW_MEDIA_STORAGE`，媒體也存在這個檔案裡。一個檔案只跑一個 CueFlow：不要在同一個檔案上再開第二個實例或副本。要把現有的 PostgreSQL 安裝搬到 SQLite，見[搬移到 SQLite](configuration.md#搬移到-sqlite)。

## 網頁搜尋

compose 內含 SearXNG（請在 `deploy/.env` 設定 `SEARXNG_SECRET`）。新增 *SearXNG* connector，URL 填 `http://searxng:8080`；沒有指定搜尋 connector 的 agent 會使用第一個啟用中的。

## Mock 發布器

```bash
cd deploy
docker compose --profile mock up -d cueflow cueflow-db mock-publisher
```

`mock-publisher` 符合發布器約定，但不會發到任何地方。建立一個 *MCP publisher* connector，endpoint 填 `http://mock-publisher:4010/mcp`，Bearer token 填 `dev-token`；它提供一個 Threads、一個 Instagram、一個 YouTube 帳號，加上一個標記為 test 的帳號。收到的貼文會列在 <http://localhost:4010/posts>。不用 Docker 的話，`make mock` 會在 `:4010` 跑同一個伺服器。

## 遠端存取

Port 刻意只綁 localhost。想用手機審核的話，請用 SSH tunnel（`ssh -L 3000:localhost:3000 nas`）或 Tailscale 等 VPN，並設定 `CUEFLOW_ADMIN_TOKEN`（[configuration.md](configuration.md)）。

## 資料

資料庫放在 Docker volume 裡，`docker compose down -v` 會把它們刪掉（SQLite 檔案放在 `deploy/data`，不受影響）。connector 憑證用 `CUEFLOW_SECRET_KEY` 加密，請在機器之外另存一份，否則還原的資料庫會解不開（[更換金鑰](configuration.md#更換加密金鑰)）。
