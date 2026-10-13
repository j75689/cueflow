# 圖片與影片

貼文用 `/media/{id}` 引用媒體（圖片最大 10 MB；MP4 最大 100 MB）。媒體有三個來源：在審核編輯器上傳、用圖片生成 connector 產生（審核者或有 `imaging` 能力的 agent），或由 agent 附上工具的產物（例如渲染好的影片）。

## 位元組放哪

`CUEFLOW_MEDIA_STORAGE` 決定新檔案的存放位置：

| 值 | 儲存 |
|---|---|
| 留空 | 資料庫（小型安裝夠用） |
| `file:///data/media` | 目錄，例如 Docker volume |
| `gcs://bucket/prefix` | Google Cloud Storage；`/media/{id}` 回短效的簽名網址（IAM 簽署，不需金鑰檔） |

`cueflow media migrate --to <target>` 在後端之間搬移既有媒體；網址不變，因為每一列都記錄位元組在哪個後端。

把貼文交給發布器時，CueFlow 會把 `/media/{id}` 換成 `CUEFLOW_MEDIA_BASE_URL` 底下的絕對網址，發布器必須連得到（Compose 設為 `http://cueflow:3000`）。

## 刪除

- `DELETE /api/v1/media/{id}` 刪除該項目與檔案；只要還有尚未發布或退回的貼文在用，就回 `409`。
- `cueflow media prune --older-than 90d [--dry-run]` 一次清掉所有舊的、沒人用的。
- `CUEFLOW_MEDIA_RETENTION_DAYS` 讓雲端模式每次 tick 都跑這個 prune。

## 受信任的服務傳大檔

你自己的服務若會產生大檔（例如影片渲染器），不該把檔案推過 CueFlow。使用 GCS 儲存時，它可以用自己的 Google 身分直接寫進 bucket 的 `uploads/`，再呼叫 `POST /internal/media/register` 讓 CueFlow 收編，位元組不經過 CueFlow。該帳號必須列在 `CUEFLOW_MEDIA_REGISTER_ACCOUNTS`。細節見 [api.md](../api.md#media) 與 [deploy/cloudrun/README.md](../../deploy/cloudrun/README.md#services-that-write-media-to-the-bucket)（英文）。
