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

// MaxRequestTime bounds one request to a stateless MCP server (see Stateless): longer than any tool call takes,
// well under Cloud Run's 30-minute request timeout. It is a last resort: a tool call cut off by it is canceled
// where it stands, and its client gets no answer.
const MaxRequestTime = 10 * time.Minute

// Stateless fronts a stateless Streamable HTTP handler: it refuses the GET stream (NoStandaloneStream) and
// cancels any request still open after MaxRequestTime, so no message shape can hold a request slot for good.
func Stateless(h http.Handler) http.Handler {
	return withDeadline(NoStandaloneStream(h), MaxRequestTime)
}

func withDeadline(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
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
