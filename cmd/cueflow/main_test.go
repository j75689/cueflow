package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/events/pubsub"
)

// A delete that Pub/Sub answers slowly is waited for as long as the timeout allows, and only once.
func TestSubscriptionDeleteWaitsForTheTimeout(t *testing.T) {
	var deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			time.Sleep(300 * time.Millisecond) // done, but the answer comes late
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	for _, tc := range []struct {
		timeout time.Duration
		ok      bool
	}{{time.Second, true}, {50 * time.Millisecond, false}} {
		tr, err := pubsub.New(context.Background(), pubsub.Config{Topic: "projects/p/topics/t", Endpoint: srv.URL, Client: srv.Client()})
		if err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		closeEvents := subscriptionCloser(tr, tc.timeout, slog.New(slog.NewTextHandler(&logs, nil)))
		before := deletes.Load()
		closeEvents()
		closeEvents()
		if n := deletes.Load() - before; n != 1 {
			t.Errorf("timeout %s: %d deletes, want 1", tc.timeout, n)
		}
		if failed := strings.Contains(logs.String(), "delete events subscription"); failed == tc.ok {
			t.Errorf("timeout %s: logs %q", tc.timeout, logs.String())
		}
	}
}
