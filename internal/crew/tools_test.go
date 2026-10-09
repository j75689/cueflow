package crew

import (
	"reflect"
	"testing"
)

func TestCleanMediaURLs(t *testing.T) {
	got, err := cleanMediaURLs([]string{
		" /media/e3e8c5e8-88bf-4917-b0a8-c6ff2ecac11b.mp4 ", "", "https://cdn.example.com/a.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/media/e3e8c5e8-88bf-4917-b0a8-c6ff2ecac11b.mp4", "https://cdn.example.com/a.png"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	for _, bad := range []string{"http://example.com/a.png", "/etc/passwd", "/media/not-an-id", "file:///x"} {
		if _, err := cleanMediaURLs([]string{bad}); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}

	if got, err := cleanMediaURLs(nil); err != nil || len(got) != 0 {
		t.Fatalf("an empty list is allowed (it removes the media): %v %v", got, err)
	}
}
