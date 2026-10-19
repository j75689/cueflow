package mcpx

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A tool that answers the way GitHub's get_file_contents does: a status line as text and the file
// as an embedded resource. CallText must hand the agent the file, not just the status line.
func TestCallTextKeepsEmbeddedResources(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_file_contents"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: "successfully downloaded text file (SHA: abc)"},
			&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "repo://o/r/contents/src/sites.js", MIMEType: "text/javascript", Text: "const NETFLIX = {};"}},
			&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "repo://o/r/contents/icon.png", MIMEType: "image/png", Blob: []byte{1, 2, 3}}},
			&mcp.ResourceLink{URI: "https://example.com/raw", Name: "raw"},
		}}, struct{}{}, nil
	})
	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	text, isErr, err := CallText(ctx, cs, "get_file_contents", nil)
	if err != nil || isErr {
		t.Fatalf("call: %v %v", isErr, err)
	}
	for _, want := range []string{
		"successfully downloaded text file (SHA: abc)",
		"--- repo://o/r/contents/src/sites.js (text/javascript)\nconst NETFLIX = {};",
		"--- repo://o/r/contents/icon.png (image/png): binary, 3 bytes, not shown",
		"--- link: https://example.com/raw",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}
