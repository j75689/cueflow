package cloudtasks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/jobs"
)

const queueName = "projects/p/locations/asia-northeast1/queues/q"

var fixedNow = time.Date(2026, 1, 2, 3, 4, 7, 0, time.UTC)

func newQ(t *testing.T, srv *httptest.Server, sleeps *[]time.Duration) *Queue {
	t.Helper()
	q, err := New(Config{
		Queue: queueName, TargetURL: "https://svc.run.app/", ServiceAccount: "sa@p.iam.gserviceaccount.com",
		Client: srv.Client(), Endpoint: srv.URL,
		Sleep: func(d time.Duration) {
			if sleeps != nil {
				*sleeps = append(*sleeps, d)
			}
		},
		Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestEnqueueRequestShape(t *testing.T) {
	id := uuid.New()
	var gotPath string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()
	q := newQ(t, srv, nil)
	if err := q.Enqueue(context.Background(), jobs.KindCrew, id, 0); err != nil {
		t.Fatal(err)
	}
	if gotPath != "POST /v2/"+queueName+"/tasks" {
		t.Fatalf("path = %s", gotPath)
	}
	tk := got["task"].(map[string]any)
	wantName := queueName + "/tasks/crew-" + id.String() + "-" + itoa(fixedNow.Unix()/5)
	if tk["name"] != wantName {
		t.Fatalf("name = %v want %v", tk["name"], wantName)
	}
	if tk["dispatchDeadline"] != "1800s" {
		t.Fatalf("dispatchDeadline = %v", tk["dispatchDeadline"])
	}
	if _, has := tk["scheduleTime"]; has {
		t.Fatal("scheduleTime must be absent without delay")
	}
	hr := tk["httpRequest"].(map[string]any)
	if hr["url"] != "https://svc.run.app/internal/work/crew/"+id.String() || hr["httpMethod"] != "POST" {
		t.Fatalf("httpRequest = %v", hr)
	}
	oidc := hr["oidcToken"].(map[string]any)
	if oidc["serviceAccountEmail"] != "sa@p.iam.gserviceaccount.com" || oidc["audience"] != "https://svc.run.app" {
		t.Fatalf("oidc = %v", oidc)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestEnqueueDelayAndBucket(t *testing.T) {
	id := uuid.New()
	var names, sched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		names = append(names, m["task"]["name"].(string))
		s, _ := m["task"]["scheduleTime"].(string)
		sched = append(sched, s)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()
	q := newQ(t, srv, nil)
	ctx := context.Background()
	_ = q.Enqueue(ctx, jobs.KindSchedule, id, 30*time.Second)
	_ = q.Enqueue(ctx, jobs.KindSchedule, id, 30*time.Second)
	_ = q.Enqueue(ctx, jobs.KindSchedule, id, 40*time.Second)
	if names[0] != names[1] {
		t.Fatalf("same job within window should share a name: %v", names)
	}
	if names[0] == names[2] {
		t.Fatalf("different bucket should differ: %v", names)
	}
	if sched[0] != "2026-01-02T03:04:37Z" {
		t.Fatalf("scheduleTime = %q", sched[0])
	}
}

func TestEnqueueConflictIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"status":"ALREADY_EXISTS"}}`, http.StatusConflict)
	}))
	defer srv.Close()
	if err := newQ(t, srv, nil).Enqueue(context.Background(), jobs.KindCrew, uuid.New(), 0); err != nil {
		t.Fatal(err)
	}
}

func TestEnqueueRetries(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()
	var sleeps []time.Duration
	if err := newQ(t, srv, &sleeps).Enqueue(context.Background(), jobs.KindCrew, uuid.New(), 0); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 || len(sleeps) != 2 {
		t.Fatalf("attempts=%d sleeps=%v", n.Load(), sleeps)
	}
}

func TestEnqueueGivesUpAfterThree(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	err := newQ(t, srv, nil).Enqueue(context.Background(), jobs.KindCrew, uuid.New(), 0)
	if err == nil || !strings.Contains(err.Error(), "429") || n.Load() != 3 {
		t.Fatalf("err=%v attempts=%d", err, n.Load())
	}
}

func TestEnqueueClientErrorNoRetry(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, `{"error":"queue not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	err := newQ(t, srv, nil).Enqueue(context.Background(), jobs.KindCrew, uuid.New(), 0)
	if err == nil || !strings.Contains(err.Error(), "queue not found") || n.Load() != 1 {
		t.Fatalf("err=%v attempts=%d", err, n.Load())
	}
}

func TestNewValidation(t *testing.T) {
	base := Config{Queue: queueName, TargetURL: "https://x", ServiceAccount: "a@b", Client: http.DefaultClient}
	for name, mut := range map[string]func(*Config){
		"bad queue":  func(c *Config) { c.Queue = "q" },
		"no target":  func(c *Config) { c.TargetURL = "" },
		"no account": func(c *Config) { c.ServiceAccount = "" },
	} {
		c := base
		mut(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := New(base); err != nil {
		t.Fatal(err)
	}
}
