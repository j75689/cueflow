package agent

import (
	"reflect"
	"testing"
)

func TestNormalizeKeepsTheFirstPartGivenAsContent(t *testing.T) {
	cases := []struct {
		name  string
		draft Draft
		want  []string
	}{
		{
			name:  "first part in content, the reply in thread_parts",
			draft: Draft{Platform: "threads", Content: "Seven words for the checkout", ThreadParts: []string{"A short dialogue"}},
			want:  []string{"Seven words for the checkout", "A short dialogue"},
		},
		{
			name:  "the whole thread in thread_parts, content repeating its first part",
			draft: Draft{Platform: "threads", Content: "Part one", ThreadParts: []string{"Part one", "Part two"}},
			want:  []string{"Part one", "Part two"},
		},
		{
			name:  "the whole thread in thread_parts, content the thread joined",
			draft: Draft{Platform: "threads", Content: "Part one\n\nPart two", ThreadParts: []string{"Part one", "Part two"}},
			want:  []string{"Part one", "Part two"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Normalize([]Draft{c.draft}, []string{"threads"})
			if len(got) != 1 || !reflect.DeepEqual(got[0].ThreadParts, c.want) {
				t.Fatalf("parts = %#v, want %#v", got[0].ThreadParts, c.want)
			}
		})
	}
}
