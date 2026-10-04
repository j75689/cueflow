package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitThreadShortTextUnchanged(t *testing.T) {
	got := SplitThread("  hello world  ", 500)
	if len(got) != 1 || got[0] != "hello world" {
		t.Fatalf("got %q", got)
	}
	if SplitThread("   ", 10) != nil {
		t.Fatal("blank text should yield nil")
	}
}

func TestSplitThreadRespectsLimit(t *testing.T) {
	cases := map[string]string{
		"latin":      strings.Repeat("This is a sentence that goes on. ", 60),
		"cjk":        strings.Repeat("這是一個關於人工智慧代理的句子。", 80),
		"paragraphs": strings.Repeat("Paragraph text here.\n\n", 50),
		"no breaks":  strings.Repeat("x", 1234),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			parts := SplitThread(text, 100)
			if len(parts) < 2 {
				t.Fatalf("expected multiple parts, got %d", len(parts))
			}
			total := 0
			for i, p := range parts {
				if n := utf8.RuneCountInString(p); n > 100 || n == 0 {
					t.Errorf("part %d has %d runes", i, n)
				}
				total += utf8.RuneCountInString(strings.Join(strings.Fields(p), ""))
			}
			// Nothing but whitespace may be lost.
			want := utf8.RuneCountInString(strings.Join(strings.Fields(text), ""))
			if total != want {
				t.Errorf("content lost: got %d runes, want %d", total, want)
			}
		})
	}
}

func TestSplitThreadPrefersSentenceBoundaries(t *testing.T) {
	parts := SplitThread("第一句話。第二句話比較長一點。第三句。", 10)
	for _, p := range parts {
		if !strings.HasSuffix(p, "。") {
			t.Errorf("part %q does not end at a sentence boundary", p)
		}
	}
}

func TestFitParts(t *testing.T) {
	got := FitParts([]string{"ok", "", strings.Repeat("a ", 80)}, 50)
	if got[0] != "ok" {
		t.Fatalf("first part changed: %q", got[0])
	}
	if len(got) < 3 {
		t.Fatalf("long part not split: %q", got)
	}
}

func TestToHTMLParagraphs(t *testing.T) {
	got := ToHTMLParagraphs("a <b>\n\nc")
	want := "<p>a &lt;b&gt;</p><p></p><p>c</p>"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
