.PHONY: all web build test test-e2e lint dev mock clean docker

DIST := internal/webui/dist
TEST_DB ?= postgres://cueflow:cueflow@localhost:55432/cueflow?sslmode=disable

all: build

## web: build the dashboard (static export) and stage it for embedding
web:
	cd web && npm ci && npm run build
	find $(DIST) -mindepth 1 ! -name .gitkeep -delete
	cp -R web/out/. $(DIST)/

## build: build the cueflow binary with the dashboard embedded
build: web
	CGO_ENABLED=0 go build -trimpath -o bin/cueflow ./cmd/cueflow
	CGO_ENABLED=0 go build -trimpath -o bin/mock-publisher-mcp ./cmd/mock-publisher-mcp

## test: unit tests (e2e tests are skipped without a database)
test:
	go test ./...

## test-e2e: full chain against PostgreSQL (start one with `make test-db`)
test-e2e:
	CUEFLOW_TEST_DATABASE_URL="$(TEST_DB)" go test -count=1 ./internal/e2e/

test-db:
	docker run -d --rm --name cueflow-test-pg -e POSTGRES_USER=cueflow -e POSTGRES_PASSWORD=cueflow \
		-e POSTGRES_DB=cueflow -p 55432:5432 postgres:17-alpine

lint:
	go vet ./...
	cd web && npx tsc --noEmit

## dev: run the API (expects DATABASE_URL and CUEFLOW_SECRET_KEY); run `cd web && npm run dev` for the UI
dev:
	go run ./cmd/cueflow

## mock: run the mock publisher MCP server on :4010 (token dev-token)
mock:
	go run ./cmd/mock-publisher-mcp

docker:
	docker compose -f deploy/docker-compose.yml build

clean:
	rm -rf bin web/out web/.next
	find $(DIST) -mindepth 1 ! -name .gitkeep -delete
