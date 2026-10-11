package mcpx

import "net/http"

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
