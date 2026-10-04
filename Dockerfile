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

# 3. Minimal runtime
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /usr/local/bin/
ENV CUEFLOW_ADDR=0.0.0.0:3000
EXPOSE 3000
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["/usr/local/bin/cueflow", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/cueflow"]
