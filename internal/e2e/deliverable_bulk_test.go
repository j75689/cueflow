package e2e

import (
	"testing"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/deliverable"
)

// POST /deliverables/bulk moves many items in one request, each on its own: the ones another
// reviewer already moved (or that do not exist) fail alone with the single endpoint's code, the
// rest go through, and the response says which was which.
func TestDeliverableBulk(t *testing.T) {
	e := setup(t)
	var docs []deliverable.Deliverable
	for _, title := range []string{"a", "b", "c"} {
		var d deliverable.Deliverable
		if code := e.do("POST", "/deliverables", map[string]any{"kind": "document", "title": title,
			"payload": map[string]any{"content": "x"}, "submit": true}, &d); code != 201 {
			t.Fatalf("create %s: %d", title, code)
		}
		docs = append(docs, d)
	}
	// c is rejected by hand first, so the bulk approve finds it already moved.
	if code := e.do("POST", "/deliverables/"+docs[2].ID.String()+"/reject", map[string]any{"reason": "no"}, nil); code != 200 {
		t.Fatalf("reject c: %d", code)
	}

	var res struct {
		OK     []uuid.UUID `json:"ok"`
		Failed []struct {
			ID, Code, Message string
		} `json:"failed"`
	}
	ids := []string{docs[0].ID.String(), docs[1].ID.String(), docs[1].ID.String(), docs[2].ID.String(), uuid.NewString(), "not-a-uuid"}
	if code := e.do("POST", "/deliverables/bulk", map[string]any{"action": "approve", "ids": ids, "note": "lgtm"}, &res); code != 200 {
		t.Fatalf("bulk approve: %d", code)
	}
	if len(res.OK) != 2 || res.OK[0] != docs[0].ID || res.OK[1] != docs[1].ID {
		t.Fatalf("ok: %v", res.OK)
	}
	if len(res.Failed) != 3 || res.Failed[0].ID != docs[2].ID.String() || res.Failed[0].Code != "invalid_transition" ||
		res.Failed[1].Code != "not_found" || res.Failed[2].ID != "not-a-uuid" || res.Failed[2].Code != "not_found" {
		t.Fatalf("failed: %+v", res.Failed)
	}
	var got deliverable.Deliverable
	for _, d := range docs[:2] {
		if e.do("GET", "/deliverables/"+d.ID.String(), nil, &got); got.Status != deliverable.StatusApproved && got.Status != deliverable.StatusQueued {
			t.Fatalf("%s after bulk approve: %s", d.Title, got.Status)
		}
	}
	var audit []deliverable.AuditLog
	if e.do("GET", "/deliverables/"+docs[0].ID.String()+"/audit", nil, &audit); len(audit) < 2 || audit[len(audit)-1].Action != deliverable.ActionApproved || audit[len(audit)-1].Note != "lgtm" {
		t.Fatalf("audit: %+v", audit)
	}

	// Reject keeps the reason as the review note. A second bulk over the same ids fails them all,
	// still with 200: the caller reads the per-id results.
	var d deliverable.Deliverable
	if code := e.do("POST", "/deliverables", map[string]any{"kind": "document", "title": "d",
		"payload": map[string]any{"content": "x"}, "submit": true}, &d); code != 201 {
		t.Fatalf("create d: %d", code)
	}
	if code := e.do("POST", "/deliverables/bulk", map[string]any{"action": "reject", "ids": []string{d.ID.String()}, "reason": "off brand"}, &res); code != 200 || len(res.OK) != 1 || len(res.Failed) != 0 {
		t.Fatalf("bulk reject: %d %+v", code, res)
	}
	if e.do("GET", "/deliverables/"+d.ID.String(), nil, &got); got.Status != deliverable.StatusRejected || got.ReviewNote != "off brand" {
		t.Fatalf("after bulk reject: %+v", got)
	}
	if code := e.do("POST", "/deliverables/bulk", map[string]any{"action": "reject", "ids": []string{d.ID.String()}}, &res); code != 200 || len(res.OK) != 0 || len(res.Failed) != 1 {
		t.Fatalf("second bulk reject: %d %+v", code, res)
	}

	for name, body := range map[string]map[string]any{
		"no action":   {"ids": []string{d.ID.String()}},
		"bad action":  {"action": "submit", "ids": []string{d.ID.String()}},
		"no ids":      {"action": "approve", "ids": []string{}},
		"too many":    {"action": "approve", "ids": make([]string, 201)},
		"ids missing": {"action": "approve"},
	} {
		if code := e.do("POST", "/deliverables/bulk", body, nil); code != 400 {
			t.Fatalf("%s: %d", name, code)
		}
	}
}
