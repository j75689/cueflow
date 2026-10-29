# External agents, runners and MCP

An agent in CueFlow normally runs on the built-in LLM tool loop. Three other arrangements exist, and in all of
them the agent keeps using CueFlow's tools, so drafts still go to Review and nothing is published by an agent.

## External agents (ACP)

An agent can run its tasks in Claude Code, Codex, Gemini CLI, OpenCode or Goose instead of the built-in loop,
over the [Agent Client Protocol](https://agentclientprotocol.com). Add an *External agent (ACP)* runtime
connector and pick it as the agent's *Runtime*. The external agent gets CueFlow's tools through a per-run MCP
server; shell and file access are refused unless the owner of the machine allows them (for a coding agent, see
[deploy/runner](../deploy/runner/README.md)). With Claude Code you can use a Claude subscription
(`claude setup-token`, paste the token into the connector).

On Docker this needs the `agents` image ([docker.md](docker.md#image-variants)); on Cloud Run there is no agent
CLI, so use a runner instead.

**Environment.** An external agent, and a stdio MCP server (`mcp_server` connector), on the CueFlow host or on a
runner, gets the host's environment without CueFlow's own settings: every `CUEFLOW_*` and `DATABASE_*`
variable is left out, which covers the secret key, the database URLs, the admin and MCP tokens and their
`_FILE` paths. Everything else is passed, such as `PATH`, `HOME` and provider credentials (`ANTHROPIC_*`,
`AWS_*`, `GOOGLE_*`). To give the agent or server something on purpose, put it in the connector's
environment variables; those are always passed, whatever their names.

## Control CueFlow from other agents (MCP)

CueFlow serves an MCP endpoint at `/mcp` so an external agent (Claude Code, Codex, Cursor...) can run
the board for you: list agents and tasks, create and assign tasks, comment (answering an agent that is
waiting on you), move, start or stop tasks, and list deliverables. It cannot approve or act on anything;
that stays in the dashboard.

Set `CUEFLOW_MCP_TOKEN` in `deploy/.env` (`openssl rand -hex 32`), restart, then for example:

```bash
claude mcp add --transport http cueflow http://localhost:3000/mcp \
  --header "Authorization: Bearer $CUEFLOW_MCP_TOKEN"
```

The tools are listed in [api.md](api.md#mcp-external-agents).

## Run agents on your own machine (Runners)

A **Runner** is a machine you own that works agents' tasks, like a CI self-hosted runner. It only makes
outbound connections, runs the agent CLI (Claude Code, Codex...) locally with *its own* credentials, and the
agent still uses CueFlow's tools, so drafts go to review and nothing is published. It works against a local
CueFlow or one running elsewhere.

1. Dashboard → **Runners → Add runner**; copy the command it shows.
2. On the machine (it needs the `cueflow` binary and the agent CLI):
   ```bash
   cueflow runner register --url http://localhost:3000 --token cfreg_... --name mbp
   cueflow runner run
   ```
3. Under **Connectors**, add a **Runner pool** (label `claude-code`) and select it as an agent's *Runtime*.

Local policy wins: the runner's own `runner.json` can only narrow what the server asks for (allowed presets, no web,
which command to run, which ACP login to use), and `cueflow runner status` checks the connection. Revoking a runner in
the dashboard stops it at once. Details and an example: [api.md](api.md#runners).

To keep the agent away from your own logins, run the runner in a container with its own Claude token, a GitHub token
of its own and a clean work directory: [deploy/runner](../deploy/runner/README.md). To add tools or another agent CLI
to that container, see [runner-image.md](runner-image.md).

## Self-driven agents

An agent can also drive itself: give it the **External (self-driven)** runtime, create an agent token on its
edit page, and connect any MCP client to `/mcp` with it. The agent claims its own tasks (`claim_task`) and
works them with the same tools. See [api.md](api.md#runners).
