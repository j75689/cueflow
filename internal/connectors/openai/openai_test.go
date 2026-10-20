package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/connector"
	_ "github.com/j75689/cueflow/internal/connectors/openai"
	"github.com/j75689/cueflow/internal/llm"
)

func TestChatWithToolCalls(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("no key configured, but Authorization was sent")
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,
			"tool_calls":[{"id":"c1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"go\"}"}}]}}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4}}}`))
	}))
	defer srv.Close()

	inst, err := connector.Build("openai", connector.Config{"provider": "ollama", "base_url": srv.URL + "/v1", "model": "qwen3"}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	model := inst.(connector.LLM)
	resp, err := model.Chat(context.Background(), llm.Request{
		System: "sys",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "hi"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c0", Name: "web_search", Arguments: json.RawMessage(`{"query":"a"}`)}}},
			{Role: llm.RoleTool, ToolCallID: "c0", Content: "[]"},
		},
		Tools: []llm.Tool{{Name: "web_search", Description: "d", Schema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "web_search" {
		t.Fatalf("resp: %+v", resp)
	}
	if string(resp.Message.ToolCalls[0].Arguments) != `{"query":"go"}` {
		t.Errorf("args: %s", resp.Message.ToolCalls[0].Arguments)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 || resp.Usage.CacheReadTokens != 4 {
		t.Errorf("usage: %+v", resp.Usage)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("messages sent: %v", msgs)
	}
	if got["model"] != "qwen3" || got["max_tokens"] == nil || got["tools"] == nil {
		t.Errorf("request body: %v", got)
	}
}

func TestHTTPErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	inst, _ := connector.Build("openai", connector.Config{"provider": "openai_compatible", "base_url": srv.URL, "model": "m", "api_key": "k"}, connector.Env{})
	res, _ := inst.Test(context.Background())
	if res.OK {
		t.Fatal("expected test failure")
	}
}

// A prompt longer than the model's window comes back as llm.ErrContextOverflow, so the crew can say so
// plainly; other errors, including ones that only look similar, keep their own text.
func TestContextOverflowRecognized(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 128000 tokens."}}`, true},
		{400, `{"error":{"code":400,"message":"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576)."}}`, true},
		// DeepSeek, vLLM and OpenRouter. (The same words for a max_tokens larger than the window are
		// told apart by the crew: that one fails on the first turn.)
		{400, `{"object":"error","message":"This model's maximum context length is 65536 tokens. However, you requested 70000 tokens (65000 in the messages, 5000 in the completion)."}`, true},
		// Ollama answers 500.
		{500, `{"error":"context length exceeded"}`, true},
		{500, `{"error":"internal error"}`, false},
		// A proxy's body limit: not an overflow.
		{413, `<html>413 Request Entity Too Large</html>`, false},
		{400, `{"error":"invalid tool schema"}`, false},
		{401, `{"error":"bad key"}`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, c.body, c.status)
		}))
		inst, _ := connector.Build("openai", connector.Config{"provider": "openai_compatible", "base_url": srv.URL, "model": "m", "api_key": "k"}, connector.Env{})
		_, err := inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
		srv.Close()
		if got := errors.Is(err, llm.ErrContextOverflow); got != c.want {
			t.Errorf("%d %s: overflow=%v, want %v (%v)", c.status, c.body, got, c.want, err)
		}
	}
}

func TestRequiresModelForCustomProvider(t *testing.T) {
	if _, err := connector.Build("openai", connector.Config{"provider": "openai_compatible", "base_url": "http://x"}, connector.Env{}); err == nil {
		t.Fatal("expected error when model is missing")
	}
}

type seen struct {
	path, query string
	header      http.Header
	body        map[string]any
}

