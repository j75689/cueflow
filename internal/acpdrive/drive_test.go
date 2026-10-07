package acpdrive

import (
	"strings"
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
		if got := allowedTool(c.title, c.kind, c.allowWeb, false); got != c.want {
			t.Errorf("%s: allowedTool(%q, %s) = %v, want %v", c.name, c.title, c.kind, got, c.want)
		}
	}
}

// A machine whose owner allowed the shell lets the agent use its own tools; nothing else changes.
func TestAllowedToolOnAMachineThatAllowsTheShell(t *testing.T) {
	for _, c := range []struct {
		title string
		kind  acp.ToolKind
	}{
		{"git push origin agent/x", acp.ToolKindExecute},
		{"Edit main.go", acp.ToolKindEdit},
		{"Read main.go", acp.ToolKindRead},
		{"Delete tmp", acp.ToolKindDelete},
		{"mcp__cueflow__complete_task", acp.ToolKindOther},
	} {
		if !allowedTool(c.title, c.kind, false, true) {
			t.Errorf("%q (%s) must be allowed when the machine allows the shell", c.title, c.kind)
		}
	}
}

func TestHostPrompt(t *testing.T) {
	p := "intro\n" + ShellRule + "\nrest"
	if got := hostPrompt(p, false); got != p {
		t.Errorf("without the shell the prompt must not change:\n%s", got)
	}
	got := hostPrompt(p, true)
	if strings.Contains(got, "will be refused") || !strings.Contains(got, ShellRuleAllowed) || !strings.HasSuffix(got, "\nrest") {
		t.Errorf("with the shell the rule must be swapped:\n%s", got)
	}
}

func TestModelSelection(t *testing.T) {
	ungrouped := acp.SessionConfigSelectOptionsUngrouped{
		{Value: "gemini-pro-agent", Name: "Gemini 3.1 Pro (High)"},
		{Value: "gemini-3.7-flash-low", Name: "Gemini 3.7 Flash (Low)"},
	}
	cat := acp.SessionConfigOptionCategoryModel
	mode := acp.SessionConfigOptionCategoryMode
	opts := []acp.SessionConfigOption{
		{Select: &acp.SessionConfigOptionSelect{Id: "mode", Category: &mode}},
		{Select: &acp.SessionConfigOptionSelect{Id: "model", Category: &cat, Options: acp.SessionConfigSelectOptions{Ungrouped: &ungrouped}}},
	}
	id, choices, ok := modelOption(opts)
	if !ok || id != "model" || len(choices) != 2 {
		t.Fatalf("modelOption: %q %v %v", id, choices, ok)
	}
	for want, expect := range map[string]string{
		"gemini-pro-agent":       "gemini-pro-agent",
		"GEMINI-PRO-AGENT":       "gemini-pro-agent",
		"Gemini 3.7 Flash (Low)": "gemini-3.7-flash-low",
	} {
		if got, found := pickModel(choices, want); !found || got != expect {
			t.Errorf("pickModel(%q) = %q, %v; want %q", want, got, found, expect)
		}
	}
	if _, found := pickModel(choices, "gpt-5"); found {
		t.Error("an unknown model must not match")
	}
	if _, _, ok := modelOption([]acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{Id: "mode", Category: &mode}}}); ok {
		t.Error("an agent without a model option must report so")
	}
}
