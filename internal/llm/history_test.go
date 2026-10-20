package llm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Past the budget, the oldest big results are cut with a note, oldest first, until the history fits;
// small ones, the assistant's messages and the newest turn's results stay whole. The input is never
// changed, and the same history always comes out cut the same way.
func TestTrimToolHistory(t *testing.T) {
	const budget, size = 64000, 32000
	big := func(c string) string { return strings.Repeat(c, size) }
	msgs := []Message{
		{Role: RoleUser, Content: "brief"},
		{Role: RoleAssistant, Content: big("a")},
		{Role: RoleTool, Content: big("1")},
		{Role: RoleTool, Content: "small"},
		{Role: RoleAssistant},
		{Role: RoleTool, Content: big("2")},
		{Role: RoleAssistant},
		{Role: RoleTool, Content: big("3")},
	}
	out := TrimToolHistory(msgs, budget)
	for i, c := range map[int]string{2: "1", 5: "2"} {
		if !strings.HasPrefix(out[i].Content, strings.Repeat(c, ToolHistoryKept)+"… (truncated: showed the first 2000 of 32000 characters; cut from the history") {
			t.Fatalf("result %d not cut: %q", i, out[i].Content[ToolHistoryKept-10:])
		}
	}
	if out[1].Content != big("a") || out[3].Content != "small" || out[7].Content != big("3") {
		t.Fatal("cut what should stay whole")
	}
	if msgs[2].Content != big("1") || msgs[5].Content != big("2") {
		t.Fatal("the caller's history was changed")
	}

	// A later turn cuts the earlier results exactly as before.
	msgs = append(msgs, Message{Role: RoleAssistant}, Message{Role: RoleTool, Content: "x"})
	again := TrimToolHistory(msgs, budget)
	if again[2].Content != out[2].Content || again[5].Content != out[5].Content {
		t.Fatal("an earlier result was cut differently")
	}
	total := 0
	for _, m := range again {
		if m.Role == RoleTool {
			total += utf8.RuneCountInString(m.Content)
		}
	}
	if total > budget {
		t.Fatalf("history still carries %d characters", total)
	}

	if got := TrimToolHistory(msgs, 0); &got[0] != &msgs[0] {
		t.Fatal("no budget should leave the history as is")
	}
}
