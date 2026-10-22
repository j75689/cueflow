package mcpx

import (
	"context"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// StatelessCapabilities are what a stateless server advertises: tools, but no list-changed notifications.
//
// The SDK advertises tools.listChanged as soon as a server has a tool. A client on protocol 2026-07-28 then opens
// subscriptions/listen, a POST the SDK holds open until the client goes away, as no other request can reach it to
// end it. A stateless server never sends those notifications, and on Cloud Run the POST held a request slot for
// 30 minutes; enough of them used up the instances. Without the capability the client opens no listen, and one it
// opens anyway is answered at once.
func StatelessCapabilities() *mcp.ServerCapabilities {
	return &mcp.ServerCapabilities{Logging: &mcp.LoggingCapabilities{}, Tools: &mcp.ToolCapabilities{}}
}

// Stateless fronts a stateless Streamable HTTP handler: it refuses the GET stream (NoStandaloneStream) and
// gives every request a deadline maxRequest away (CUEFLOW_MCP_REQUEST_TIMEOUT), so no message shape can hold a
// request slot for good. maxRequest <= 0 sets no deadline.
//
// The SDK runs handlers on a ctx detached from the request's (it neither ends nor has a deadline), and the
// request lasts until its handler returns. A tool handler that takes the deadline back with RequestContext
// (run and agent-token tool calls) is canceled where it stands when it passes; others (the operator's /mcp
// tools) run to the end. A client that goes away cancels nothing: a tool call is not stopped halfway for it.
func Stateless(h http.Handler, maxRequest time.Duration) http.Handler {
	h = NoStandaloneStream(h)
	if maxRequest <= 0 {
		return h
	}
	return withDeadline(h, maxRequest)
}

type deadlineKey struct{}

func withDeadline(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		at := time.Now().Add(d)
		ctx, cancel := context.WithDeadline(context.WithValue(r.Context(), deadlineKey{}, at), at)
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestContext is a tool handler's ctx with its request's deadline (see Stateless) put back: the SDK
// hands handlers a ctx that keeps the request's values but not its deadline. Without one it only adds
// a cancel.
func RequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if at, ok := ctx.Value(deadlineKey{}).(time.Time); ok {
		return context.WithDeadline(ctx, at)
	}
	return context.WithCancel(ctx)
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
