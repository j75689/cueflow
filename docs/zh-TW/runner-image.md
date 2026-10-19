# 自訂 Runner 映像

[deploy/runner](../../deploy/runner/README.md)（英文）用發佈好的 `ghcr.io/j75689/cueflow:runner` 映像跑
[Runner](external-agents.md#在自己的機器上跑-agentrunner)。映像以 Debian 為底，內含：

- CueFlow；
- Node.js；
- Claude Code 與 Codex 的 ACP 橋接器；
- git、gh 和 SSH client。

agent 需要更多工具（其他 agent CLI、Python、ffmpeg⋯）時，就以它為底做自己的映像。不需要 clone 這個 repo。

## 映像與標籤

所有映像都在同一個套件 `ghcr.io/j75689/cueflow` 裡。`runner` 和 `agents` 提供 `linux/amd64` 與 `linux/arm64`，`slim` 只有 amd64。

| 映像 | 最新版本 | 指定版本 |
|---|---|---|
| `runner`：agents 加上 git、gh、SSH，以 `cueflow runner run` 啟動 | `runner` | `<版本>-runner`，例如 `1.0.0-runner` |
| `agents`：CueFlow 加上 Node.js 與 Claude Code／Codex 的 ACP 橋接器 | `agents` | `<版本>-agents` |
| `slim`：只有 CueFlow（distroless） | `latest` | `<版本>` |

映像只在[發佈版本](https://github.com/j75689/cueflow/releases)時 build，不是 `main` 上的每個 commit 都有；`latest`、`agents` 和 `runner` 也只在發佈新版本時移動。所以 `FROM ...:runner` 跟著最新版本走；想自己決定什麼時候升級，就釘 `<版本>-runner`。

## 只要一兩個套件

只需要幾個 Debian 套件的話，用 `RUNNER_EXTRA_PACKAGES` build 參數。這個做法需要原始碼：先把 `deploy/runner/docker-compose.yml` 裡的 `build:` 區塊取消註解，並把 `image:` 改成本機用的名稱，例如 `cueflow-runner:local`（不改的話，`docker compose pull` 會用發佈的映像蓋掉你 build 的版本），再執行：

```bash
RUNNER_EXTRA_PACKAGES="python3 ffmpeg" docker compose up -d --build
```

沒有 clone 的話，也可以直接從 repo build，再在 `.env` 設 `CUEFLOW_RUNNER_IMAGE=my-runner`：

```bash
docker build --target runner --build-arg RUNNER_EXTRA_PACKAGES="python3 ffmpeg" -t my-runner \
  https://github.com/j75689/cueflow.git#main
```

## 自己的 Dockerfile

把 Dockerfile 放在 `deploy/runner/docker-compose.yml` 旁邊。用 root 安裝，最後切回 `nonroot`：

```dockerfile
FROM ghcr.io/j75689/cueflow:runner
USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends python3 python3-venv ffmpeg \
 && rm -rf /var/lib/apt/lists/*
RUN npm install -g --omit=dev @google/gemini-cli@0.62.0 opencode-ai \
 && npm cache clean --force
USER nonroot:nonroot
```

接著讓 compose 改用它，有兩種做法：

- 把 `image:` 換成 `build: .`；
- 自己 build 並加上標籤，再在 `.env` 設定 `CUEFLOW_RUNNER_IMAGE`：

```bash
docker build -t my-runner . && echo CUEFLOW_RUNNER_IMAGE=my-runner >> .env
docker compose up -d
```

只存在你機器上的映像，`docker compose pull` 更新不到。要換上新版的底層映像，請用 `docker build --pull -t my-runner .` 重新 build，再執行一次 `docker compose up -d`。

agent 需要的東西要在 build 時裝好。執行時根檔案系統是唯讀的，家目錄是 tmpfs，所以 agent 在執行時用 `npm install -g` 或 `apt-get` 安裝都會失敗，寫在 `~` 下的東西重啟後也會消失。

除非你就是要改 Runner 的啟動方式，否則不要動 `ENTRYPOINT`、`CMD`、`WORKDIR` 和 `HEALTHCHECK`。

## 其他 ACP agent

每個 Runner 任務都會指定一個 *preset*，也就是 Runner pool connector 的 `preset` 欄位，可以是 `claude-code`、`codex`、`gemini`、`opencode`、`goose` 或 `custom`。Runner 會執行該 preset 的指令，所以映像裡要有那個 agent CLI，而且指令要找得到它：

| Preset | 預設指令 | 映像裡要做的事 |
|---|---|---|
| `claude-code`、`codex` | 映像內建的橋接器 | 不用做什麼 |
| `gemini` | `npx -y @google/gemini-cli@0.62.0 --acp`（每次重啟都要重新下載） | 安裝 `@google/gemini-cli`，並把指令對應到 `gemini --acp` |
| `opencode` | `opencode acp` | 安裝 `opencode-ai`；預設指令會在 `PATH` 上找到它 |
| `goose` | `goose acp` | 從 Goose 的 release 下載 `goose` 執行檔，放進 `/usr/local/bin` |
| `custom` | 無 | 對應一個指令（任何 ACP server 都可以） |

對應寫在 `config` volume 裡的 `runner.json`，跟 `register` 存下的憑證放在一起：

```json
{
  "commands": { "gemini": "gemini --acp", "custom": "/usr/local/bin/my-acp-server --stdio" },
  "auth_methods": { "custom": "oauth-personal" },
  "models": { "gemini": "gemini-2.5-pro" },
  "allow_presets": ["claude-code", "gemini"]
}
```

- `commands`：取代 preset 的啟動指令。
- `auth_methods`：指定 ACP `authenticate` 的方法，給需要登入的 agent 用。
- `models`：釘住模型，優先於伺服器要求的模型。
- `allow_presets`：拒絕其他 agent 的任務。

映像裡沒有編輯器，檔案屬於 `nonroot`、權限 0600。可以用映像自帶的 Node.js 修改：

```bash
docker compose run --rm --entrypoint node runner -e '
  const fs = require("fs"), f = process.env.CUEFLOW_RUNNER_CONFIG, c = JSON.parse(fs.readFileSync(f));
  c.commands = { ...c.commands, gemini: "gemini --acp" };
  fs.writeFileSync(f, JSON.stringify(c, null, 2), { mode: 0o600 });'
docker compose up -d --force-recreate
```

憑證跟 Claude token 一樣放在 `.env`：

- Gemini CLI：`GEMINI_API_KEY`。
- OpenCode：對應 provider 的金鑰，例如 `ANTHROPIC_API_KEY` 或 `OPENAI_API_KEY`。
- Goose：對應 provider 的金鑰，加上 `GOOSE_PROVIDER` 和 `GOOSE_MODEL`。

每個 agent 程序都會繼承容器的環境變數，再加上 `runner.json` 裡的 `env`。最後，在 pool 的 connector 設定對應的 `preset` 與標籤。

## 憑證與工作目錄

沿用 [deploy/runner](../../deploy/runner/README.md#what-it-limits)（英文）的安排：映像只放工具，不放憑證；每次執行只拿到你交給它的東西。

| 路徑 | 掛載方式 | 原因 |
|---|---|---|
| `/config` | named volume，可讀寫 | `runner.json`：`register` 存下的 Runner token |
| `/work` | named volume，可讀寫 | agent clone 和修改的東西；`docker compose down -v` 會清掉 |
| `/etc/claude-code/managed-settings.json` | 檔案，**唯讀** | agent 不能改的 Claude Code 設定 |
| `/home/nonroot`、`/tmp` | tmpfs | agent 自己的狀態，重啟後消失 |
| 其他所有路徑 | 唯讀的根檔案系統 | agent 改不了自己的工具 |

秘密一律來自 `.env`（`env_file`），不要在 Dockerfile 裡用 `COPY` 或 `ARG` 放進去：映像裡的東西，任何拉得到映像的人都讀得到。

其他 agent 必須遵守的檔案也要唯讀掛載，例如某個 agent 的 managed policy，或 SSH 的 `known_hosts`。

不要掛載你的家目錄、`~/.ssh`、`~/.config/gh` 或 Docker socket：這些會把你自己的登入、甚至整台主機交給 agent。

## 不會變的部分

以 `ghcr.io/j75689/cueflow:runner` 為底的映像可以依賴下面這些。任何一項改變都算 breaking change：

- `/usr/local/bin/cueflow` 是 CueFlow 執行檔，映像以 `cueflow runner run` 啟動（`ENTRYPOINT ["/usr/local/bin/cueflow", "runner"]`、`CMD ["run"]`）。
- Runner 從 `/config/runner.json` 讀設定（`CUEFLOW_RUNNER_CONFIG`）。
- `/work` 是工作目錄；允許 shell 時，agent 也在這裡作業。
- 映像以使用者 `nonroot` 執行，uid 與 gid 都是 65532，`HOME=/home/nonroot`。
- 有 `apt-get` 和 `npm`，衍生映像可以用 root 安裝套件。

下面這些在任何版本都可能改變：

- Debian 版本與 Node.js 主版本；
- 內建 ACP 橋接器的版本；
- 預裝套件的確切清單（git、gh、SSH client 之外）；
- 各 preset 的預設指令。

你的映像如果依賴其中任何一項，請釘 `<版本>-runner` 標籤。
