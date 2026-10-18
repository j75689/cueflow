package deliverable

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/domain"
)

func TestStateMachine(t *testing.T) {
	for from, tos := range Transitions() {
		for _, to := range tos {
			if err := CheckTransition(from, to); err != nil {
				t.Errorf("%s -> %s should be allowed: %v", from, to, err)
			}
		}
	}
	var te *domain.TransitionError
	if err := CheckTransition(StatusPendingReview, StatusDone); !errors.As(err, &te) || te.From != "PENDING_REVIEW" {
		t.Fatalf("skipping approval must be refused: %v", err)
	}
	if err := CheckTransition(StatusDraft, StatusApproved); err == nil {
		t.Fatal("a draft cannot be approved without review")
	}
	for _, terminal := range []Status{StatusDone, StatusRejected} {
		if len(Transitions()[terminal]) != 0 {
			t.Errorf("%s must be terminal", terminal)
		}
	}
	if !StatusPendingReview.Editable() || StatusApproved.Editable() || !StatusRejected.Removable() || StatusDone.Removable() {
		t.Fatal("editable / removable flags")
	}
}

func TestKindsAndPayloads(t *testing.T) {
	if _, err := validatePayload("document", json.RawMessage(`{"content":"# hi"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePayload("document", json.RawMessage(`{"content":"  "}`)); err == nil || !strings.Contains(err.Error(), "content is required") {
		t.Fatalf("empty document: %v", err)
	}
	if _, err := validatePayload("document", json.RawMessage(`[1,2]`)); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("non-object payload: %v", err)
	}
	if _, err := validatePayload("nope", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "registered: document") {
		t.Fatalf("unknown kind: %v", err)
	}
	RegisterKind(Kind{ID: "ticket", Name: "Ticket"})
	if _, err := validatePayload("ticket", nil); err != nil {
		t.Fatalf("a kind without Validate accepts any object: %v", err)
	}
	ids := kindIDs()
	if len(ids) != 2 || ids[0] != "document" || ids[1] != "ticket" {
		t.Fatalf("kinds: %v", ids)
	}
}
