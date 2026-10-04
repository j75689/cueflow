# syntax=docker/dockerfile:1
#
# Images (targets):
#   slim         default. CueFlow only (distroless). Built-in agents and LLM connectors.
#   agents       + Node.js and the ACP adapters for Claude Code and Codex (external agents).
#   agents-full  + Google's Antigravity ACP server (~1 GB more).
#
#   docker build -t cueflow:slim .
#   docker build --target agents -t cueflow:agents .
#   docker build --target agents-full -t cueflow:agents-full .

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
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mock-postiz-mcp ./cmd/mock-postiz-mcp

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

# 4. agents-full: "agents" plus Google's Antigravity ACP server (proprietary; terms at
#    https://antigravity.google/terms). Downloaded from dl.google.com for the build architecture.
FROM debian:bookworm-slim AS antigravity
ARG ANTIGRAVITY_ACP_VERSION=1.3.0
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl unzip \
 && rm -rf /var/lib/apt/lists/*
RUN set -eux; \
    case "${TARGETARCH:-amd64}" in amd64) arch=x86_64 ;; arm64) arch=arm64 ;; *) echo "unsupported arch ${TARGETARCH}"; exit 1 ;; esac; \
    curl -fsSL -o /tmp/agy.zip "https://dl.google.com/agy-extensions/releases/linux/agy-acp-server-${ANTIGRAVITY_ACP_VERSION}-linux-${arch}.zip"; \
    mkdir -p /opt/antigravity-acp; \
    unzip -q /tmp/agy.zip -d /opt/antigravity-acp; \
    rm /tmp/agy.zip; \
    par=$(find /opt/antigravity-acp -name agy_acp_server.par -type f | head -1); \
    test -n "$par"; chmod +x "$par"; \
    ln -s "$par" /opt/antigravity-acp/agy-acp-server

FROM agents AS agents-full
USER root
COPY --from=antigravity /opt/antigravity-acp /opt/antigravity-acp
RUN ln -s /opt/antigravity-acp/agy-acp-server /usr/local/bin/agy-acp-server
USER nonroot:nonroot

# 5. slim (default, last stage): distroless, no Node.js and no external agents.
FROM gcr.io/distroless/static-debian12:nonroot AS slim
COPY --from=build /out/ /usr/local/bin/
ENV CUEFLOW_ADDR=0.0.0.0:3000
EXPOSE 3000
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["/usr/local/bin/cueflow", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/cueflow"]
