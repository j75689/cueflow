// Package mcptools connects agents to an external MCP server (Notion, Drive, a database, your own
// service...). Which tools agents may call is set per tool in the connector's permissions:
// allow, ask (a human approves each call) or deny.
package mcptools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/connector"
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
			{Key: "command", Label: "Command (stdio)", Type: connector.FieldString, Placeholder: "npx",
				Help: "Runs inside the CueFlow container; npx needs the agents image"},
			{Key: "args", Label: "Arguments (stdio)", Type: connector.FieldStringList},
			{Key: "env", Label: "Environment (stdio)", Type: connector.FieldKV},
			{Key: "secret_env", Label: "Secret environment (stdio)", Type: connector.FieldText, Secret: true,
				Help: "KEY=VALUE per line, e.g. API tokens"},
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
	opts mcpx.Options
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	o := mcpx.Options{
		Transport:  cfg.String("transport"),
		Endpoint:   cfg.String("endpoint_url"),
		Command:    cfg.String("command"),
		Args:       cfg.StringList("args"),
		HTTPClient: env.HTTPClient,
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
	for k, v := range cfg.KV("headers") {
		o.Headers[k] = v
	}
	if t := cfg.String("auth_token"); t != "" {
		o.Headers["Authorization"] = "Bearer " + t
	}
	for k, v := range cfg.KV("env") {
		o.Env[k] = v
	}
	sc := bufio.NewScanner(strings.NewReader(cfg.String("secret_env")))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("mcp server: secret environment line %q is not KEY=VALUE", line)
		}
		o.Env[strings.TrimSpace(k)] = v
	}
	return &Server{opts: o}, nil
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
	return mcpx.CallText(ctx, cs, name, args)
}
