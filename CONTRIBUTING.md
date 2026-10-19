# Contributing to CueFlow

Thanks for taking a look. CueFlow is maintained by one person in spare time, so small, focused changes
are the ones most likely to land quickly.

## Before you start

- **Bugs**: open an issue with the version, how CueFlow runs (Docker, Cloud Run, `go run`), what you did
  and what happened. Logs with secrets removed help.
- **Features**: open an issue first for anything beyond a small fix, so we can agree on the shape before
  you write it. New connectors are the most welcome kind of contribution; see
  [docs/connectors.md](docs/connectors.md).
- **Security problems**: never in an issue. See [SECURITY.md](SECURITY.md).

## Development setup

Requirements: Go 1.27+, Node 26+, Docker (for PostgreSQL).

```bash
make test-db                      # disposable PostgreSQL on :55432
export DATABASE_URL="postgres://cueflow:cueflow@localhost:55432/cueflow?sslmode=disable"
export CUEFLOW_SECRET_KEY=$(go run ./cmd/cueflow genkey)

go run ./cmd/cueflow              # API on :3000
go run ./cmd/mock-publisher-mcp   # mock publisher on :4010 (token dev-token)
cd web && npm install && npm run dev   # dashboard on :3001, proxies /api to :3000
```

Before opening a pull request:

```bash
make lint                         # go vet, tsc
make test                         # unit tests
make test-e2e                     # full chain against PostgreSQL
scripts/check-no-private-refs.sh  # what CI runs on the tree
```

CI runs the same checks plus a Docker build and a secret scan (gitleaks).

## Rules that are not negotiable

- **No agent tool may publish.** Publishing happens only in the dispatch worker after a human approval.
  A pull request that adds a tool, flag or shortcut around that will not be merged.
- **Secrets stay encrypted.** Anything a connector stores goes through `internal/secret`.
- **Private addresses stay private.** `read_url`, sources and connectors must keep refusing internal
  hosts unless the operator opts in explicitly.
- **Migrations are append-only.** Add `internal/db/migrations/000N_*.sql`; never edit a shipped one.

## Style

- Go: `gofmt`, `go vet`, errors wrapped with context, tests next to the code (table-driven where it fits).
- Web: TypeScript strict, Tailwind, no new UI library.
- Docs: English is the source of truth. Updating `docs/zh-TW/` and `README.zh-TW.md` in the same pull
  request is appreciated but not required; say so in the description if you skipped it.
- Commits follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/):
  `feat(crew): ...`, `fix(rss): ...`, `docs: ...`. One logical change per commit.

## Pull requests

- Describe what changed and why, and how you tested it.
- Keep refactors separate from behaviour changes.
- Expect a first reply within a week. If a change needs discussion, it may take longer.

By contributing you agree that your contribution is licensed under the Apache-2.0 license of this repository.
