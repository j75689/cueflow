# syntax=docker/dockerfile:1
#
# Images (targets):
#   slim         default. CueFlow only (distroless). Built-in agents and LLM connectors.
#   agents       + Node.js and the ACP adapters for Claude Code and Codex (external agents).
#   runner       agents + git and gh, started as `cueflow runner run` (deploy/runner).
#
#   docker build -t cueflow:slim .
#   docker build --target agents -t cueflow:agents .
#   docker build --target runner -t cueflow:runner .

# 1. Dashboard (Next.js static export)
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# 2. Go binaries with the dashboard embedded
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/out/ ./internal/webui/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cueflow ./cmd/cueflow \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mock-publisher-mcp ./cmd/mock-publisher-mcp

# 3. agents: Node.js plus the ACP adapters for Claude Code and Codex, for the
#    "External agent (ACP)" runtime. Other presets (Gemini CLI, OpenCode...) run via npx or PATH.
FROM node:22-bookworm-slim AS agents
ARG CLAUDE_ACP_VERSION=0.85.1
ARG CODEX_ACP_VERSION=2.1.1
# ca-certificates: the Go binary verifies TLS with the system trust store (node images ship without it).
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && test -s /etc/ssl/certs/ca-certificates.crt
RUN npm install -g --omit=dev \
      @agentclientprotocol/claude-agent-acp@${CLAUDE_ACP_VERSION} \
      @agentclientprotocol/codex-acp@${CODEX_ACP_VERSION} \
 && npm cache clean --force \
 && groupadd -g 65532 nonroot && useradd -u 65532 -g nonroot -m -d /home/nonroot nonroot
COPY --from=build /out/ /usr/local/bin/
ENV CUEFLOW_ADDR=0.0.0.0:3000 HOME=/home/nonroot
EXPOSE 3000
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["/usr/local/bin/cueflow", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/cueflow"]

# 4. runner: a machine that works agents' tasks (see deploy/runner). It adds git and gh so the agent
#    can clone, push a branch and open a pull request with the token it is given (GH_TOKEN), and
#    nothing else of the host: the work directory and the runner credentials are volumes.
FROM agents AS runner
USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends git gh openssh-client \
 && rm -rf /var/lib/apt/lists/* \
 && git config --system credential.helper '!gh auth git-credential' \
 && git config --system --add safe.directory '*' \
 && mkdir -p /work /config && chown nonroot:nonroot /work /config
ENV CUEFLOW_RUNNER_CONFIG=/config/runner.json
WORKDIR /work
USER nonroot:nonroot
HEALTHCHECK NONE
ENTRYPOINT ["/usr/local/bin/cueflow", "runner"]
CMD ["run"]

# 5. slim (default, last stage): distroless, no Node.js and no external agents.
FROM gcr.io/distroless/static-debian12:nonroot AS slim
COPY --from=build /out/ /usr/local/bin/
ENV CUEFLOW_ADDR=0.0.0.0:3000
EXPOSE 3000
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["/usr/local/bin/cueflow", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/cueflow"]