func recorder(t *testing.T) (*httptest.Server, *seen) {
	t.Helper()
	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.query, got.header = r.URL.Path, r.URL.RawQuery, r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"pong"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func ping(t *testing.T, cfg connector.Config) {
	t.Helper()
	inst, err := connector.Build("openai", cfg, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "ping"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRouter(t *testing.T) {
	srv, got := recorder(t)
	ping(t, connector.Config{"provider": "openrouter", "base_url": srv.URL + "/api/v1", "api_key": "sk-or-v1-test"})
	if got.path != "/api/v1/chat/completions" || got.header.Get("Authorization") != "Bearer sk-or-v1-test" {
		t.Fatalf("request: %s %v", got.path, got.header)
	}
	if got.header.Get("X-Title") != "CueFlow" || got.header.Get("HTTP-Referer") == "" {
		t.Fatalf("attribution headers missing: %v", got.header)
	}
	if got.body["model"] != "openrouter/auto" {
		t.Fatalf("default model: %v", got.body["model"])
	}
	// Defaults: real OpenRouter base URL.
	inst, _ := connector.Build("openai", connector.Config{"provider": "openrouter", "api_key": "k"}, connector.Env{})
	if inst == nil {
		t.Fatal("openrouter preset should build without a base URL")
	}
}

func TestCustomHeadersOverride(t *testing.T) {
	srv, got := recorder(t)
	ping(t, connector.Config{"provider": "openrouter", "base_url": srv.URL, "api_key": "k",
		"headers": map[string]any{"X-Title": "My Studio", "X-Gateway-Key": "gw"}})
	if got.header.Get("X-Title") != "My Studio" || got.header.Get("X-Gateway-Key") != "gw" {
		t.Fatalf("headers: %v", got.header)
	}
}

func TestAzureV1(t *testing.T) {
	srv, got := recorder(t)
	ping(t, connector.Config{"provider": "azure", "base_url": srv.URL + "/openai/v1", "api_key": "az-key", "model": "my-gpt"})
	if got.path != "/openai/v1/chat/completions" || got.header.Get("Api-Key") != "az-key" || got.header.Get("Authorization") != "" {
		t.Fatalf("request: %s %v", got.path, got.header)
	}
	if got.body["max_completion_tokens"] == nil || got.body["model"] != "my-gpt" {
		t.Fatalf("body: %v", got.body)
	}
}

func TestAzureLegacyDeployments(t *testing.T) {
	srv, got := recorder(t)
	ping(t, connector.Config{"provider": "azure", "base_url": srv.URL + "/openai", "api_key": "az-key",
		"model": "prod-gpt", "api_version": "2024-10-21"})
	if got.path != "/openai/deployments/prod-gpt/chat/completions" || got.query != "api-version=2024-10-21" {
		t.Fatalf("url: %s?%s", got.path, got.query)
	}
}

func TestAzureRequiresKeyAndDeployment(t *testing.T) {
	if _, err := connector.Build("openai", connector.Config{"provider": "azure", "base_url": "https://x/openai/v1", "model": "m"}, connector.Env{}); err == nil {
		t.Fatal("azure without a key should fail")
	}
	if _, err := connector.Build("openai", connector.Config{"provider": "azure", "base_url": "https://x/openai/v1", "api_key": "k"}, connector.Env{}); err == nil {
		t.Fatal("azure without a deployment name should fail")
	}
}

// The context window is what the operator configured; without one it is unknown (0), not a guess.
func TestContextWindowConfigured(t *testing.T) {
	for cfg, want := range map[string]int64{"": 0, "128000": 128000, "-5": 0} {
		c := connector.Config{"provider": "ollama", "model": "qwen3"}
		if cfg != "" {
			c["context_window"] = cfg
		}
		inst, err := connector.Build("openai", c, connector.Env{})
		if err != nil {
			t.Fatal(err)
		}
		if got := inst.(llm.ContextWindower).ContextWindow(context.Background()); got != want {
			t.Errorf("context_window %q: %d", cfg, got)
		}
	}
}

// sse writes chunks as a chat completions stream, pausing between them, then [DONE].
func sse(w http.ResponseWriter, pause time.Duration, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
		w.(http.Flusher).Flush()
		time.Sleep(pause)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// A streamed answer is put back together: text in order, tool calls from their pieces (Gemini's
// thought signature kept for the replay), finish reason and the usage chunk. The request asks for
// a stream with usage, and a stream longer than the shared client's timeout isn't cut off.
func TestStreamedCompletion(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		sse(w, 100*time.Millisecond,
			`{"choices":[{"index":0,"delta":{"role":"assistant","content":"Reading "}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"the file."}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read_file","arguments":""},"extra_content":{"google":{"thought_signature":"sig"}}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"name\":"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.md\"}"}},{"index":1,"id":"c2","function":{"name":"list_files","arguments":"{}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":100}}}`)
	}))
	defer srv.Close()
	inst, err := connector.Build("openai", connector.Config{"provider": "openai_compatible", "base_url": srv.URL, "model": "m"},
		connector.Env{HTTPClient: &http.Client{Timeout: 300 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "go"}}})
	if err != nil {
		t.Fatalf("a long stream was cut off: %v", err)
	}
	if got["stream"] != true || got["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("request: %v", got)
	}
	if resp.Message.Content != "Reading the file." || resp.StopReason != "tool_use" || len(resp.Message.ToolCalls) != 2 ||
		resp.Message.ToolCalls[0].Name != "read_file" || string(resp.Message.ToolCalls[0].Arguments) != `{"name":"a.md"}` ||
		resp.Message.ToolCalls[1].ID != "c2" {
		t.Fatalf("reassembled: %+v", resp.Message)
	}
	if resp.Usage.InputTokens != 120 || resp.Usage.OutputTokens != 30 || resp.Usage.CacheReadTokens != 100 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
	// The replayed turn carries the thought signature back.
	_, _ = inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: "go"}, resp.Message, {Role: llm.RoleTool, ToolCallID: "c1", Content: "x"}}})
	replayed, _ := json.Marshal(got["messages"])
	if !strings.Contains(string(replayed), `"thought_signature":"sig"`) {
		t.Fatalf("thought signature not replayed: %s", replayed)
	}
}

