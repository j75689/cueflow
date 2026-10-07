package media

import (
	"bytes"
	"strings"
	"testing"
)

var mp4Head = append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00"), bytes.Repeat([]byte{0}, 32)...)

func TestSniffHead(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRfake")
	cases := []struct {
		name    string
		head    []byte
		size    int64
		want    string
		wantErr string
	}{
		{"png", png, int64(len(png)), "image/png", ""},
		{"mp4", mp4Head, 5 << 20, "video/mp4", ""},
		{"empty", nil, 0, "", "empty"},
		{"zero size", png, 0, "", "empty"},
		{"text", []byte("hello, not media"), 16, "", "unsupported"},
		{"huge image", png, MaxImageBytes + 1, "", "larger"},
		{"huge video", mp4Head, MaxVideoBytes + 1, "", "larger"},
	}
	for _, c := range cases {
		got, err := SniffHead(c.head, c.size)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v", c.name, got, err)
		}
	}
}
