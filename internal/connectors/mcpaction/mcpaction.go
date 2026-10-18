// Package mcpaction is an action connector for any MCP server: an approved deliverable is handed
// to one tool of the server ("deliver" by default) and whatever the tool reports is kept on the
// item. Where the deliverable ends up (a wiki page, a ticket, a message, a file) is the server's
// business; CueFlow keeps the review, the approval and the retries.
//
// The tool receives {id, kind, title, payload, previous} and may reply with
// {"external_ids": {...}, "details": {...}}; a reply of any other shape is kept as details. A tool
// error means the server refused the item and nothing happened; no reply (a timeout) means the
// outcome is unknown and the item is failed with a warning to check before retrying.
package mcpaction

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/mcpx"
)

const defaultTool = "deliver"

func init() {
	connector.Register(connector.Spec{
		Type:        "mcp_action",
		Kind:        connector.KindAction,
		Name:        "MCP action",
		Description: "Hands approved deliverables to one tool of an MCP server you run (file a document, open a ticket, send a message...). Only the worker calls it, and only for items a human approved.",
		Fields: []connector.Field{
			{Key: "endpoint_url", Label: "MCP endpoint", Type: connector.FieldString, Required: true,
				Placeholder: "https://tools.example.com/mcp", Help: "The server's URL + /mcp (Streamable HTTP)"},
			{Key: "auth_token", Label: "Bearer token", Type: connector.FieldPassword, Secret: true,
				Help: "Sent as Authorization: Bearer <token>"},
			{Key: "headers", Label: "Extra headers", Type: connector.FieldKV},
			{Key: "secret_headers", Label: "Secret headers", Type: connector.FieldText, Secret: true,
				Help: "Header-Name: value, one per line. For a Cloudflare Access service token: CF-Access-Client-Id and CF-Access-Client-Secret"},
			{Key: "tool", Label: "Tool", Type: connector.FieldString, Default: defaultTool,
				Help: "The tool that receives approved deliverables: {id, kind, title, payload, previous}"},
			{Key: "kinds", Label: "Deliverable kinds", Type: connector.FieldStringList,
				Help: "Optional. Only these kinds are sent; others are refused (blank = any)"},
		},
		DefaultName: func(cfg connector.Config) string {
			if u, err := url.Parse(cfg.String("endpoint_url")); err == nil && u.Host != "" {
				return connector.WithDetail("MCP action", u.Host)
			}
			return ""
		},
	}, New)
}

type Action struct {
	opts  mcpx.Options
	tool  string
	kinds []string
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	opts := mcpx.Options{Transport: mcpx.TransportStreamable, Endpoint: cfg.String("endpoint_url"), HTTPClient: env.HTTPClient,
		Headers: map[string]string{}}
	if opts.Endpoint == "" {
		return nil, errors.New("mcp action: an MCP endpoint is required")
	}
	for k, v := range cfg.KV("headers") {
		opts.Headers[k] = v
	}
	sc := bufio.NewScanner(strings.NewReader(cfg.String("secret_headers")))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("mcp action: secret header line %q is not Header-Name: value", line)
		}
		opts.Headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if t := cfg.String("auth_token"); t != "" {
		opts.Headers["Authorization"] = "Bearer " + t
	}
	tool := strings.TrimSpace(cfg.String("tool"))
	if tool == "" {
		tool = defaultTool
	}
	return &Action{opts: opts, tool: tool, kinds: cfg.StringList("kinds")}, nil
}

func (a *Action) Test(ctx context.Context) (*connector.TestResult, error) {
	cs, err := mcpx.Connect(ctx, a.opts)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	defer cs.Close()
	tools, err := mcpx.ListTools(ctx, cs)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	names := make([]string, 0, len(tools))
	found := false
	for _, t := range tools {
		names = append(names, t.Name)
		found = found || t.Name == a.tool
	}
	if !found {
		return &connector.TestResult{OK: false, Message: fmt.Sprintf("connected, but tool %q is missing (server has: %s)", a.tool, strings.Join(names, ", ")),
			Details: map[string]any{"tools": names}}, nil
	}
	return &connector.TestResult{OK: true, Message: fmt.Sprintf("connected: tool %q found (%d tools)", a.tool, len(tools)),
		Details: map[string]any{"tools": names}}, nil
}

// ListTools shows the server's tools; none of them is ever offered to an agent.
func (a *Action) ListTools(ctx context.Context) ([]connector.ToolInfo, error) {
	cs, err := mcpx.Connect(ctx, a.opts)
	if err != nil {
		return nil, err
	}
	defer cs.Close()
	tools, err := mcpx.ListTools(ctx, cs)
	if err != nil {
		return nil, err
	}
	out := make([]connector.ToolInfo, 0, len(tools))
	for _, t := range tools {
		out = append(out, connector.ToolInfo{Name: t.Name, Description: t.Description, AgentAllowed: false,
			Reason: "action tools are only invoked by the worker for approved deliverables"})
	}
	return out, nil
}

func (a *Action) Act(ctx context.Context, req connector.ActRequest) (*connector.ActResult, error) {
	if len(a.kinds) > 0 && !contains(a.kinds, req.Kind) {
		return nil, fmt.Errorf("this action connector takes %s deliverables, not %s", strings.Join(a.kinds, ", "), req.Kind)
	}
	cs, err := mcpx.Connect(ctx, a.opts)
	if err != nil {
		return nil, err
	}
	defer cs.Close()
	var payload any = map[string]any{}
	_ = json.Unmarshal(req.Payload, &payload)
	args := map[string]any{"id": req.DeliverableID.String(), "kind": req.Kind, "title": req.Title, "payload": payload}
	if len(req.Previous) > 0 {
		args["previous"] = req.Previous
	}
	raw, err := mcpx.CallJSON(ctx, cs, a.tool, args)
	if err != nil {
		var te *mcpx.ToolError
		if errors.As(err, &te) {
			return nil, err // the server refused: nothing happened
		}
		return nil, fmt.Errorf("no reply from the action server to %s (%v): the action may have happened; check before retrying", a.tool, err)
	}
	return parseResult(raw), nil
}

// parseResult reads the tool's reply: external_ids and details when it follows the contract,
// otherwise the whole reply as details.
func parseResult(raw json.RawMessage) *connector.ActResult {
	res := &connector.ActResult{ExternalIDs: map[string]string{}, Details: map[string]any{}}
	var shaped struct {
		ExternalIDs map[string]string `json:"external_ids"`
		Details     map[string]any    `json:"details"`
	}
	if err := json.Unmarshal(raw, &shaped); err == nil && (shaped.ExternalIDs != nil || shaped.Details != nil) {
		if shaped.ExternalIDs != nil {
			res.ExternalIDs = shaped.ExternalIDs
		}
		if shaped.Details != nil {
			res.Details = shaped.Details
		}
		return res
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil && obj != nil {
		res.Details = obj
		if id, ok := obj["id"].(string); ok && id != "" {
			res.ExternalIDs["id"] = id
		}
		return res
	}
	res.Details["reply"] = strings.TrimSpace(string(raw))
	return res
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), s) {
			return true
		}
	}
	return false
}
