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
	sc := bufio.NewScanner(resp.Body)
	waitConnected(t, sc)
	go func() {
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
	var other, mine store.Task
	e.do("POST", "/tasks", map[string]any{"title": "default org", "status": "BACKLOG"}, &other)
	e.do("POST", "/tasks?org="+dev.Slug, map[string]any{"title": "dev org", "status": "BACKLOG"}, &mine)
	e.do("PATCH", "/orgs/"+dev.ID.String(), map[string]any{"name": "Dev team"}, nil)
	e.do("DELETE", "/tasks/"+other.ID.String(), nil, nil)
	e.do("DELETE", "/tasks/"+mine.ID.String(), nil, nil)

	want := []string{"task.updated " + mine.ID.String(), "org.updated " + dev.ID.String(), "task.deleted " + mine.ID.String()}
	for i, w := range want {
		select {
		case ev := <-got:
			if ev.name+" "+ev.data["id"].(string) != w {
				t.Fatalf("event %d: got %s %v, want %s (the default org's task must not arrive)", i, ev.name, ev.data, w)
			}
			if strings.HasPrefix(ev.name, "task.") && ev.data["org_id"] != dev.ID.String() {
				t.Fatalf("task event org_id: %v", ev.data["org_id"])
			}
		case <-ctx.Done():
			t.Fatalf("waiting for %s", w)
		}
	}
}

// A tab still watching an org that was deleted keeps a stream (EventSource would retry an error forever) that
// carries the global events, so it hears org.updated and switches to another org.
func TestEventsForAGoneOrg(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.api.URL+"/api/v1/events?org=deleted-org", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	names := make(chan string, 16)
	sc := bufio.NewScanner(resp.Body)
	waitConnected(t, sc)
	go func() {
		for sc.Scan() {
			if n, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				names <- n
			}
		}
	}()
	e.do("POST", "/tasks", map[string]any{"title": "default org", "status": "BACKLOG"}, nil)
	e.do("POST", "/orgs", map[string]any{"name": "Another"}, nil)
	select {
	case n := <-names:
		if n != "org.updated" {
			t.Fatalf("got %s first, want only the global org.updated", n)
		}
	case <-ctx.Done():
		t.Fatal("no org.updated")
	}
}

// waitConnected reads the stream up to its ": connected" comment, which the handler writes after subscribing,
// so events published from here on reach it.
func waitConnected(t *testing.T, sc *bufio.Scanner) {
	t.Helper()
	for sc.Scan() {
		if sc.Text() == ": connected" {
			return
		}
	}
	t.Fatalf("stream ended before it connected: %v", sc.Err())
}
