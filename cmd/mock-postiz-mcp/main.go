// Command mock-postiz-mcp is a stand-in for the Postiz MCP server, for local development and demos.
// It speaks the same tools as Postiz (see internal/postizmock) and records posts at GET /posts.
//
//	MOCK_POSTIZ_ADDR      listen address (default :4007)
//	MOCK_POSTIZ_API_KEY   expected bearer token (default dev-key)
//	MOCK_POSTIZ_FAIL      if "true", publishing returns a Postiz-style validation error
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/j75689/cueflow/internal/postizmock"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	addr := env("MOCK_POSTIZ_ADDR", ":4007")
	srv := postizmock.New(env("MOCK_POSTIZ_API_KEY", "dev-key"))
	srv.Fail.Store(os.Getenv("MOCK_POSTIZ_FAIL") == "true")
	srv.Logf = log.Printf
	log.Printf("mock Postiz MCP listening on %s (MCP endpoint /api/mcp, received posts at /posts)", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
