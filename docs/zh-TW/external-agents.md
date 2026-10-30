# 外部 agent、Runner 與 MCP

CueFlow 的 agent 預設跑在內建的 LLM 工具迴圈上。另外有三種安排，共通點是 agent 仍然透過 CueFlow 的工具作業，所以草稿照樣進審核、不會由 agent 發布。

## 外部 agent（ACP）

agent 可以改由 Claude Code、Codex、Gemini CLI、OpenCode 或 Goose 執行任務，透過 [Agent Client Protocol](https://agentclientprotocol.com) 溝通，取代內建的 LLM 迴圈。新增一個「External agent (ACP)」runtime connector，再把它設為 agent 的 *Runtime* 即可。外部 agent 透過每次執行專屬的 MCP 端點使用 CueFlow 的工具；shell 與檔案存取預設拒絕，除非機器的擁有者允許（要當 coding agent 用請見 [deploy/runner](../../deploy/runner/README.md)，英文）。用 Claude Code 時可以使用 Claude 訂閱方案（`claude setup-token`，把 token 貼進 connector）。

Docker 要用 `agents` 映像（[docker.md](docker.md#映像版本)）；Cloud Run 上沒有 agent CLI，請改用 Runner。

**環境變數。** 外部 agent 與 stdio MCP 伺服器（`mcp_server` connector），不論在 CueFlow 主機或 Runner 上，
繼承的都是主機的環境變數扣掉 CueFlow 自己的設定：所有 `CUEFLOW_*` 與 `DATABASE_*` 變數都不會傳入，包括金鑰、
資料庫網址、admin 與 MCP token 以及它們的 `_FILE` 路徑。其他變數照常傳入，例如 `PATH`、`HOME` 與模型供應商的憑證
（`ANTHROPIC_*`、`AWS_*`、`GOOGLE_*`）。要刻意交給 agent 或伺服器某個值，請寫在 connector 的環境變數裡；
那些不論名稱一律傳入。

這只是過濾子程序繼承的環境變數，並不是隔離。以同一個 OS 使用者執行的子程序，仍可讀取伺服器的環境變數
（Linux 上的 `/proc/<pid>/environ`）與任何 `<NAME>_FILE` 路徑，也能用主機的雲端身分（metadata server、
Application Default Credentials）自己讀取 `gcpsm://` secret。在 CueFlow 主機上執行的 ACP agent 或 stdio MCP 伺服器
與伺服器共用 OS 使用者與雲端身分，請只在可信任的情況下使用；正式環境請在 [Runner](#在自己的機器上跑-agentrunner) 上跑 agent。

## 讓其他 agent 操作 CueFlow（MCP）

CueFlow 在 `/mcp` 提供 MCP 端點，讓外部 agent（Claude Code、Codex、Cursor⋯）幫你操作看板：查看 agent 與任務、建立並指派任務、留言（也就是回覆正在等你的 agent）、移動、啟動或停止任務，以及查看待審核的草稿。它不能核准或發布貼文，這一步仍然只在後台進行。

在 `deploy/.env` 設定 `CUEFLOW_MCP_TOKEN`（`openssl rand -hex 32`），重啟後以 Claude Code 為例：

```bash
claude mcp add --transport http cueflow http://localhost:3000/mcp \
  --header "Authorization: Bearer $CUEFLOW_MCP_TOKEN"
```

工具清單見 [api.md](../api.md#mcp-external-agents)（英文）。

## 在自己的機器上跑 agent（Runner）

**Runner** 是你自己的機器，像 CI 的 self-hosted runner 一樣替 agent 執行任務。它只對外連線，在本機用*自己的*憑證執行 agent CLI（Claude Code、Codex⋯），agent 仍然透過 CueFlow 的工具作業，所以草稿照樣進審核、不會自動發布。本機的 CueFlow 或別處的 CueFlow 都能用。

1. 後台 **Runners → Add runner**，複製畫面上的指令。
2. 在那台機器上（需要 `cueflow` 執行檔與 agent CLI）：
   ```bash
   cueflow runner register --url http://localhost:3000 --token cfreg_... --name mbp
   cueflow runner run
   ```
3. 在 **Connectors** 新增 **Runner pool**（標籤 `claude-code`），再把它選為 agent 的 *Runtime*。有多個標籤時，
   *Match labels* 決定 Runner 要全部都有（`all`，預設）還是有其中一個就好（`any`）。

本機政策優先：Runner 自己的 `runner.json` 只能縮小伺服器的要求（允許的 preset、禁用網路、要執行的指令、ACP 登入方式），`cueflow runner status` 可檢查連線，在後台撤銷 Runner 會立即讓它停止。CueFlow 放在 Cloudflare Access 後面時，註冊時帶上 service token 的 header（`register --header 'CF-Access-Client-Id: ...'`），之後每個請求都會帶上，而且不會交給 agent。細節與範例見 [api.md](../api.md#runners)（英文）。

想讓 agent 碰不到你自己的登入，可以把 Runner 放進容器：它只拿到專屬的 Claude token、另給的 GitHub token 和乾淨的工作目錄，見 [deploy/runner](../../deploy/runner/README.md)（英文）。要在那個容器裡加工具或其他 agent CLI，見 [runner-image.md](runner-image.md)。

## 自主驅動的 agent

agent 也可以完全自己控制：把它的 Runtime 設為 **External (self-driven)**，在 agent 編輯頁建立 agent token，再用任何 MCP client 帶著 token 連 `/mcp`。agent 用 `claim_task` 領自己的任務，並使用同一套工具。詳見 [api.md](../api.md#runners)（英文）。
