# Security policy

CueFlow stores LLM API keys, OAuth tokens and mailbox passwords (encrypted with `CUEFLOW_SECRET_KEY`),
exposes an MCP endpoint, and can start external agents on machines you own. Please report anything that
weakens those guarantees privately.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting: **Security → Report a vulnerability** on this repository.
Do not open a public issue for a security problem.

Include what you found, how to reproduce it, and which version or commit you tested. You will get a
first reply within 7 days. CueFlow is maintained by one person without an on-call rotation, so a fix may
take longer than a reply; you will be told when one is available or if the report is declined.

## Supported versions

Only the latest release and `main` receive fixes. There are no backports.

## What is in scope

- Anything that lets an agent publish, or lets a tool call bypass the allow / ask / deny setting
- Reading or decrypting stored connector credentials without the secret key
- Authentication bypasses on the dashboard, `/api/v1/*`, `/api/runner/*`, `/mcp*` or `/internal/*`
- SSRF through `read_url`, sources or connectors reaching private addresses
- Escaping the restrictions placed on an external agent (ACP) or a runner

## What is out of scope

- Instances exposed to the internet without `CUEFLOW_ADMIN_TOKEN` or Cloudflare Access; the local
  Docker setup binds to `127.0.0.1` for a reason
- Issues in third-party services CueFlow talks to (LLM providers, publishers and MCP servers you add)
- Prompt injection that only affects the content of a draft, since every draft is reviewed by a human
  before publishing
