# Customizing the runner image

[deploy/runner](../deploy/runner/README.md) runs a [runner](external-agents.md#run-agents-on-your-own-machine-runners)
from the published image `ghcr.io/j75689/cueflow:runner`. It contains CueFlow, Node.js, the ACP adapters for Claude
Code and Codex, git, gh and an SSH client, on Debian. When an agent needs more than that (another agent CLI, Python,
ffmpeg...), build your own image on top of it. You don't need a clone of this repository.

## Images and tags

All images are in one package, `ghcr.io/j75689/cueflow`. `runner` and `agents` are built for `linux/amd64` and
`linux/arm64`; `slim` only for amd64.

| Image | Moving tag | Pinned tags |
|---|---|---|
| `runner`: agents + git, gh, SSH, started as `cueflow runner run` | `runner` (follows `main`) | `main-runner`, `<version>-runner`, `sha-<commit>-runner` |
| `agents`: CueFlow + Node.js and the Claude Code / Codex ACP adapters | `agents` (follows `main`) | `main-agents`, `<version>-agents`, `sha-<commit>-agents` |
| `slim`: CueFlow only (distroless) | `main` | `<version>`, `sha-<commit>` |

`FROM ...:runner` picks up every change on `main`. To choose when to upgrade, pin a release tag (`<version>-runner`)
or a `sha-<commit>-runner` tag instead. Both are pushed once and not rebuilt. Every release is built; a commit on
`main` that was superseded by newer pushes before its build started has no `sha-<commit>` tags, so pick a sha from
the [package's tag list](https://github.com/j75689/cueflow/pkgs/container/cueflow) rather than from the git log.

## Just a package or two

If all you need is a few Debian packages, use the `RUNNER_EXTRA_PACKAGES` build argument. It needs the source:
uncomment the `build:` block in `deploy/runner/docker-compose.yml` and give `image:` a local name such as
`cueflow-runner:local` (otherwise `docker compose pull` replaces your build with the published image), then run:

```bash
RUNNER_EXTRA_PACKAGES="python3 ffmpeg" docker compose up -d --build
```

Without a clone, build straight from the repository and set `CUEFLOW_RUNNER_IMAGE=my-runner` in `.env`:

```bash
docker build --target runner --build-arg RUNNER_EXTRA_PACKAGES="python3 ffmpeg" -t my-runner \
  https://github.com/j75689/cueflow.git#main
```

## Your own Dockerfile

Put the Dockerfile next to `deploy/runner/docker-compose.yml`. Install as root and switch back to `nonroot` at the
end:

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

Then point the compose file at it. Either replace `image:` with `build: .`, or build and tag it yourself and set
`CUEFLOW_RUNNER_IMAGE` in `.env`:

```bash
docker build -t my-runner . && echo CUEFLOW_RUNNER_IMAGE=my-runner >> .env
docker compose up -d
```

`docker compose pull` cannot update an image that exists only on your machine. To pick up a newer base, rebuild with
`docker build --pull -t my-runner .` and run `docker compose up -d` again.

Install what agents need at build time. At run time the root file system is read-only and the home directory is a
tmpfs. Anything an agent installs with `npm install -g` or `apt-get` fails, and anything it writes under `~` is gone
after a restart. Leave `ENTRYPOINT`, `CMD`, `WORKDIR` and `HEALTHCHECK` alone unless you mean to change how the runner
starts.

## Another ACP agent

A runner job names a *preset*, the runner pool connector's `preset` field: `claude-code`, `codex`, `gemini`,
`opencode`, `goose` or `custom`. The runner starts the preset's command, so the agent CLI has to be in the image and
the command has to find it:

| Preset | Default command | In your image |
|---|---|---|
| `claude-code`, `codex` | the adapter in the image | nothing to do |
| `gemini` | `npx -y @google/gemini-cli@0.62.0 --acp` (downloads it again after every restart) | install `@google/gemini-cli` and map the command to `gemini --acp` |
| `opencode` | `opencode acp` | install `opencode-ai`; the default command finds it on `PATH` |
| `goose` | `goose acp` | copy the `goose` binary from its release into `/usr/local/bin` |
| `custom` | none | map a command (any ACP server) |

The mapping lives in `runner.json` in the `config` volume, next to the credentials `register` saved there:

```json
{
  "commands": { "gemini": "gemini --acp", "custom": "/usr/local/bin/my-acp-server --stdio" },
  "auth_methods": { "custom": "oauth-personal" },
  "models": { "gemini": "gemini-2.5-pro" },
  "allow_presets": ["claude-code", "gemini"]
}
```

- `commands` replaces a preset's launch command.
- `auth_methods` names the ACP `authenticate` method, for agents that require one.
- `models` pins a model. It wins over the model the server asks for.
- `allow_presets` refuses jobs for any other agent.

The image has no editor, and the file belongs to `nonroot` with mode 0600. Edit it with the image's own Node.js:

```bash
docker compose run --rm --entrypoint node runner -e '
  const fs = require("fs"), f = process.env.CUEFLOW_RUNNER_CONFIG, c = JSON.parse(fs.readFileSync(f));
  c.commands = { ...c.commands, gemini: "gemini --acp" };
  fs.writeFileSync(f, JSON.stringify(c, null, 2), { mode: 0o600 });'
docker compose up -d --force-recreate
```

Credentials go in `.env`, like the Claude token:

- Gemini CLI: `GEMINI_API_KEY`.
- OpenCode: the provider's key, for example `ANTHROPIC_API_KEY` or `OPENAI_API_KEY`.
- Goose: the provider's key, plus `GOOSE_PROVIDER` and `GOOSE_MODEL`.

Each agent process inherits the container's environment, plus `env` from `runner.json`. Finally, give the pool's
connector the matching `preset` and labels.

## Credentials and work directories

Keep the arrangement described in [deploy/runner](../deploy/runner/README.md#what-it-limits). The image holds tools
only, never credentials, and each run gets only what you hand it.

| Path | Mount | Why |
|---|---|---|
| `/config` | named volume, read-write | `runner.json`: the runner token saved by `register` |
| `/work` | named volume, read-write | what agents clone and edit; `docker compose down -v` wipes it |
| `/etc/claude-code/managed-settings.json` | file, **read-only** | Claude Code settings the agent must not change |
| `/home/nonroot`, `/tmp` | tmpfs | the agents' own state; gone on restart |
| everything else | read-only root file system | the agent cannot change its own tools |

Secrets come from `.env` (`env_file`), never from `COPY` or `ARG` in a Dockerfile: anyone who pulls an image can read
everything built into it. Mount other files the agent must obey read-only too, such as an agent's managed policy or
an SSH `known_hosts`. Don't mount your home directory, `~/.ssh`, `~/.config/gh` or the Docker socket: each of those
hands the agent your own logins or the host itself.

## What stays the same

Images built `FROM ghcr.io/j75689/cueflow:runner` can rely on the following. A change to any of them is treated as a
breaking change:

- `/usr/local/bin/cueflow` is the CueFlow binary. The image starts as `cueflow runner run`
  (`ENTRYPOINT ["/usr/local/bin/cueflow", "runner"]`, `CMD ["run"]`).
- The runner reads its configuration from `/config/runner.json` (`CUEFLOW_RUNNER_CONFIG`).
- `/work` is the working directory. It is where agents work when the shell is allowed.
- The image runs as user `nonroot`, uid and gid 65532, with `HOME=/home/nonroot`.
- `apt-get` and `npm` are available, so a derived image can install packages as root.

The following may change in any release:

- the Debian release and the Node.js major version;
- the versions of the bundled ACP adapters;
- the exact list of preinstalled packages, beyond git, gh and an SSH client;
- the presets' default commands.

If your image depends on any of them, pin a `sha-<commit>-runner` tag.
