// Package mcptools connects agents to an external MCP server (Notion, Drive, a database, your own
// service...). Which tools agents may call is set per tool in the connector's permissions:
// allow, ask (a human approves each call) or deny.
package mcptools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/net/http/httpguts"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/googleid"
	"github.com/j75689/cueflow/internal/headerlines"
	"github.com/j75689/cueflow/internal/mcpx"
)

func init() {
	connector.Register(connector.Spec{
		Type:        "mcp_server",
		Kind:        connector.KindTools,
		Name:        "MCP server",
		Description: "Gives agents the tools of an external MCP server. Set each tool to allow, ask (you approve every call) or deny after saving.",
		Fields: []connector.Field{
			{Key: "transport", Label: "Transport", Type: connector.FieldSelect,
				Options: []string{mcpx.TransportStreamable, mcpx.TransportSSE, mcpx.TransportStdio}, Default: mcpx.TransportStreamable},
			{Key: "endpoint_url", Label: "Server URL", Type: connector.FieldString, Placeholder: "https://mcp.example.com/mcp",
				Help: "Streamable HTTP or SSE endpoint"},
			{Key: "auth_token", Label: "Bearer token", Type: connector.FieldPassword, Secret: true,
				Help: "Sent as Authorization: Bearer <token>"},
			{Key: "headers", Label: "Extra headers", Type: connector.FieldKV},
			{Key: "secret_headers", Label: "Secret headers", Type: connector.FieldText, Secret: true,
				Help: "Header-Name: value, one per line (Streamable HTTP or SSE). For a Cloudflare Access service token: CF-Access-Client-Id and CF-Access-Client-Secret"},
			{Key: "google_identity", Label: "Sign requests with CueFlow's Google identity", Type: connector.FieldBool,
				Help: "For a private Cloud Run service: each request carries an ID token of the account CueFlow runs as (it needs roles/run.invoker on that service); the bearer token above still goes in Authorization. Off for anything else"},
			{Key: "command", Label: "Command (stdio)", Type: connector.FieldString, Placeholder: "npx",
				Help: "Runs inside the CueFlow container; npx needs the agents image"},
			{Key: "args", Label: "Arguments (stdio)", Type: connector.FieldStringList},
			{Key: "env", Label: "Environment (stdio)", Type: connector.FieldKV},
			{Key: "secret_env", Label: "Secret environment (stdio)", Type: connector.FieldText, Secret: true,
				Help: "KEY=VALUE per line, e.g. API tokens"},
			{Key: "timeout_minutes", Label: "Call timeout (minutes)", Type: connector.FieldNumber, Default: 2,
				Help: "How long one tool call may take; raise it for slow tools such as video rendering"},
		},
		DefaultName: func(cfg connector.Config) string {
			if cfg.String("transport") == mcpx.TransportStdio {
				return connector.WithDetail("MCP", strings.TrimSpace(cfg.String("command")+" "+strings.Join(cfg.StringList("args"), " ")))
			}
			if u, err := url.Parse(cfg.String("endpoint_url")); err == nil && u.Host != "" {
				return connector.WithDetail("MCP", u.Host)
			}
			return ""
		},
	}, New)
}

type Server struct {
	opts    mcpx.Options
	timeout time.Duration
}

