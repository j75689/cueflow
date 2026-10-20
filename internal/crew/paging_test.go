package crew

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A page with its head always fits what the model sees, so clipToolResult never cuts it: a long head
// shrinks the page, a very long one is clipped.
func TestPageTextFitsTheToolResult(t *testing.T) {
	text := []byte(strings.Repeat("字", 3*PageChars))
	for _, n := range []int{0, 10, 2100, pageHeadMax, 3 * pageHeadMax} {
		head := strings.Repeat("頭", n) + "\n"
		var got strings.Builder
		for off := 0; ; {
			page, err := PageText(head, text, off, 0, "t", "x")
			if err != nil {
				t.Fatal(err)
			}
			if clipToolResult(page) != page {
				t.Fatalf("head %d, offset %d: the page (%d characters) would be clipped", n, off, utf8.RuneCountInString(page))
			}
			status, body, _ := strings.Cut(page[strings.LastIndex(page, "characters "):], "\n\n")
			got.WriteString(body)
			if !strings.Contains(status, "Call t with offset") {
				break
			}
			off += utf8.RuneCountInString(body)
		}
		if got.String() != string(text) {
			t.Fatalf("head %d: the pages don't add up to the text", n)
		}
	}
	if _, err := PageText("", text, -1, 0, "t", "x"); err == nil {
		t.Fatal("a negative offset was accepted")
	}
	if _, err := PageText("", []byte("abc"), 3, 0, "t", "x"); err == nil || !strings.Contains(err.Error(), "past the end of x, which has 3 characters") {
		t.Fatalf("offset past the end: %v", err)
	}
	if page, err := PageText("h: ", nil, 0, 0, "t", "x"); err != nil || page != "h: characters 0–0 of 0.\n\n" {
		t.Fatalf("empty text: %q %v", page, err)
	}
}
