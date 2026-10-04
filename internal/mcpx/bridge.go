package mcpx

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Bridge serves, on stdio, every tool of the Streamable HTTP MCP server at url, forwarding calls.
// It lets agents that only launch stdio MCP servers reach CueFlow's per-run tool endpoint.
func Bridge(ctx context.Context, url string) error {
	remote, err := Connect(ctx, Options{Transport: TransportStreamable, Endpoint: url, HTTPClient: &http.Client{}})
	if err != nil {
		return err
	}
	defer remote.Close()
	tools, err := ListTools(ctx, remote)
	if err != nil {
		return err
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "cueflow-bridge", Version: "0.1.0"}, nil)
	for _, t := range tools {
		name := t.Name
		srv.AddTool(t, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			res, err := remote.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: req.Params.Arguments})
			if err != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("bridge: %v", err)}}}, nil
			}
			return res, nil
		})
	}
	return srv.Run(ctx, &mcp.StdioTransport{})
}
