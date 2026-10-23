package mcpx

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// StatelessCapabilities are what a stateless server advertises: tools, prompts and resources, but no list-changed
// notifications.
//
// The SDK advertises tools.listChanged as soon as a server has a tool. A client on protocol 2026-07-28 then opens
// subscriptions/listen, a POST the SDK holds open until the client goes away, as no other request can reach it to
// end it. A stateless server never sends those notifications, and on Cloud Run the POST held a request slot for
// 30 minutes; enough of them used up the instances. Without the capability the client opens no listen, and one it
// opens anyway is answered at once.
//
// The SDK does the same for prompts and resources: any of the three left nil gets listChanged: true once one is
// registered, so all three are pinned here. They are advertised even when the server has none; prompts/list and
// resources/list then answer an empty list. A SubscribeHandler would turn on resources.subscribe and hold listens
// the same way, so don't set one on a stateless server.
func StatelessCapabilities() *mcp.ServerCapabilities {
	return &mcp.ServerCapabilities{
		Logging:   &mcp.LoggingCapabilities{},
		Tools:     &mcp.ToolCapabilities{},
		Prompts:   &mcp.PromptCapabilities{},
		Resources: &mcp.ResourceCapabilities{},
	}
}

// Stateless fronts a stateless Streamable HTTP handler: it refuses the GET stream (NoStandaloneStream) and
// gives every request a deadline maxRequest away (CUEFLOW_MCP_REQUEST_TIMEOUT), which its tool call reads
// with RequestDeadline. maxRequest <= 0 sets none.
//
// The SDK runs handlers on a ctx detached from the request's (it neither ends nor has a deadline), and the
// request lasts until its handler returns. The deadline cuts no tool by itself: a run's or agent token's
// tool call bounds by it what may run long (a connector call, the wait for the run's tool turn), and
// built-in tools run to the end, so none stops halfway through its state writes (a thread message that
// can't be written within crew's DetachedWriteTimeout is dropped). At the deadline a request is ended
// unless a tool call runs on it (KeepToolAnswers): the SDK holds a request until its handler returns, so
// ending it would only lose the answer. One that hangs elsewhere holds its request until the platform's
// timeout (30 minutes on Cloud Run); its answer reaches the client if it comes before that. The deadline
// is one request's, that is one tool call's: it does not carry over to the requests a tool makes in turn.
// A client that goes away cancels nothing.
func Stateless(h http.Handler, maxRequest time.Duration) http.Handler {
	h = NoStandaloneStream(h)
	if maxRequest <= 0 {
		return h
	}
	return withDeadline(h, maxRequest)
}

type deadlineKey struct{}

// request is what Stateless records on a request: its deadline, and whether a tool call has started on it.
type request struct {
	at   time.Time
	mu   sync.Mutex
	tool bool // a tool call runs: the deadline no longer ends the request
	cut  bool // the deadline ended the request
}

// withDeadline gives the request its deadline d away and, at the deadline, ends it unless a tool call has
// started (see KeepToolAnswers): the safety net for a request the SDK holds with no tool running.
func withDeadline(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rq := &request{at: time.Now().Add(d)}
		ctx, cancel := context.WithCancel(context.WithValue(r.Context(), deadlineKey{}, rq))
		defer cancel()
		t := time.AfterFunc(d, func() {
			rq.mu.Lock()
			defer rq.mu.Unlock()
			if !rq.tool {
				rq.cut = true
				cancel()
			}
		})
		defer t.Stop()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// startTool marks ctx's request as serving a tool call; false if its deadline already ended it. It is true
// on a ctx from no such request.
func startTool(ctx context.Context) bool {
	rq, ok := ctx.Value(deadlineKey{}).(*request)
	if !ok {
		return true
	}
	rq.mu.Lock()
	defer rq.mu.Unlock()
	if rq.cut {
		return false
	}
	rq.tool = true
	return true
}

// KeepToolAnswers makes srv's tool calls outlast their request's deadline (see Stateless): the SDK holds the
// request until the tool returns anyway, and ending it at the deadline would only drop the answer, which an
// agent then retries, repeating what the tool did. A tool call that would start after the deadline is not run.
func KeepToolAnswers(srv *mcp.Server) {
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" && !startTool(ctx) {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{
					Text: "This request's time ran out before this tool call could run; it was not run."}}}, nil
			}
			return next(ctx, method, req)
		}
	})
}

// RequestDeadline is the deadline Stateless gave the request a tool handler serves: the SDK keeps the
// request's values on the handler's ctx, but not its deadline, so a tool reads it here and bounds by it
// only what may run long. ok is false without one (maxRequest <= 0, or a ctx from no such request).
// It is a value, so it survives context.WithoutCancel: don't make connector calls on a ctx detached
// from a tool call's.
func RequestDeadline(ctx context.Context) (time.Time, bool) {
	rq, ok := ctx.Value(deadlineKey{}).(*request)
	if !ok {
		return time.Time{}, false
	}
	return rq.at, true
}

// WithRequestDeadline records at as ctx's request deadline (see RequestDeadline).
func WithRequestDeadline(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, deadlineKey{}, &request{at: at})
}

// NoStandaloneStream answers GET with 405 Method Not Allowed in front of a stateless Streamable HTTP handler.
//
// A client opens a GET to receive messages the server sends on its own. A stateless server never sends any, but
// the SDK still holds that request open until the client goes away or the platform times it out (30 minutes on
// Cloud Run), and a request in progress is what Cloud Run bills: one MCP client left connected kept the service
// billed all day. The specification lets a server refuse the stream this way; clients then use plain requests.
func NoStandaloneStream(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "this MCP server sends no messages of its own: use POST", http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(w, r)
	})
}