// A server that refuses only stream_options keeps streaming without them, and isn't asked for them
// again; one that refuses streaming gets plain requests from then on, and its plain answer is read.
func TestStreamRefusedFallsBack(t *testing.T) {
	for _, refuse := range []string{"stream_options", "stream"} {
		var bodies []map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			bodies = append(bodies, b)
			if b["stream_options"] != nil || refuse == "stream" && b["stream"] != nil {
				http.Error(w, `{"error":{"message":"Unrecognized request argument supplied: `+refuse+`"}}`, http.StatusBadRequest)
				return
			}
			if b["stream"] != nil {
				sse(w, 0, `{"choices":[{"index":0,"delta":{"content":"streamed"}}]}`)
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"plain"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`))
		}))
		inst, _ := connector.Build("openai", connector.Config{"provider": "openai_compatible", "base_url": srv.URL, "model": "m"}, connector.Env{})
		chat := func() string {
			resp, err := inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
			if err != nil {
				t.Fatalf("%s: %v", refuse, err)
			}
			return resp.Message.Content
		}
		first, second := chat(), chat()
		srv.Close()
		switch refuse {
		case "stream_options":
			// options refused, then streamed without them; the second call doesn't ask for them.
			if first != "streamed" || second != "streamed" || len(bodies) != 3 || bodies[1]["stream"] != true || bodies[2]["stream_options"] != nil {
				t.Fatalf("stream_options: %q %q, %d requests", first, second, len(bodies))
			}
		case "stream":
			// options refused, streaming refused, plain; the second call is plain at once.
			if first != "plain" || second != "plain" || len(bodies) != 4 || bodies[3]["stream"] != nil {
				t.Fatalf("stream: %q %q, %d requests", first, second, len(bodies))
			}
		}
	}
}

// Broken or odd streams: a cut at the size cap, an overflow reported in an error chunk, tool calls
// without an index, and a stream with no choice at all.
func TestStreamEdgeCases(t *testing.T) {
	serve := func(write func(w http.ResponseWriter)) (*llm.Response, error) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { write(w) }))
		defer srv.Close()
		inst, _ := connector.Build("openai", connector.Config{"provider": "openai_compatible", "base_url": srv.URL, "model": "m"}, connector.Env{})
		return inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
	}

	// Over 16 MB: an error, not the first 16 MB passed off as the answer.
	_, err := serve(func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		piece := `data: {"choices":[{"index":0,"delta":{"content":"` + strings.Repeat("x", 1<<20) + `"}}]}` + "\n\n"
		for range 17 {
			fmt.Fprint(w, piece)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "over 16 MB") {
		t.Errorf("oversized stream: %v", err)
	}

	// OpenRouter answers 200 and reports the overflow in the stream.
	_, err = serve(func(w http.ResponseWriter) {
		sse(w, 0, `{"error":{"code":400,"message":"This endpoint's maximum context length is 131072 tokens. However, you requested about 140000 tokens."}}`)
	})
	if !errors.Is(err, llm.ErrContextOverflow) {
		t.Errorf("in-stream overflow: %v", err)
	}

	// No index on the tool-call deltas: each new id starts a new call.
	resp, err := serve(func(w http.ResponseWriter) {
		sse(w, 0,
			`{"choices":[{"delta":{"tool_calls":[{"id":"a","function":{"name":"read_file","arguments":"{\"name\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"x\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"id":"b","function":{"name":"list_files","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	})
	if err != nil || len(resp.Message.ToolCalls) != 2 || string(resp.Message.ToolCalls[0].Arguments) != `{"name":"x"}` ||
		resp.Message.ToolCalls[1].Name != "list_files" {
		t.Errorf("index-less tool calls: %v %+v", err, resp)
	}

	// Usage and metadata only (Azure's prompt filter results): an empty response, like a plain one.
	_, err = serve(func(w http.ResponseWriter) {
		sse(w, 0, `{"choices":[],"prompt_filter_results":[{"prompt_index":0}]}`, `{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":0}}`)
	})
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Errorf("choice-less stream: %v", err)
	}
}
