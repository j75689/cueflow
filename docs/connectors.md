# Writing a connector plugin

Every integration in CueFlow is a **connector**: a Go package that registers a *type* with a config
schema and a factory. The dashboard renders the config form from the schema, the store encrypts the
fields marked secret, and the rest of the system talks to the connector through one of five small
interfaces. Postiz is just the first publisher; adding another (a direct Threads API client, a webhook,
Buffer, Mastodon...) does not touch the core.

## Kinds and interfaces

All interfaces live in [`internal/connector`](../internal/connector/connector.go). Every instance
implements `Test(ctx) (*TestResult, error)`, which backs the **Test** button.

| Kind | Interface | Called by |
|---|---|---|
| `publisher` | `Publish(ctx, PublishRequest) (*PublishResult, error)` | dispatch worker, for approved posts only |
| `source` | `Fetch(ctx, FetchOptions) ([]Item, error)` | pipeline runs and the **Fetch now** button |
| `search` | `Search(ctx, query, limit) ([]SearchResult, error)` | the agent's `web_search` tool |
| `llm` | `Chat(ctx, llm.Request) (*llm.Response, error)`, `Model()` | the agent loop |
| `notifier` | `Notify(ctx, Notification) error` | pipeline runs and the review digest |
| `runtime` | `Launch() RuntimeLaunch` | the crew runner, for agents that run in an external ACP agent |
| `image` | `GenerateImage(ctx, ImageRequest) ([]Image, error)` | the **Generate image** button and the agent's `generate_image` tool |
| `tools` | `ExternalTools(ctx)`, `CallTool(ctx, name, args)` | agents the server is attached to, per tool permission (allow / ask / deny); see agentic-flow §11b |

Optional extras:

- `ListTools(ctx) ([]ToolInfo, error)` (`connector.ToolLister`) - MCP-backed connectors list their tools in the UI.
- `Close() error` - release resources; called after each use.
- `env.State` (`connector.StateStore`) - persist a small cursor between runs (e.g. a last-seen ID).

## Minimal example: a webhook publisher

```go
// internal/connectors/webhook/webhook.go
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/j75689/cueflow/internal/connector"
)

func init() {
	connector.Register(connector.Spec{
		Type:        "webhook",
		Kind:        connector.KindPublisher,
		Name:        "Webhook",
		Description: "POSTs approved posts as JSON to a URL.",
		Fields: []connector.Field{
			{Key: "url", Label: "URL", Type: connector.FieldString, Required: true},
			{Key: "token", Label: "Bearer token", Type: connector.FieldPassword, Secret: true},
		},
	}, func(cfg connector.Config, env connector.Env) (connector.Instance, error) {
		return &Publisher{url: cfg.String("url"), token: cfg.String("token"), http: env.HTTPClient}, nil
	})
}

type Publisher struct {
	url, token string
	http       *http.Client
}

func (p *Publisher) Test(ctx context.Context) (*connector.TestResult, error) {
	return &connector.TestResult{OK: true, Message: "configured"}, nil
}

func (p *Publisher) Publish(ctx context.Context, req connector.PublishRequest) (*connector.PublishResult, error) {
	body, _ := json.Marshal(req)
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		r.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.http.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
	}
	return &connector.PublishResult{ExternalIDs: map[string]string{}}, nil
}
```

Then add one line to [`internal/connectors/all/all.go`](../internal/connectors/all/all.go):

```go
_ "github.com/j75689/cueflow/internal/connectors/webhook"
```

Rebuild, and *Webhook* appears under **Connectors → Add**.

## Rules the host enforces for you

- **Publishing authorization.** `connector.Build` wraps every publisher. `Publish` returns
  `guard.ErrPublishForbidden` unless the context carries a grant for `req.PostID`, which only the dispatch
  worker adds after claiming a human-approved post, and never inside an agent run. Do not try to bypass it.
- **Validation and defaults.** Required fields, numbers, booleans, select options, lists and key/value
  fields are validated before your factory runs; `Default` values are filled in.
- **Secrets.** Fields with `Secret: true` are encrypted with AES-256-GCM and never returned by the API.
  Your factory receives them decrypted in `cfg`.
- **Errors.** Return errors with a message a reviewer can act on - they are shown verbatim on `FAILED` posts
  and in run logs.
- **Idempotency.** If the platform supports an idempotency key, derive it from `req.PostID`.

## Field types

`string`, `password`, `text` (multi-line), `number`, `bool`, `select` (with `Options`), `string_list`,
`kv` (string → string map), `json`. `Config` has helpers: `String`, `Int`, `Bool`, `StringList`, `KV`.

## MCP-based connectors

[`internal/mcpx`](../internal/mcpx/mcpx.go) wraps the official MCP Go SDK: `Connect` (Streamable HTTP,
SSE or stdio, with auth headers), `ListTools`, `CallJSON` (tool errors become Go errors) and
`ClassifyTool`, which flags publishing/mutating tools so they are never offered to the agent. The Postiz
connector ([`internal/connectors/postiz`](../internal/connectors/postiz/postiz.go)) is the reference
implementation, and [`internal/postizmock`](../internal/postizmock/postizmock.go) shows how to test one
against an in-process MCP server.

## Images and media

Images a reviewer uploads or an image connector generates are stored by CueFlow (table `media`) and
referenced from posts as `/media/{id}`. Publishers receive `PublishRequest.MediaURLs` as absolute URLs
(`CUEFLOW_MEDIA_BASE_URL` + path) and `PublishRequest.Media`, where stored images carry a `Load` func
for their bytes. Use the bytes when your service cannot reach CueFlow (the MCP publisher uploads them as base64);
use the URL when it can (Postiz fetches it).

An image connector returns raw bytes; CueFlow checks the type (PNG, JPEG, WebP or GIF, at most 10 MB)
and stores the first image. Map `ImageRequest.Aspect` (one of `connector.Aspects`) to your provider's
nearest size. Built-in: `openai_image`, `gemini_image` (AI Studio), `vertex_image`, `comfyui`.

## Per-account publishers

A publisher that posts to individual accounts (several Threads, Instagram or YouTube accounts, for example) sets
`Spec.Accounts` and implements `connector.AccountLister`. The review editor then lists the accounts and
stores the reviewer's choice on the post; `Publish` receives it as `PublishRequest.Accounts`.

Set `Spec.AccountsRequired` too when a post cannot go out without accounts: the editor then blocks approval
until some are picked. Otherwise an empty `Accounts` means "use your defaults". The MCP publisher
([`internal/connectors/mcppublisher`](../internal/connectors/mcppublisher/mcppublisher.go); its tool contract is in
[publisher-mcp.md](publisher-mcp.md)) requires accounts; the Postiz connector treats picked channels as optional
and falls back to its channel mapping.

When a publish fails after something was created remotely, return a `*connector.PartialError` with the
external IDs that exist. The dispatch worker keeps them on the failed post and passes them back as
`PublishRequest.Previous` on retry, so the connector can finish that attempt (the MCP publisher calls
`publish_post`, which only retries the accounts that failed) instead of publishing a second copy.

If a call gets no reply, the outcome is unknown. Report a plain error that says so; don't guess. The
reviewer checks the platform before pressing Retry.
