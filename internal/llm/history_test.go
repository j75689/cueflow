package llm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Past the budget, the oldest big results are cut with a note, oldest first, until they are under
// half of it; small ones, the assistant's messages and the newest turn's results stay whole. The
// assistant turns produced before a cut among the turns they had seen are reported stale. The input
// is never changed, and a later turn sends the earlier ones exactly as before.
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
	out, stale := TrimToolHistory(msgs, budget)
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
	// Result 2 was cut by the request that produced message 6, so message 4 is stale; result 5 by
	// this request, so message 6 is too. Message 1 saw nothing that was cut.
	if stale[1] || !stale[4] || !stale[6] {
		t.Fatalf("stale: %v", stale)
	}

	// The next turn adds little: nothing new is cut, and earlier turns go out exactly as before.
	msgs = append(msgs, Message{Role: RoleAssistant}, Message{Role: RoleTool, Content: "x"})
	again, staleAgain := TrimToolHistory(msgs, budget)
	for i := range out {
		if again[i].Content != out[i].Content || staleAgain[i] != stale[i] {
			t.Fatalf("message %d changed between turns", i)
		}
	}
	if staleAgain[8] {
		t.Fatal("a turn produced after the last cut is not stale")
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

	if got, s := TrimToolHistory(msgs, 0); &got[0] != &msgs[0] || s != nil {
		t.Fatal("no budget should leave the history as is")
	}
	if got, s := TrimToolHistory(msgs[:2], budget); &got[0] != &msgs[0] || s != nil {
		t.Fatal("a history under budget should be left as is")
	}
}

func TestClipNotedCountsCharacters(t *testing.T) {
	if got := ClipNoted("字字字", 3, ""); got != "字字字" {
		t.Fatalf("cut at the limit: %q", got)
	}
	if got := ClipNoted("字字字字", 2, "!"); got != "字字… (truncated: showed the first 2 of 4 characters!)" {
		t.Fatalf("got %q", got)
	}
}
