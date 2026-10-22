package crew

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/mcpx"
	"github.com/j75689/cueflow/internal/version"
)

// MCPPathPrefix is where per-run tool servers are mounted on CueFlow's HTTP server.
const MCPPathPrefix = "/mcp/run/"

// mcpRun is the tool server for one external-agent run. The random token in its URL is the only
// credential: it is created when the run starts, scoped to that task and agent's capabilities,
// and removed when the run ends.
type mcpRun struct {
	env    *runEnv
	server *mcp.Server
	token  string
	turn   chan struct{} // serializes tool calls within a run (counters, outcome); a call waits for it only while its request lasts
	done   chan struct{}
	once   sync.Once
	// born is when this instance learned of the run (for pruning runs it does not own).
	born time.Time
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

func (r *mcpRegistry) put(token string, run *mcpRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs == nil {
		r.runs = map[string]*mcpRun{}
	}
	// Runs this instance only serves on behalf of another's waiter are never unregistered by an
	// owner here; drop the old ones so the map cannot grow without bound.
	for k, v := range r.runs {
		if time.Since(v.born) > runTokenTTL {
			delete(r.runs, k)
		}
	}
	r.runs[token] = run
}

func (r *mcpRegistry) remove(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, token)
}

// runTokenTTL bounds the life of a run's MCP credential (longer than any run timeout).
const runTokenTTL = 2 * time.Hour

// outcomeRecord is an outcome as stored with the run's token, so any instance can read it.
type outcomeRecord struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// newMCPRun exposes env's tools as an MCP server for the run identified by token.
func (c *Crew) newMCPRun(ctx context.Context, env *runEnv, token string) *mcpRun {
	run := &mcpRun{env: env, token: token, turn: make(chan struct{}, 1), done: make(chan struct{}), born: time.Now()}
	srv := mcp.NewServer(&mcp.Implementation{Name: "cueflow", Version: version.Version}, &mcp.ServerOptions{Capabilities: mcpx.StatelessCapabilities()})
	byName := map[string]tool{}
	for _, t := range c.agentTools(ctx, env.agent) {
		t := t
		byName[t.name] = t
		srv.AddTool(&mcp.Tool{Name: t.name, Description: t.description, InputSchema: t.schema},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return run.call(ctx, c, byName, t.name, req.Params.Arguments), nil
			})
	}
	run.server = srv
	return run
}

// registerMCPRun gives a run its tool server and returns the token for its URL. The credential is
// recorded in the database, so any instance can serve the run's tool calls and a terminal call
// made on another instance still ends this one's wait. Call the returned func when the run ends.
func (c *Crew) registerMCPRun(ctx context.Context, env *runEnv) (string, *mcpRun, func()) {
	token, err := c.store.CreateRunToken(ctx, env.task.ID, env.agent.ID, env.runID, runTokenTTL)
	if err != nil {
		// Without the database record only this instance can serve the run: fine locally.
		c.logger.Warn("record run token", "err", err)
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		token = hex.EncodeToString(b)
	}
	run := c.newMCPRun(ctx, env, token)
	c.mcp.put(token, run)
	if c.Queue != nil {
		go c.watchOutcome(ctx, run)
	}
	return token, run, func() {
		c.mcp.remove(token)
		_ = c.store.DeleteRunToken(context.WithoutCancel(ctx), token)
	}
}

// watchOutcome notices a terminal tool call that another instance served and ends the wait.
func (c *Crew) watchOutcome(ctx context.Context, run *mcpRun) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-run.done:
			return
		case <-t.C:
		}
		rt, err := c.store.RunTokenByPlain(ctx, run.token)
		if err != nil || len(rt.Outcome) == 0 {
			continue
		}
		var rec outcomeRecord
		if json.Unmarshal(rt.Outcome, &rec) != nil {
			continue
		}
		run.turn <- struct{}{}
		if run.env.outcome == nil {
			run.env.outcome = &outcome{kind: rec.Kind, text: rec.Text}
		}
		<-run.turn
		run.once.Do(func() { close(run.done) })
		return
	}
}

// lookupMCPRun finds a run's tool server: here if this instance owns the run, otherwise rebuilt
// from the database record.
func (c *Crew) lookupMCPRun(ctx context.Context, token string) *mcpRun {
	if run := c.mcp.get(token); run != nil {
		return run
	}
	rt, err := c.store.RunTokenByPlain(ctx, token)
	if err != nil {
		return nil
	}
	task, err := c.store.GetTask(ctx, rt.TaskID)
	if err != nil {
		return nil
	}
	agent, err := c.store.GetAgent(ctx, rt.AgentID)
	if err != nil {
		return nil
	}
	env := &runEnv{c: c, task: task, agent: agent, runID: rt.RunID}
	run := c.newMCPRun(ctx, env, token)
	var rec outcomeRecord
	if len(rt.Outcome) > 0 && json.Unmarshal(rt.Outcome, &rec) == nil {
		env.outcome = &outcome{kind: rec.Kind, text: rec.Text}
		run.once.Do(func() { close(run.done) })
	}
	c.mcp.put(token, run)
	return run
}

func (r *mcpRun) call(ctx context.Context, c *Crew, byName map[string]tool, name string, args json.RawMessage) *mcp.CallToolResult {
	select {
	case r.turn <- struct{}{}:
		defer func() { <-r.turn }()
	case <-ctx.Done():
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{
			Text: "Timed out waiting for this run's previous tool call to finish; try again."}}}
	}
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
		_ = c.store.SetRunTokenOutcome(context.WithoutCancel(ctx), r.token,
			outcomeRecord{Kind: r.env.outcome.kind, Text: r.env.outcome.text})
		r.once.Do(func() { close(r.done) })
	}
	return &mcp.CallToolResult{IsError: isErr, Content: []mcp.Content{&mcp.TextContent{Text: out}}}
}

// MCPHandler serves every active run's tool server under MCPPathPrefix, from any instance.
func (c *Crew) MCPHandler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if run := c.lookupMCPRun(r.Context(), tokenFromPath(r.URL.Path)); run != nil {
			return run.server
		}
		return nil
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	serve := mcpx.Stateless(h) // no request held open (Cloud Run bills one, and each takes a slot)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.lookupMCPRun(r.Context(), tokenFromPath(r.URL.Path)) == nil {
			http.Error(w, "unknown or finished run", http.StatusNotFound)
			return
		}
		serve.ServeHTTP(w, r)
	})
}

func tokenFromPath(p string) string {
	return strings.Trim(strings.TrimPrefix(p, MCPPathPrefix), "/")
}
