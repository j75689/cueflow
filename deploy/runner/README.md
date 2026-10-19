# Runner in a container

A [runner](../../docs/external-agents.md#run-agents-on-your-own-machine-runners) that works agents' tasks with Claude Code, inside a
container. The agent sees only what you hand it here: a Claude login, one GitHub token and an empty work directory.
Your own `gh` / Claude / SSH logins on the host stay out of reach.

## Set up

1. **Claude login.** On the host run `claude setup-token` and copy the token. (Mounting `~/.claude` does not work on
   macOS: the login is in the Keychain, not in a file.)
2. **GitHub token.** Create a fine-grained personal access token for only the repositories the agent may work on:
   Contents and Pull requests read/write, Metadata read. No Workflows, no Administration. Keep it separate from any
   token you give an MCP connector.
3. **Fill in the environment.**
   ```bash
   cd deploy/runner
   cp .env.example .env && chmod 600 .env     # set CLAUDE_CODE_OAUTH_TOKEN, GH_TOKEN, GIT_EMAIL
   ```
4. **Register.** Dashboard → Runners → Add runner gives a one-time `cfreg_...` token:
   ```bash
   docker compose run --rm runner register --url https://<your CueFlow> --token cfreg_... --name docker-runner
   docker compose up -d
   docker compose run --rm runner status
   ```
   The registration is kept in the `config` volume, so restarts and updates (`docker compose pull && docker compose
   up -d`) do not need it again.
5. Under **Connectors** add a **Runner pool** (label `claude-code`) and give the agent that runtime. Stop any other
   runner that is registered for the same pool, or they will share the tasks.

## Other tools and agents

The compose file runs the published `ghcr.io/j75689/cueflow:runner` image: Claude Code, Codex, git, gh and nothing
else. To give agents more tools (Python, ffmpeg...) or another agent CLI (Gemini CLI, OpenCode, Goose), build your own
image on top of it: [docs/runner-image.md](../../docs/runner-image.md).

## Coding agents and the shell

A runner normally refuses everything an agent does on the machine except CueFlow's own tools. This one sets
`CUEFLOW_RUNNER_ALLOW_SHELL=true` (or `"allow_shell": true` in `runner.json`, or `register --allow-shell`), so the
agent may run commands and edit files, working in `/work`. Only the machine's owner can turn this on: the server cannot
ask for it, and every job this runner takes gets it, so give the pool to coding agents only.

The agent can read the container's environment, so `GH_TOKEN` and the Claude token are exposed to whatever it runs.
It also reads issues and code written by others, which may try to steer it. That is why the token is limited to the
repositories and permissions the work needs and the container has nothing else in it.

## What it limits

| | |
|---|---|
| Claude Code settings | `claude-settings.json` is mounted read-only as its managed settings: no `Co-Authored-By` on commits and no footer on pull requests. Edit it and `docker compose up -d --force-recreate` |
| Credentials | only `.env`: the Claude token and `GH_TOKEN` (`gh` and `git push` use it; `.env` is never copied into the image) |
| Files | `/work` (volume) and `/config` (volume); the root file system is read-only and the home directory is a tmpfs |
| Process | non-root, no capabilities, no new privileges, 4 GB memory, 512 processes |
| Network | outbound only (CueFlow, GitHub, Anthropic); nothing listens |

`docker compose down -v` also removes `/work` and the registration; `docker compose down` keeps both. What the agent can
do on GitHub is whatever `GH_TOKEN` allows, so keep it to the repositories and permissions the work needs and protect
the default branch (require a pull request) so nothing reaches it without your review.
