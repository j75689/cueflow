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
		t.Fatalf("got ...%q", got[len(got)-80:])
	}
}
