package mcpaction

import (
	"encoding/json"
	"testing"
)

func TestParseResult(t *testing.T) {
	r := parseResult(json.RawMessage(`{"external_ids":{"page":"p1"},"details":{"url":"https://x"}}`))
	if r.ExternalIDs["page"] != "p1" || r.Details["url"] != "https://x" {
		t.Fatalf("contract shape: %+v", r)
	}
	r = parseResult(json.RawMessage(`{"id":"42","status":"filed"}`))
	if r.ExternalIDs["id"] != "42" || r.Details["status"] != "filed" {
		t.Fatalf("plain object: %+v", r)
	}
	r = parseResult(json.RawMessage(`"ok"`))
	if len(r.ExternalIDs) != 0 || r.Details["reply"] != `"ok"` {
		t.Fatalf("text reply: %+v", r)
	}
}
