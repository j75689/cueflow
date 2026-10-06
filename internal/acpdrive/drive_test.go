package acpdrive

import (
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

func TestAllowedTool(t *testing.T) {
	cases := []struct {
		name     string
		title    string
		kind     acp.ToolKind
		allowWeb bool
		want     bool
	}{
		{"claude style", "mcp__cueflow__complete_task", acp.ToolKindOther, false, true},
		{"dotted", "cueflow.create_post_draft", acp.ToolKindOther, false, true},
		{"antigravity underscore", "cueflow_complete_task", acp.ToolKindOther, false, true},
		{"underscore tool claiming to execute is refused", "cueflow_run", acp.ToolKindExecute, false, false},
		{"underscore tool claiming to edit is refused", "cueflow_patch", acp.ToolKindEdit, false, false},
		{"shell", "ls -la", acp.ToolKindExecute, true, false},
		{"file read", "Running view_file", acp.ToolKindRead, true, false},
		{"web fetch when allowed", "fetch https://example.com", acp.ToolKindFetch, true, true},
		{"web fetch when not allowed", "fetch https://example.com", acp.ToolKindFetch, false, false},
		{"thinking", "thinking", acp.ToolKindThink, false, true},
		{"lookalike prefix", "mycueflow_complete_task", acp.ToolKindOther, false, false},
	}
	for _, c := range cases {
		if got := allowedTool(c.title, c.kind, c.allowWeb); got != c.want {
			t.Errorf("%s: allowedTool(%q, %s) = %v, want %v", c.name, c.title, c.kind, got, c.want)
		}
	}
}
