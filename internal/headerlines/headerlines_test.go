package headerlines

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	got, err := Parse("# Cloudflare Access\r\nCF-Access-Client-Id: id\n\n  X-Two :  a:b  \n")
	if err != nil {
		t.Fatal(err)
	}
	want := []Header{{"CF-Access-Client-Id", "id"}, {"X-Two", "a:b"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"ok: 1\nno colon s3cret", "ok: 1\n: s3cret"} {
		_, err := Parse(bad)
		if !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "line 2") || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
