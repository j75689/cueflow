// Package mcpx wraps the official MCP Go SDK client for connector use: transport selection
// (Streamable HTTP, SSE, stdio), auth headers, JSON tool calls, and tool safety classification.
package mcpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	TransportStreamable = "streamable_http"
	TransportSSE        = "sse"
	TransportStdio      = "stdio"
)

type Options struct {
	Transport  string
	Endpoint   string
	Headers    map[string]string
	Command    string
	Args       []string
	Env        map[string]string
	HTTPClient *http.Client
}

var impl = &mcp.Implementation{Name: "cueflow", Version: "0.1.0"}

// Connect opens an MCP client session. Callers must Close it.
func Connect(ctx context.Context, o Options) (*mcp.ClientSession, error) {
	var t mcp.Transport
	switch o.Transport {
	case TransportStdio:
		if o.Command == "" {
			return nil, errors.New("mcp: stdio transport needs a command")
		}
		cmd := exec.Command(o.Command, o.Args...)
		cmd.Env = os.Environ()
		for k, v := range o.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		t = &mcp.CommandTransport{Command: cmd}
	case TransportSSE:
		t = &mcp.SSEClientTransport{Endpoint: o.Endpoint, HTTPClient: withHeaders(o.HTTPClient, o.Headers)}
	case TransportStreamable, "":
		t = &mcp.StreamableClientTransport{
			Endpoint:             o.Endpoint,
			HTTPClient:           withHeaders(o.HTTPClient, o.Headers),
			DisableStandaloneSSE: true, // request/response only; stateless servers reject the GET stream
			MaxRetries:           2,
		}
	default:
		return nil, fmt.Errorf("mcp: unknown transport %q", o.Transport)
	}
	cs, err := mcp.NewClient(impl, nil).Connect(ctx, t, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: connect: %w", err)
	}
	return cs, nil
}

// ListTools returns every tool, following pagination.
func ListTools(ctx context.Context, cs *mcp.ClientSession) ([]*mcp.Tool, error) {
	var out []*mcp.Tool
	params := &mcp.ListToolsParams{}
	for {
		res, err := cs.ListTools(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("mcp: tools/list: %w", err)
		}
		out = append(out, res.Tools...)
		if res.NextCursor == "" {
			return out, nil
		}
		params.Cursor = res.NextCursor
	}
}

// ToolError is a tool-level failure (isError: true), as opposed to a transport failure.
type ToolError struct {
	Tool, Message string
}

func (e *ToolError) Error() string { return fmt.Sprintf("mcp tool %s: %s", e.Tool, e.Message) }

// CallJSON invokes a tool and returns its result as JSON: structuredContent when present,
// otherwise the first text content (parsed as JSON when possible, else JSON-quoted).
func CallJSON(ctx context.Context, cs *mcp.ClientSession, name string, args any) (json.RawMessage, error) {
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("mcp: tools/call %s: %w", name, err)
	}
	text := textOf(res)
	if res.IsError {
		if text == "" {
			text = "tool returned an error"
		}
		return nil, &ToolError{Tool: name, Message: text}
	}
	if res.StructuredContent != nil {
		return json.Marshal(res.StructuredContent)
	}
	if json.Valid([]byte(text)) {
		return json.RawMessage(text), nil
	}
	return json.Marshal(text)
}

// CallText invokes a tool and returns its text content. A tool-level failure (isError) is not
// a Go error: the caller passes the text on, flagged.
func CallText(ctx context.Context, cs *mcp.ClientSession, name string, args json.RawMessage) (string, bool, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", false, fmt.Errorf("mcp: tools/call %s: %w", name, err)
	}
	text := textOf(res)
	if text == "" && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			text = string(b)
		}
	}
	if text == "" && !res.IsError {
		text = "(no output)"
	}
	return text, res.IsError, nil
}

func textOf(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// sideEffect matches tool names that publish, schedule, mutate or delete. The agent may never call
// these (FR-4.2); only read-only research tools are eligible for agent use.
var sideEffect = regexp.MustCompile(`(?i)(post|publish|schedule|send|create|delete|remove|update|upload|trigger|tweet|share|reply|comment|write|edit|generate)`)

// ClassifyTool reports whether a tool is safe for the agent and why not otherwise.
func ClassifyTool(t *mcp.Tool) (agentAllowed bool, reason string) {
	if t.Annotations != nil && t.Annotations.ReadOnlyHint && !sideEffect.MatchString(t.Name) {
		return true, ""
	}
	if sideEffect.MatchString(t.Name) {
		return false, "publishing or mutating tool"
	}
	if t.Annotations == nil || !t.Annotations.ReadOnlyHint {
		return false, "not annotated read-only"
	}
	return true, ""
}

type headerRT struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
}

func withHeaders(c *http.Client, headers map[string]string) *http.Client {
	if c == nil {
		c = &http.Client{}
	}
	if len(headers) == 0 {
		return c
	}
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	cp := *c
	cp.Transport = &headerRT{base: base, headers: headers}
	return &cp
}
