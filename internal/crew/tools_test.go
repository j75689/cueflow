package crew

import (
	"fmt"
	"strings"
	"testing"
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

// An overflow on the first turn isn't fixed by a fresh run, so it doesn't suggest one.
func TestOverflowMessage(t *testing.T) {
	if m := overflowMessage(0); strings.Contains(m, "Reply to continue") || !strings.Contains(m, "max output tokens") {
		t.Fatalf("turn 0: %s", m)
	}
	if m := overflowMessage(12); !strings.Contains(m, "after 12 turns") || !strings.Contains(m, "Reply to continue") {
		t.Fatalf("turn 12: %s", m)
	}
}
