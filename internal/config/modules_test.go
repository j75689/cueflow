package config

import (
	"strings"
	"testing"
)

func TestParseModules(t *testing.T) {
	for raw, want := range map[string]string{"": "content,deliverables", "all": "content,deliverables", "none": "",
		" deliverables ": "deliverables", "content, Content,deliverables": "content,deliverables"} {
		got, err := parseModules(raw)
		if err != nil || strings.Join(got, ",") != want {
			t.Errorf("%q: got %v %v, want %s", raw, got, err, want)
		}
	}
	if _, err := parseModules("content,posts"); err == nil || !strings.Contains(err.Error(), "unknown module") {
		t.Fatalf("unknown module accepted: %v", err)
	}
	c := &Config{Modules: []string{"deliverables"}}
	if c.Enabled("content") || !c.Enabled("deliverables") {
		t.Fatal("Enabled")
	}
}
