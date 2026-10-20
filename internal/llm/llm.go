// Package llm defines provider-neutral chat types used by the agent tool loop. Provider plugins
// (OpenAI-compatible, Anthropic) translate to and from their own wire formats.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content,omitempty"`
	// ToolCalls is set on assistant messages that request tools.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID and IsError are set on tool-result messages.
	ToolCallID string `json:"tool_call_id,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	// ProviderState carries the provider's native assistant message so it can be replayed
	// verbatim on the next turn (e.g. Anthropic thinking blocks must round-trip unchanged).
	ProviderState any `json:"-"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"input_schema"`
}

type Request struct {
	System    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int
}

type Usage struct {
	// InputTokens is the whole prompt, whether or not the provider served part of it from a cache.
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	// CacheReadTokens and CacheWriteTokens are the parts of InputTokens read from and written to the
	// provider's prompt cache, which are billed differently from the rest.
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type Response struct {
	Message    Message
	StopReason string // "end", "tool_use", "max_tokens", "refusal"
	Usage      Usage
}

// ContextWindower is implemented by connectors that know how many input tokens their model accepts.
// ContextWindow returns 0 when it doesn't: a size is then shown without a percentage rather than
// against a guess. Callers ask once per run; implementations cache any lookup.
type ContextWindower interface {
	ContextWindow(ctx context.Context) int64
}

// Chatter is implemented by every LLM connector.
type Chatter interface {
	Chat(ctx context.Context, req Request) (*Response, error)
	Model() string
}

// ErrContextOverflow is wrapped by a connector's Chat error when the request was refused for being
// longer than the model's context window.
var ErrContextOverflow = errors.New("the conversation is longer than the model's context window")

// overflowPhrases are how providers word the refusal of a prompt that doesn't fit the model's window
// (some count max_tokens on top of the prompt).
var overflowPhrases = []string{
	"prompt is too long",                   // Claude API, Vertex
	"exceed context limit",                 // Claude API: input length and max_tokens exceed context limit
	"input is too long",                    // Bedrock: Input is too long for requested model
	"context_length_exceeded",              // OpenAI, Azure (error code)
	"input exceeds the context window",     // OpenAI
	"maximum context length",               // DeepSeek, vLLM, OpenRouter
	"exceeds the maximum number of tokens", // Gemini
	"context length exceeded",              // Ollama
}

// IsOverflowText tells whether a provider's error body reports a prompt that doesn't fit the model's
// context window. Connectors check the status code themselves and pass only the response body, not
// a formatted error that also carries the URL.
func IsOverflowText(body string) bool {
	b := strings.ToLower(body)
	for _, p := range overflowPhrases {
		if strings.Contains(b, p) {
			return true
		}
	}
	return false
}

// CallTimeout bounds one model call. A long answer (a whole document at high effort) takes minutes,
// so connectors don't use the shared HTTP client's short overall timeout for model calls; this is
// the backstop instead.
const CallTimeout = 10 * time.Minute

// ResponseHeaderTimeout is how long a streaming model call may wait for the server to start
// answering (it has to read the whole prompt first). With an SDK's 3 retries, 4 waits stay within
// CallTimeout.
const ResponseHeaderTimeout = 2 * time.Minute

// HTTPClient returns a client for model calls built from the shared one: no overall timeout (that
// would cut a long answer off mid-stream; callers bound the call with CallTimeout), and, when its
// transport is a plain *http.Transport, a limit on waiting for the response headers. It is built once
// per shared client and timeout, so every connector instance (one per run) shares one connection pool.
func HTTPClient(shared *http.Client, headerTimeout time.Duration) *http.Client {
	key := httpClientKey{shared, headerTimeout}
	httpClients.Lock()
	defer httpClients.Unlock()
	if c, ok := httpClients.m[key]; ok {
		return c
	}
	c := &http.Client{}
	if shared != nil {
		*c = *shared
	}
	c.Timeout = 0
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	if headerTimeout > 0 {
		if t, ok := base.(*http.Transport); ok {
			t = t.Clone()
			t.ResponseHeaderTimeout = headerTimeout
			c.Transport = t
		} else {
			slog.Warn("llm: the HTTP transport isn't an *http.Transport, so model calls have no response header timeout (only the call timeout)",
				"transport", fmt.Sprintf("%T", base))
		}
	}
	httpClients.m[key] = c
	return c
}

type httpClientKey struct {
	shared  *http.Client
	timeout time.Duration
}

var httpClients = struct {
	sync.Mutex
	m map[httpClientKey]*http.Client
}{m: map[httpClientKey]*http.Client{}}
