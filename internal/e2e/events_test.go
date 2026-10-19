package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/store"
)

// A dashboard tab watches one org: /events?org= sends that org's events and the global ones, not other orgs'.
func TestEventsFollowTheOrg(t *testing.T) {
	e := setup(t)
	var dev store.Org
	if code := e.do("POST", "/orgs", map[string]any{"name": "Dev"}, &dev); code != 201 {
		t.Fatalf("create org: %d", code)
	}
	if code := e.do("GET", "/events?org=nope", nil, nil); code != 400 {
		t.Fatalf("unknown org: %d", code)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.api.URL+"/api/v1/events?org="+dev.Slug, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	type sse struct {
		name string
		data map[string]any
	}
	got := make(chan sse, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && name != "":
				var d map[string]any
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d)
				got <- sse{name, d}
				name = ""
			}
		}
	}()
	time.Sleep(300 * time.Millisecond) // let the stream subscribe

	var other, mine store.Task
	e.do("POST", "/tasks", map[string]any{"title": "default org", "status": "BACKLOG"}, &other)
	e.do("POST", "/tasks?org="+dev.Slug, map[string]any{"title": "dev org", "status": "BACKLOG"}, &mine)
	e.do("PATCH", "/orgs/"+dev.ID.String(), map[string]any{"name": "Dev team"}, nil)

	want := []string{"task.updated " + mine.ID.String(), "org.updated " + dev.ID.String()}
	for i, w := range want {
		select {
		case ev := <-got:
			if ev.name+" "+ev.data["id"].(string) != w {
				t.Fatalf("event %d: got %s %v, want %s (the default org's task must not arrive)", i, ev.name, ev.data, w)
			}
			if ev.name == "task.updated" && ev.data["org_id"] != dev.ID.String() {
				t.Fatalf("task event org_id: %v", ev.data["org_id"])
			}
		case <-ctx.Done():
			t.Fatalf("waiting for %s", w)
		}
	}
}
