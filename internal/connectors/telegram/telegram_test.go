package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/connector"
)

func TestNotifySendsPlainText(t *testing.T) {
	var path string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	inst, err := New(connector.Config{"bot_token": "123:abc", "chat_id": "-100", "message_thread_id": 7, "endpoint_url": srv.URL},
		connector.Env{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("字", 5000)
	if err := inst.(*Notifier).Notify(context.Background(), connector.Notification{Subject: "Ready", Text: long}); err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:abc/sendMessage" {
		t.Fatalf("path %q", path)
	}
	text, _ := got["text"].(string)
	if !strings.HasPrefix(text, "Ready\n\n字") || len([]rune(text)) > maxText+1 {
		t.Fatalf("text starts %q, %d runes", text[:12], len([]rune(text)))
	}
	if got["chat_id"] != "-100" || got["message_thread_id"] != float64(7) || got["parse_mode"] != nil {
		t.Fatalf("payload %v", got)
	}
}

func TestErrorsNeverCarryTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	}))
	base := srv.URL
	n, _ := New(connector.Config{"bot_token": "123:secret", "chat_id": "1", "endpoint_url": base}, connector.Env{HTTPClient: srv.Client()})

	res, _ := n.(*Notifier).Test(context.Background())
	if res.OK || res.Message != "telegram: HTTP 400: Bad Request: chat not found" {
		t.Fatalf("API error: %+v", res)
	}
	srv.Close() // now the request itself fails
	err := n.(*Notifier).Notify(context.Background(), connector.Notification{Text: "x"})
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), base) {
		t.Fatalf("transport error %v", err)
	}
}
