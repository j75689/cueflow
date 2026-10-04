package crew

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/llm"
)

// MCPPathPrefix is where per-run tool servers are mounted on CueFlow's HTTP server.
const MCPPathPrefix = "/mcp/run/"

// mcpRun is the tool server for one external-agent run. The random token in its URL is the only
// credential: it is created when the run starts, scoped to that task and agent's capabilities,
// and removed when the run ends.
type mcpRun struct {
	env    *runEnv
	server *mcp.Server
	mu     sync.Mutex // serializes tool calls within a run (counters, outcome)
	done   chan struct{}
	once   sync.Once
}

type mcpRegistry struct {
	mu   sync.Mutex
	runs map[string]*mcpRun
}

func (r *mcpRegistry) get(token string) *mcpRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[token]
}

// registerMCPRun exposes env's tools as an MCP server and returns its token. Call the returned
// func when the run ends.
func (c *Crew) registerMCPRun(env *runEnv) (string, *mcpRun, func()) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)

	run := &mcpRun{env: env, done: make(chan struct{})}
	srv := mcp.NewServer(&mcp.Implementation{Name: "cueflow", Version: "0.1.0"}, nil)
	byName := map[string]tool{}
	for _, t := range toolsFor(env.agent) {
		t := t
		byName[t.name] = t
		srv.AddTool(&mcp.Tool{Name: t.name, Description: t.description, InputSchema: t.schema},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return run.call(ctx, c, byName, t.name, req.Params.Arguments), nil
			})
	}
	run.server = srv

	c.mcp.mu.Lock()
	if c.mcp.runs == nil {
		c.mcp.runs = map[string]*mcpRun{}
	}
	c.mcp.runs[token] = run
	c.mcp.mu.Unlock()
	return token, run, func() {
		c.mcp.mu.Lock()
		delete(c.mcp.runs, token)
		c.mcp.mu.Unlock()
	}
}

func (r *mcpRun) call(ctx context.Context, c *Crew, byName map[string]tool, name string, args json.RawMessage) *mcp.CallToolResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.done:
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{
			Text: "This task run has already finished; stop working."}}}
	default:
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	out, isErr := c.execTool(guard.WithAgent(ctx), r.env, byName, llm.ToolCall{ID: uuid.NewString(), Name: name, Arguments: args})
	if r.env.outcome != nil {
		r.once.Do(func() { close(r.done) })
	}
	return &mcp.CallToolResult{IsError: isErr, Content: []mcp.Content{&mcp.TextContent{Text: out}}}
}

// MCPHandler serves every active run's tool server under MCPPathPrefix.
func (c *Crew) MCPHandler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if run := c.mcp.get(tokenFromPath(r.URL.Path)); run != nil {
			return run.server
		}
		return nil
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.mcp.get(tokenFromPath(r.URL.Path)) == nil {
			http.Error(w, "unknown or finished run", http.StatusNotFound)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func tokenFromPath(p string) string {
	return strings.Trim(strings.TrimPrefix(p, MCPPathPrefix), "/")
}
