package pubsub

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/events"
)

// fakePubSub is the part of the REST API the transport uses: subscriptions fan a topic's messages
// out, pulls wait briefly for one.
type fakePubSub struct {
	mu      sync.Mutex
	subs    map[string][]string // subscription -> queued base64 data
	created map[string]map[string]any
	deleted []string
	acked   int
}

func newFake() *fakePubSub {
	return &fakePubSub{subs: map[string][]string{}, created: map[string]map[string]any{}}
}

func (f *fakePubSub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPut && strings.Contains(path, "/subscriptions/"):
		f.subs[path], f.created[path] = nil, body
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodDelete:
		delete(f.subs, path)
		f.deleted = append(f.deleted, path)
		_, _ = w.Write([]byte(`{}`))
	case strings.HasSuffix(path, ":publish"):
		for _, m := range body["messages"].([]any) {
			for s := range f.subs {
				f.subs[s] = append(f.subs[s], m.(map[string]any)["data"].(string))
			}
		}
		_, _ = w.Write([]byte(`{"messageIds":["1"]}`))
	case strings.HasSuffix(path, ":pull"):
		sub := strings.TrimSuffix(path, ":pull")
		if _, ok := f.subs[sub]; !ok {
			http.Error(w, `{"error":{"message":"Subscription does not exist"}}`, http.StatusNotFound)
			return
		}
		f.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		f.mu.Lock()
		var msgs []map[string]any
		for i, d := range f.subs[sub] {
			msgs = append(msgs, map[string]any{"ackId": sub + string(rune('a'+i)), "message": map[string]any{"data": d}})
		}
		f.subs[sub] = nil
		_ = json.NewEncoder(w).Encode(map[string]any{"receivedMessages": msgs})
	case strings.HasSuffix(path, ":acknowledge"):
		f.acked += len(body["ackIds"].([]any))
		_, _ = w.Write([]byte(`{}`))
	default:
		http.NotFound(w, r)
	}
}

// endpoint is the emulator when PUBSUB_EMULATOR_HOST is set (its topic must exist), else a fake.
func endpoint(t *testing.T) (string, *fakePubSub) {
	if h := os.Getenv("PUBSUB_EMULATOR_HOST"); h != "" {
		return "http://" + h, nil
	}
	f := newFake()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv.URL, f
}

func TestTwoInstancesSeeEachOthersEvents(t *testing.T) {
	ep, fake := endpoint(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := Config{Topic: "projects/p/topics/cueflow-events", Endpoint: ep, Client: http.DefaultClient}
	ta, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	tb, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if ta.Subscription() == tb.Subscription() {
		t.Fatal("each instance needs its own subscription")
	}
	if fake != nil {
		got := fake.created[ta.Subscription()]
		if got["topic"] != cfg.Topic || got["expirationPolicy"].(map[string]any)["ttl"] != "86400s" {
			t.Fatalf("subscription settings: %v", got)
		}
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, b := events.NewBroker(), events.NewBroker()
	a.Use(ta, log)
	b.Use(tb, log)
	go a.Run(ctx)
	go b.Run(ctx)
	inA, unsubA := a.Subscribe()
	defer unsubA()
	inB, unsubB := b.Subscribe()
	defer unsubB()

	org := uuid.New()
	a.Publish(org, "task.updated", map[string]any{"id": "x"})
	for name, ch := range map[string]<-chan events.Event{"other instance": inB, "same instance": inA} {
		select {
		case ev := <-ch:
			if ev.Name != "task.updated" || ev.Org != org.String() {
				t.Fatalf("%s got %q for org %q", name, ev.Name, ev.Org)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: event did not arrive", name)
		}
	}

	for _, tr := range []*Transport{ta, tb} {
		if err := tr.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if fake != nil && (len(fake.deleted) != 2 || fake.acked < 2) {
		t.Fatalf("deleted %v, acked %d", fake.deleted, fake.acked)
	}
}

func TestTopicMustBeAFullName(t *testing.T) {
	for _, topic := range []string{"cueflow-events", "projects/p/subscriptions/s", "projects//topics/t"} {
		if _, err := New(context.Background(), Config{Topic: topic, Client: http.DefaultClient}); err == nil {
			t.Errorf("%q accepted", topic)
		}
	}
}

// A pull on a deleted subscription fails, so Run reconnects instead of hanging.
func TestReceiveFailsWhenTheSubscriptionIsGone(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	tr, err := New(context.Background(), Config{Topic: "projects/p/topics/t", Endpoint: srv.URL, Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_ = tr.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.Receive(ctx, func([]byte) {}); err == nil || !strings.Contains(err.Error(), "Subscription does not exist") {
		t.Fatalf("got %v", err)
	}
}
