package mcpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNoStandaloneStream(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, struct{}{}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(NoStandaloneStream(h))
	defer ts.Close()

	// GET is refused at once, not held open
	start := time.Now()
	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.Header.Set("Accept", "text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed || time.Since(start) > 2*time.Second {
		t.Fatalf("GET: %d after %s", res.StatusCode, time.Since(start))
	}

	// an MCP client still works: it connects, lists and calls tools over POST
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ping"})
	if err != nil || out.Content[0].(*mcp.TextContent).Text != "pong" {
		t.Fatalf("call: %v %v", out, err)
	}
}
