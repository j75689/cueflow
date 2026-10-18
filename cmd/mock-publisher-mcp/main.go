// Command mock-publisher-mcp is a publisher MCP server that publishes nowhere, for local development
// and demos. It follows the publisher contract (docs/publisher-mcp.md), so CueFlow's "MCP publisher"
// connector drives it like a real one; received posts are listed at GET /posts.
//
//	MOCK_PUBLISHER_ADDR    listen address (default :4010)
//	MOCK_PUBLISHER_TOKEN   expected bearer token (default dev-token; empty disables the check)
//	MOCK_PUBLISHER_FAIL    if "true", create_post refuses every post, like a publisher that is down
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/j75689/cueflow/internal/publishermock"
)

func main() {
	addr := os.Getenv("MOCK_PUBLISHER_ADDR")
	if addr == "" {
		addr = ":4010"
	}
	token, set := os.LookupEnv("MOCK_PUBLISHER_TOKEN")
	if !set {
		token = "dev-token"
	}
	srv := publishermock.New(token)
	srv.Fail.Store(os.Getenv("MOCK_PUBLISHER_FAIL") == "true")
	srv.Logf = log.Printf
	log.Printf("mock publisher MCP listening on %s (MCP endpoint /mcp, received posts at /posts)", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
