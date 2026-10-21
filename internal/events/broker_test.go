package events

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// An in-process broker hands the org along; For lets a subscriber keep its own org's events and the global ones.
func TestPublishCarriesOrg(t *testing.T) {
	b := NewBroker()
	ch, unsub := b.Subscribe()
	defer unsub()
	mine, other := uuid.New(), uuid.New()
	b.Publish(mine, "task.updated", map[string]any{"id": "a"})
	b.Publish(other, "task.updated", map[string]any{"id": "b"})
	b.Publish(uuid.Nil, "task.updated", map[string]any{"id": "lost"}) // unknown org: dropped, not global
	b.PublishGlobal("connector.updated", map[string]any{"id": "c"})
	var got []string
	for range 3 {
		var ev Event
		select {
		case ev = <-ch:
		case <-time.After(time.Second):
			t.Fatalf("only %d events arrived", len(got))
		}
		if ev.Data.(map[string]any)["id"] == "lost" {
			t.Fatal("an event with no org must be dropped")
		}
		if ev.Org != "" && ev.Data.(map[string]any)["org_id"] != ev.Org {
			t.Fatalf("org_id missing from %+v", ev)
		}
		if ev.For(mine.String()) {
			got = append(got, ev.Name+"/"+ev.Org)
		}
		if !ev.For("") {
			t.Fatalf("an unfiltered subscriber must get every event, not %+v", ev)
		}
	}
	want := []string{"task.updated/" + mine.String(), "connector.updated/"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