// CallTimeout is how long one tool call may take (the connector's timeout_minutes).
func (s *Server) CallTimeout() time.Duration { return s.timeout }

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	timeout := time.Duration(cfg.Int("timeout_minutes", 2)) * time.Minute
	if timeout <= 0 || timeout > time.Hour {
		timeout = 2 * time.Minute
	}
	httpClient := &http.Client{}
	if env.HTTPClient != nil {
		*httpClient = *env.HTTPClient
	}
	httpClient.Timeout = timeout
	o := mcpx.Options{
		Transport:  cfg.String("transport"),
		Endpoint:   cfg.String("endpoint_url"),
		Command:    cfg.String("command"),
		Args:       cfg.StringList("args"),
		HTTPClient: httpClient,
		Headers:    map[string]string{},
		Env:        map[string]string{},
	}
	if o.Transport == mcpx.TransportStdio {
		if o.Command == "" {
			return nil, fmt.Errorf("mcp server: stdio needs a command")
		}
	} else if o.Endpoint == "" {
		return nil, fmt.Errorf("mcp server: a server URL is required")
	}
	if cfg.Bool("google_identity") && o.Transport != mcpx.TransportStdio {
		c, err := googleid.Client(context.Background(), httpClient, o.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("mcp server: %w", err)
		}
		o.HTTPClient = c
	}
	// stdio sends no headers, so neither header field nor the bearer token is read there. Elsewhere
	// each header is checked here, so Test connection and agent tool loading report it rather than
	// the first request; canonical names, so the precedence (plain, then secret, then the bearer
	// token) holds whatever case each field uses.
	if o.Transport != mcpx.TransportStdio {
		for k, v := range cfg.KV("headers") {
			if !httpguts.ValidHeaderFieldName(k) {
				return nil, fmt.Errorf("mcp server: header %q: not a valid header name", k)
			}
			if !httpguts.ValidHeaderFieldValue(v) {
				return nil, fmt.Errorf("mcp server: header %q: the value contains a control character", k)
			}
			o.Headers[http.CanonicalHeaderKey(k)] = v
		}
		// The lines hold secrets, names included: name them by number only. The raw value, as
		// cfg.String trims leading blank lines and the numbers must match what the user sees.
		raw, _ := cfg["secret_headers"].(string)
		for i, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			h, err := headerlines.ParseLine(line)
			switch {
			case err != nil:
			case !httpguts.ValidHeaderFieldName(h.Name):
				err = errors.New("not a valid header name")
			case h.Value == "" || !httpguts.ValidHeaderFieldValue(h.Value):
				err = errors.New("the value is empty or contains a control character")
			}
			if err != nil {
				return nil, fmt.Errorf("mcp server: secret headers: line %d: %w", i+1, err)
			}
			o.Headers[http.CanonicalHeaderKey(h.Name)] = h.Value
		}
		if t := cfg.String("auth_token"); t != "" {
			o.Headers["Authorization"] = "Bearer " + t
		}
	}
	for k, v := range cfg.KV("env") {
		o.Env[k] = v
	}
	sc := bufio.NewScanner(strings.NewReader(cfg.String("secret_env")))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			// The line holds a secret value: name it by number only.
			return nil, fmt.Errorf("mcp server: secret environment: line %d: want KEY=VALUE", n)
		}
		o.Env[strings.TrimSpace(k)] = v
	}
	return &Server{opts: o, timeout: timeout}, nil
}

func (s *Server) Test(ctx context.Context) (*connector.TestResult, error) {
	tools, err := s.ExternalTools(ctx)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return &connector.TestResult{OK: true, Message: fmt.Sprintf("connected: %d tools", len(tools)),
		Details: map[string]any{"tools": names}}, nil
}

func (s *Server) ExternalTools(ctx context.Context) ([]connector.ExternalTool, error) {
	cs, err := mcpx.Connect(ctx, s.opts)
	if err != nil {
		return nil, err
	}
	defer cs.Close()
	tools, err := mcpx.ListTools(ctx, cs)
	if err != nil {
		return nil, err
	}
	out := make([]connector.ExternalTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, convert(t))
	}
	return out, nil
}

func convert(t *mcp.Tool) connector.ExternalTool {
	et := connector.ExternalTool{Name: t.Name, Description: t.Description}
	if t.Description == "" && t.Title != "" {
		et.Description = t.Title
	}
	if a := t.Annotations; a != nil {
		et.ReadOnly = a.ReadOnlyHint
		et.Destructive = a.DestructiveHint != nil && *a.DestructiveHint && !a.ReadOnlyHint
	}
	schema := map[string]any{}
	if t.InputSchema != nil {
		if b, err := json.Marshal(t.InputSchema); err == nil {
			_ = json.Unmarshal(b, &schema)
		}
	}
	if schema["type"] == nil {
		schema["type"] = "object"
	}
	if schema["properties"] == nil {
		schema["properties"] = map[string]any{}
	}
	et.InputSchema = schema
	return et
}

func (s *Server) CallTool(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	cs, err := mcpx.Connect(ctx, s.opts)
	if err != nil {
		return "", false, err
	}
	defer cs.Close()
	// Servers such as GitHub's want parameters mirrored into Mcp-Param-* headers (x-mcp-header in
	// the tool's schema). The client only knows which from a tools/list on this same session.
	// A failed listing is not fatal: the call may still work, and its own error is the useful one.
	if s.opts.Transport != mcpx.TransportStdio {
		_, _ = mcpx.ListTools(ctx, cs)
	}
	return mcpx.CallText(ctx, cs, name, args)
}
