# syntax=docker/dockerfile:1

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

# 3a. Minimal runtime without external agents: docker build --target slim .
FROM gcr.io/distroless/static-debian12:nonroot AS slim
COPY --from=build /out/ /usr/local/bin/
ENV CUEFLOW_ADDR=0.0.0.0:3000
EXPOSE 3000
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["/usr/local/bin/cueflow", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/cueflow"]

# 3b. Default runtime: Node.js plus the ACP adapters for Claude Code and Codex, so agents can use
# the "External agent (ACP)" runtime. Other presets (Gemini, OpenCode...) run via npx or PATH.
FROM node:22-bookworm-slim AS full
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
