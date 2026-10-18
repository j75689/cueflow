# Modules

The core of CueFlow knows nothing about any one kind of work: orgs, agents, tasks, the board, schedules,
approvals, runners, connectors and the MCP endpoints. Everything domain-specific is a module. A module brings
its own REST routes, agent capabilities and tools, workers, cron and dashboard pages, and registers them when
it is mounted.

`CUEFLOW_MODULES` picks the modules to mount, comma-separated (`all`, `none`, or any of `content`,
`deliverables`; default: all). A module that is not mounted has no routes, tools, workers or cron; its
tables stay, and the dashboard hides its pages. `GET /api/v1/meta` lists what is mounted in `modules`.

| Module | Adds | Code |
|---|---|---|
| `deliverables` | Typed items agents produce for a human to review, then something acts on them | `internal/deliverable` |
| `content` | Social posts as deliverables, source connectors, publishers | `internal/content` |

Writing a module: implement `api.Module` (`internal/api/module.go`: `ID` and `Routes`), plus any of the optional
hooks: `MetaProvider` (keys on `/meta`), `StatsProvider` (`/stats`), `TaskDetailer` (`GET /tasks/{id}` and the MCP
`get_task`), `MCPProvider` (operator MCP tools). Then mount it in `cmd/cueflow/main.go` behind `cfg.Enabled(...)`:
call its own setup there (the built-in modules have an `Attach()` that registers capabilities and tools with
`crew.RegisterCapability` / `crew.RegisterTool` and job kinds with `jobs.Register`), append it to `modules`, and
add its job handlers, sweeps and workers to the lists next to it. Add its id to `KnownModules` in
`internal/config/config.go`.

Media (`/api/v1/media`, `/media/{file}`, image generation) is served by the core whatever is mounted; today only the
content module's posts use it, so it is described under [content](#images-and-videos).

## deliverables

A deliverable is a typed item (`document` is built in; other modules add kinds) that an agent writes into a
review queue. It moves `DRAFT → PENDING_REVIEW → APPROVED | REJECTED`; an approved item is `QUEUED` and the
worker acts on it (`DONE` or `FAILED`, and a failed one can be retried). Nothing acts before a human approves.

- **Acting**: a kind can act by itself (the content module's `post` publishes); otherwise the worker calls the
  org's `action` connector (`mcp_action`, see [connectors.md](connectors.md)). An org with neither is done at
  approval: the review was the point.
- **Agents**: capability `deliverables` (tools `create_deliverable`, `list_deliverables`,
  `revise_deliverable`).
- **Dashboard**: the Deliverables page (review, edit, approve, reject, retry) and a task's deliverables.
- **API and MCP**: `/api/v1/deliverables` ([api.md](api.md)), and `list_deliverables` on the operator MCP.
- **Settings**: `CUEFLOW_DISPATCH_INTERVAL`, `CUEFLOW_DISPATCH_LEASE` ([configuration.md](configuration.md)).

## content

Social content on top of deliverables: posts are deliverables of kind `post`, checked against each platform's
rules and published through a publisher connector (any MCP server that follows the
[publisher contract](publisher-mcp.md)) when approved. Mount it with `deliverables`: without it, drafts have no
review page and nothing publishes them.

- **Agents**: capabilities `post_deliverables` (`create_post_draft`, `list_posts`, `revise_draft`), `imaging`
  (`generate_image`) and `sources` (`search_materials`, `fetch_sources`).
- **Connectors**: source connectors (RSS, IMAP) and the accounts publishers offer
  (`/connectors/{id}/fetch`, `/items`, `/accounts`).
- **Team**: [`templates/social-content.json`](../templates/social-content.json) is a researcher, writer and
  editor that use these capabilities ([templates/README.md](../templates/README.md)).

### Images and videos

Posts reference media as `/media/{id}` (images up to 10 MB; MP4 up to 100 MB). Media gets there in three ways:
uploaded from the review editor, generated with an image connector (by a reviewer, or by an agent with the
`imaging` capability), or attached by an agent from what a tool produced (for example a rendered video).

#### Where the bytes live

`CUEFLOW_MEDIA_STORAGE` decides where new files are kept:

| Value | Storage |
|---|---|
| empty | the database (fine for a small install) |
| `file:///data/media` | a directory, e.g. a Docker volume |
| `gcs://bucket/prefix` | Google Cloud Storage; `/media/{id}` answers with a short-lived signed URL (IAM-signed, no key file) |

`cueflow media migrate --to <target>` moves existing media between backends; URLs do not change, because each
row records which backend holds its bytes.

When handing a post to a publisher, CueFlow turns `/media/{id}` into an absolute URL under
`CUEFLOW_MEDIA_BASE_URL`, which must be reachable from the publisher (Compose sets it to `http://cueflow:3000`).

#### Deleting

- `DELETE /api/v1/media/{id}` removes the item and its file, and answers `409` while a deliverable that is not yet
  done or rejected still uses it.
- `cueflow media prune --older-than 90d [--dry-run]` does the same for everything old and unused.
- `CUEFLOW_MEDIA_RETENTION_DAYS` runs that prune on every tick in cloud mode.

#### Big files from a trusted service

A service of yours that produces big files (a video renderer, for example) should not push them through
CueFlow. With GCS storage it can write straight into the bucket's `uploads/` area with its own Google identity
and have CueFlow adopt the file with `POST /internal/media/register`; the bytes never pass through CueFlow.
The account must be listed in `CUEFLOW_MEDIA_REGISTER_ACCOUNTS`. Details: [api.md](api.md#media) and
[deploy/cloudrun/README.md](../deploy/cloudrun/README.md#services-that-write-media-to-the-bucket).
