package e2e

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/store"
)

func init() {
	connector.Register(connector.Spec{Type: "fake_accounts_required", Kind: connector.KindPublisher, Name: "Fake accounts (required)",
		Accounts: true, AccountsRequired: true},
		func(connector.Config, connector.Env) (connector.Instance, error) { return accountsPublisher{}, nil })
}

// A post whose publisher needs accounts cannot be approved without one, from any entry point (#47): the
// single approve answers 400 and leaves it pending, bulk fails just that id.
func TestApproveRefusesPostItsPublisherCannotTake(t *testing.T) {
	e := setup(t)
	var pub store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Per account", "type": "fake_accounts_required", "config": map[string]any{}}, &pub); code != 201 {
		t.Fatalf("create connector: %d", code)
	}
	post := func(title string, accounts []string, publisher any) deliverable.Deliverable {
		var d deliverable.Deliverable
		payload := map[string]any{"content": title, "platforms": []string{"threads"}, "publisher_connector_id": publisher}
		if accounts != nil {
			payload["accounts"] = accounts
		}
		if code := e.do("POST", "/deliverables", map[string]any{"kind": "post", "title": title, "payload": payload, "submit": true}, &d); code != 201 {
			t.Fatalf("create %s: %d", title, code)
		}
		return d
	}

	none := post("no accounts", nil, pub.ID)
	var apiErr struct {
		Error struct{ Code, Message string }
	}
	if code := e.do("POST", "/deliverables/"+none.ID.String()+"/approve", nil, &apiErr); code != 400 || apiErr.Error.Code != "invalid_argument" {
		t.Fatalf("approve without accounts: %d %+v", code, apiErr)
	}
	var got deliverable.Deliverable
	if e.do("GET", "/deliverables/"+none.ID.String(), nil, &got); got.Status != deliverable.StatusPendingReview {
		t.Fatalf("refused approval moved it to %s", got.Status)
	}

	gone := post("deleted publisher", []string{"a1"}, uuid.New())
	if code := e.do("POST", "/deliverables/"+gone.ID.String()+"/approve", nil, &apiErr); code != 400 {
		t.Fatalf("approve with a missing publisher: %d %+v", code, apiErr)
	}

	// A disabled publisher, or a connector that is not a publisher, is refused too.
	var off store.Connector
	e.do("POST", "/connectors", map[string]any{"name": "Switched off", "type": "fake_accounts_required", "config": map[string]any{}}, &off)
	if code := e.do("PATCH", "/connectors/"+off.ID.String(), map[string]any{"is_active": false}, nil); code != 200 {
		t.Fatalf("disable connector: %d", code)
	}
	disabled := post("disabled publisher", []string{"a1"}, off.ID)
	if code := e.do("POST", "/deliverables/"+disabled.ID.String()+"/approve", nil, &apiErr); code != 400 || !strings.Contains(apiErr.Error.Message, "disabled") {
		t.Fatalf("approve with a disabled publisher: %d %+v", code, apiErr)
	}

	with := post("with accounts", []string{"a1"}, pub.ID)
	var res struct {
		OK     []uuid.UUID `json:"ok"`
		Failed []struct{ ID, Code, Message string }
	}
	if code := e.do("POST", "/deliverables/bulk", map[string]any{"action": "approve", "ids": []string{none.ID.String(), with.ID.String()}}, &res); code != 200 {
		t.Fatalf("bulk: %d", code)
	}
	if len(res.OK) != 1 || res.OK[0] != with.ID || len(res.Failed) != 1 || res.Failed[0].ID != none.ID.String() || res.Failed[0].Code != "invalid_argument" {
		t.Fatalf("bulk result: %+v", res)
	}

	// Once accounts are picked, the same post goes through.
	if code := e.do("PATCH", "/deliverables/"+none.ID.String(), map[string]any{"version": got.Version,
		"payload": map[string]any{"content": "no accounts", "platforms": []string{"threads"}, "publisher_connector_id": pub.ID, "accounts": []string{"a2"}}}, &got); code != 200 {
		t.Fatalf("pick accounts: %d", code)
	}
	if code := e.do("POST", "/deliverables/"+none.ID.String()+"/approve", nil, &got); code != 200 || got.Status != deliverable.StatusApproved {
		t.Fatalf("approve after picking accounts: %d %s", code, got.Status)
	}
}
