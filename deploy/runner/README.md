# Runner in a container

A [runner](../../README.md#run-agents-on-your-own-machine-runners) that works agents' tasks with Claude Code, inside a
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
   The registration is kept in the `config` volume, so restarts and rebuilds do not need it again.
5. Under **Connectors** add a **Runner pool** (label `claude-code`) and give the agent that runtime. Stop any other
   runner that is registered for the same pool, or they will share the tasks.

## What it limits

| | |
|---|---|
| Credentials | only `.env`: the Claude token and `GH_TOKEN` (`gh` and `git push` use it; `.env` is never copied into the image) |
| Files | `/work` (volume) and `/config` (volume); the root file system is read-only and the home directory is a tmpfs |
| Process | non-root, no capabilities, no new privileges, 4 GB memory, 512 processes |
| Network | outbound only (CueFlow, GitHub, Anthropic); nothing listens |

`docker compose down -v` also removes `/work` and the registration; `docker compose down` keeps both. What the agent can
do on GitHub is whatever `GH_TOKEN` allows, so keep it to the repositories and permissions the work needs and protect
the default branch (require a pull request) so nothing reaches it without your review.
