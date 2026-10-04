package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/llm"
)

func fakeADC(t *testing.T, credType string) {
	t.Helper()
	orig := findDefaultCredentials
	findDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return &google.Credentials{
			ProjectID:   "adc-project",
			TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "ya29.test"}),
			JSON:        []byte(`{"type":"` + credType + `"}`),
		}, nil
	}
	t.Cleanup(func() { findDefaultCredentials = orig })
}

func TestVertexBaseURL(t *testing.T) {
	if got := vertexBaseURL("p1", "global"); got != "https://aiplatform.googleapis.com/v1/projects/p1/locations/global/endpoints/openapi" {
		t.Fatalf("global: %s", got)
	}
	if got := vertexBaseURL("p1", "us-central1"); got != "https://us-central1-aiplatform.googleapis.com/v1/projects/p1/locations/us-central1/endpoints/openapi" {
		t.Fatalf("regional: %s", got)
	}
}

func TestVertexGeminiWithGcloudUserCredentials(t *testing.T) {
	fakeADC(t, "authorized_user")
	var reqs []map[string]any
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		reqs = append(reqs, body)
		headers = append(headers, r.Header.Clone())
		if r.URL.Path != "/openapi/chat/completions" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if len(reqs) == 1 {
			// Gemini returns a thought signature that must be echoed on the next turn.
			_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"mcp\"}"},
				"extra_content":{"google":{"thought_signature":"sig-abc"}}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer srv.Close()

	inst, err := connector.Build("gemini_vertex", connector.Config{
		"project_id": "my-proj", "model": "gemini-2.5-pro", "base_url": srv.URL + "/openapi",
	}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	model := inst.(connector.LLM)
	tools := []llm.Tool{{Name: "web_search", Description: "search", Schema: map[string]any{"type": "object"}}}
	msgs := []llm.Message{{Role: llm.RoleUser, Content: "find news"}}

	first, err := model.Chat(context.Background(), llm.Request{Messages: msgs, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if first.StopReason != "tool_use" || len(first.Message.ToolCalls) != 1 {
		t.Fatalf("first: %+v", first)
	}
	msgs = append(msgs, first.Message, llm.Message{Role: llm.RoleTool, ToolCallID: "call_1", Content: "[]"})
	if _, err := model.Chat(context.Background(), llm.Request{Messages: msgs, Tools: tools}); err != nil {
		t.Fatal(err)
	}

	h := headers[0]
	if h.Get("Authorization") != "Bearer ya29.test" || h.Get("X-Goog-User-Project") != "my-proj" {
		t.Fatalf("auth headers: %v", h)
	}
	if reqs[0]["model"] != "google/gemini-2.5-pro" {
		t.Fatalf("model prefix not added: %v", reqs[0]["model"])
	}
	// The assistant turn is replayed with its thought signature intact.
	raw, _ := json.Marshal(reqs[1]["messages"])
	if !strings.Contains(string(raw), `"thought_signature":"sig-abc"`) {
		t.Fatalf("thought signature not echoed: %s", raw)
	}
}

func TestVertexGeminiServiceAccountSkipsUserProjectHeader(t *testing.T) {
	orig := credentialsFromJSON
	credentialsFromJSON = func(ctx context.Context, js []byte, scopes ...string) (*google.Credentials, error) {
		if !strings.Contains(string(js), "service_account") {
			return nil, errors.New("bad key")
		}
		return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "sa-token"}), JSON: js}, nil
	}
	t.Cleanup(func() { credentialsFromJSON = orig })

	inst, err := connector.Build("gemini_vertex", connector.Config{"project_id": "p", "model": "google/gemini-2.5-flash",
		"service_account_json": `{"type":"service_account"}`}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if h := inst.(*Client).headers["x-goog-user-project"]; h != "" {
		t.Fatalf("service accounts should not send x-goog-user-project, got %q", h)
	}
	if got := inst.(*Client).baseURL; got != vertexBaseURL("p", "global") {
		t.Fatalf("default endpoint: %s", got)
	}
	if _, err := connector.Build("gemini_vertex", connector.Config{"project_id": "p", "model": "m", "service_account_json": "nope"}, connector.Env{}); err == nil ||
		!strings.Contains(err.Error(), "vertex: credentials") {
		t.Fatalf("bad key should fail clearly: %v", err)
	}
}

func TestVertexGeminiRequiresProjectAndModel(t *testing.T) {
	fakeADC(t, "authorized_user")
	if _, err := connector.Build("gemini_vertex", connector.Config{"model": "m"}, connector.Env{}); err == nil {
		t.Fatal("project_id is required")
	}
	if _, err := connector.Build("gemini_vertex", connector.Config{"project_id": "p"}, connector.Env{}); err == nil {
		t.Fatal("model is required")
	}
}
