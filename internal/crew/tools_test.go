package crew

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/j75689/cueflow/internal/llm"
)

func TestClipToolResult(t *testing.T) {
	short := strings.Repeat("字", maxToolResult)
	if got := clipToolResult(short); got != short {
		t.Fatal("a result at the limit was cut")
	}
	long := strings.Repeat("字", maxToolResult+5)
	got := clipToolResult(long)
	want := fmt.Sprintf("… (truncated: showed the first %d of %d characters)", maxToolResult, maxToolResult+5)
	if !strings.HasSuffix(got, want) || !strings.HasPrefix(got, short) {
		r := []rune(got)
		t.Fatalf("got ...%q", string(r[len(r)-80:]))
	}
}

// Past the budget, the oldest big results are cut with a note, oldest first, until the history fits;
// small ones, the assistant's messages and the newest turn's results stay whole.
func TestTrimToolHistory(t *testing.T) {
	big := func(c string) string { return strings.Repeat(c, maxToolResult) }
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "brief"},
		{Role: llm.RoleAssistant, Content: big("a")},
		{Role: llm.RoleTool, Content: big("1")},
		{Role: llm.RoleTool, Content: "small"},
		{Role: llm.RoleAssistant},
		{Role: llm.RoleTool, Content: big("2")},
		{Role: llm.RoleAssistant},
		{Role: llm.RoleTool, Content: big("3")},
	}
	trimToolHistory(msgs)
	for i, c := range map[int]string{2: "1", 5: "2"} {
		if !strings.HasPrefix(msgs[i].Content, strings.Repeat(c, toolHistoryKept)+"… (truncated: showed the first 2000 of 32000 characters; cut from the history") {
			t.Fatalf("result %d not cut: %q", i, msgs[i].Content[toolHistoryKept-10:])
		}
	}
	if msgs[1].Content != big("a") || msgs[3].Content != "small" || msgs[7].Content != big("3") {
		t.Fatal("cut what should stay whole")
	}

	// A second pass changes nothing: already-cut results are left alone.
	msgs = append(msgs, llm.Message{Role: llm.RoleAssistant}, llm.Message{Role: llm.RoleTool, Content: "x"})
	before := msgs[2].Content
	trimToolHistory(msgs)
	if msgs[2].Content != before || msgs[7].Content != big("3") {
		t.Fatal("an already-cut result was cut again")
	}
	total := 0
	for _, m := range msgs {
		if m.Role == llm.RoleTool {
			total += utf8.RuneCountInString(m.Content)
		}
	}
	if total > toolHistoryBudget {
		t.Fatalf("history still carries %d characters", total)
	}
}
