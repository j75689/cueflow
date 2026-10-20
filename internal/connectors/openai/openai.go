// Package openai is the LLM connector for OpenAI and every OpenAI-compatible Chat Completions
// endpoint: OpenRouter, Azure OpenAI, DeepSeek, Ollama, vLLM, LocalAI, LM Studio...
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"golang.org/x/oauth2"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/llm"
)

// Presets fill in base URL and model for well-known providers.
var presets = map[string]struct{ baseURL, model string }{
	"openai":            {"https://api.openai.com/v1", "gpt-5.2"},
	"openrouter":        {"https://openrouter.ai/api/v1", "openrouter/auto"},
	"azure":             {"", ""},
	"deepseek":          {"https://api.deepseek.com/v1", "deepseek-chat"},
	"ollama":            {"http://ollama:11434/v1", "qwen3:8b"},
	"vllm":              {"http://vllm:8000/v1", ""},
	"localai":           {"http://localai:8080/v1", ""},
	"openai_compatible": {"", ""},
}

func init() {
	connector.Register(connector.Spec{
		Type:        "openai",
		Kind:        connector.KindLLM,
		Name:        "OpenAI / OpenAI-compatible",
		Description: "OpenAI, OpenRouter, Azure OpenAI, DeepSeek, or any local OpenAI-compatible server (Ollama, vLLM, LocalAI). The model must support tool calling.",
		Fields: []connector.Field{
			{Key: "provider", Label: "Provider", Type: connector.FieldSelect,
				Options: []string{"openai", "openrouter", "azure", "deepseek", "ollama", "vllm", "localai", "openai_compatible"}, Default: "openai"},
			{Key: "base_url", Label: "Base URL", Type: connector.FieldString,
				Help: "Leave blank to use the provider default. Azure: https://<resource>.openai.azure.com/openai/v1 (or .../openai with an API version)"},
			{Key: "model", Label: "Model", Type: connector.FieldString,
				Help: "Leave blank to use the provider default. OpenRouter: e.g. anthropic/claude-opus-4.8. Azure: the deployment name"},
			{Key: "api_key", Label: "API key", Type: connector.FieldPassword, Secret: true, Help: "Not needed for most local servers"},
			{Key: "api_version", Label: "Azure API version", Type: connector.FieldString,
				Help: "Azure only, for the legacy deployments endpoint (e.g. 2024-10-21). Leave blank for the v1 endpoint"},
			{Key: "headers", Label: "Extra headers", Type: connector.FieldKV,
				Help: "Sent with every request, e.g. a gateway key. OpenRouter defaults HTTP-Referer and X-Title to CueFlow"},
			{Key: "max_tokens", Label: "Max output tokens", Type: connector.FieldNumber, Default: 8000},
			contextWindowField,
			{Key: "temperature", Label: "Temperature", Type: connector.FieldString, Help: "Optional, e.g. 0.7. Blank uses the server default."},
		},
		DefaultName: func(cfg connector.Config) string {
			label := providerLabels[cfg.String("provider")]
			if label == "" {
				label = "OpenAI-compatible"
			}
			return connector.WithDetail(label, cfg.String("model"))
		},
	}, New)
}

// contextWindowField is the model's input limit, for showing how full a run's context is. It
// depends on the model (and, for local servers, on how they were started), not the provider, so
// there is no default: blank shows the size without a percentage.
var contextWindowField = connector.Field{Key: "context_window", Label: "Context window (tokens)", Type: connector.FieldNumber,
	Help: "The model's input limit, e.g. 128000. Used to show how full a run's context is; blank shows the size only"}

var providerLabels = map[string]string{
	"openai": "OpenAI", "openrouter": "OpenRouter", "azure": "Azure OpenAI", "deepseek": "DeepSeek",
	"ollama": "Ollama", "vllm": "vLLM", "localai": "LocalAI", "openai_compatible": "OpenAI-compatible",
}

type Client struct {
	provider, baseURL, model, apiKey, temperature, apiVersion string
	headers                                                   map[string]string
	maxTokens                                                 int
	contextWindow                                             int64
	http                                                      *http.Client
	// tokens, when set, supplies short-lived OAuth access tokens (Vertex AI).
	tokens oauth2.TokenSource
	logger *slog.Logger
	// noStreamOptions and noStream are set once the server has refused stream_options, or streaming
	// itself, so later calls don't ask again.
	noStreamOptions, noStream atomic.Bool
	warnedNoUsage             atomic.Bool
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	p := presets[cfg.String("provider")]
	provider := cfg.String("provider")
	c := &Client{
		provider:    provider,
		apiVersion:  cfg.String("api_version"),
		headers:     map[string]string{},
		baseURL:     strings.TrimRight(firstNonEmpty(cfg.String("base_url"), p.baseURL), "/"),
		model:       firstNonEmpty(cfg.String("model"), p.model),
		apiKey:      cfg.String("api_key"),
		temperature: cfg.String("temperature"),
		maxTokens:   cfg.Int("max_tokens", 8000),
		// No overall timeout (it would cut a stream off), and a server that doesn't stream answers
		// only once the whole answer is written. Chat bounds each call with llm.CallTimeout.
		http:   llm.HTTPClient(env.HTTPClient, 0),
		logger: env.Logger,
	}
	c.contextWindow = int64(cfg.Int("context_window", 0))
	if provider == "openrouter" {
		// App attribution recommended by OpenRouter; overridable via Extra headers.
		c.headers["HTTP-Referer"] = "https://github.com/j75689/cueflow"
		c.headers["X-Title"] = "CueFlow"
	}
	for k, v := range cfg.KV("headers") {
		if k = strings.TrimSpace(k); k != "" {
			c.headers[k] = v
		}
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("openai: base URL is required for provider %q", cfg.String("provider"))
	}
	if provider == "azure" && c.apiKey == "" && c.headers["Authorization"] == "" {
		return nil, fmt.Errorf("openai: Azure OpenAI needs an API key")
	}
	if c.model == "" {
		return nil, fmt.Errorf("openai: model is required for provider %q", cfg.String("provider"))
	}
	return c, nil
}

func (c *Client) Model() string { return c.model }

func (c *Client) Test(ctx context.Context) (*connector.TestResult, error) {
	resp, err := c.Chat(ctx, llm.Request{
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: "Reply with the single word: pong"}},
		MaxTokens: 512,
	})
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	return &connector.TestResult{OK: true, Message: fmt.Sprintf("%s replied: %.60s", c.model, strings.TrimSpace(resp.Message.Content))}, nil
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	// ExtraContent carries provider extensions that must be echoed back, e.g. Gemini's
	// thought signatures (extra_content.google.thought_signature).
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// ContextWindow is the configured context_window, 0 when not set (or not positive).
func (c *Client) ContextWindow(context.Context) int64 { return max(c.contextWindow, 0) }

func (c *Client) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	msgs := make([]wireMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		s := req.System
		msgs = append(msgs, wireMessage{Role: "system", Content: &s})
	}
	for _, m := range req.Messages {
		// Assistant turns produced by this connector are replayed exactly as the server sent them,
		// so provider extensions (thought signatures) survive the tool loop.
		if prev, ok := m.ProviderState.(wireMessage); ok && m.Role == llm.RoleAssistant {
			msgs = append(msgs, prev)
			continue
		}
		content := m.Content
		wm := wireMessage{Role: string(m.Role), Content: &content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			w := wireToolCall{ID: tc.ID, Type: "function"}
			w.Function.Name = tc.Name
			w.Function.Arguments = string(tc.Arguments)
			wm.ToolCalls = append(wm.ToolCalls, w)
		}
		if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 && content == "" {
			wm.Content = nil
		}
		msgs = append(msgs, wm)
	}
	body := map[string]any{"model": c.model, "messages": msgs}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = c.maxTokens
	}
	if strings.Contains(c.baseURL, "api.openai.com") || c.provider == "azure" {
		body["max_completion_tokens"] = maxTokens
	} else {
		body["max_tokens"] = maxTokens
	}
	if c.temperature != "" {
		var t float64
		if _, err := fmt.Sscan(c.temperature, &t); err == nil {
			body["temperature"] = t
		}
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Schema,
			}})
		}
		body["tools"] = tools
	}
	// Streamed: a long answer arrives as it is written, so no proxy or gateway in between times out
	// an idle connection while the model works. A server that doesn't stream answers in one body,
	// which is read the old way.
	callCtx, cancel := context.WithTimeout(ctx, llm.CallTimeout)
	defer cancel()
	var hresp *http.Response
	for {
		delete(body, "stream")
		delete(body, "stream_options")
		if !c.noStream.Load() {
			body["stream"] = true
			if !c.noStreamOptions.Load() {
				body["stream_options"] = map[string]any{"include_usage": true}
			}
		}
		var err error
		if hresp, err = c.post(callCtx, body); err != nil {
			return nil, err
		}
		if hresp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(io.LimitReader(hresp.Body, 1<<20))
			hresp.Body.Close()
			// Some servers refuse stream_options, or streaming: drop the first one they named, remember
			// it for this connector, and ask again.
			if hresp.StatusCode == http.StatusBadRequest && body["stream"] != nil && c.dropStreaming(body, data) {
				continue
			}
			return nil, httpError(hresp.StatusCode, data)
		}
		break
	}
	defer hresp.Body.Close()
	var out completion
	var err error
	if strings.HasPrefix(hresp.Header.Get("Content-Type"), "text/event-stream") {
		out, err = readStream(hresp.Body)
		if err == nil && !out.sawUsage && body["stream_options"] != nil && c.logger != nil && c.warnedNoUsage.CompareAndSwap(false, true) {
			c.logger.Warn("llm: the server streamed no usage, so this connector's runs record no token counts or context size", "model", c.model)
		}
	} else {
		data, rerr := io.ReadAll(io.LimitReader(hresp.Body, 16<<20))
		if rerr != nil {
			return nil, rerr
		}
		out, err = readBody(data)
	}
	if err != nil {
		return nil, err
	}
	replay := out.message
	replay.Role = "assistant"
	msg := llm.Message{Role: llm.RoleAssistant, ProviderState: replay}
	if out.message.Content != nil {
		msg.Content = *out.message.Content
	}
	for _, tc := range out.message.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if !json.Valid(args) {
			args = json.RawMessage(`{}`)
		}
		msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: args})
	}
	stop := "end"
	switch {
	case len(msg.ToolCalls) > 0:
		stop = "tool_use"
	case out.finish == "length":
		stop = "max_tokens"
	case out.finish == "content_filter":
		stop = "refusal"
	}
	return &llm.Response{Message: msg, StopReason: stop,
		Usage: llm.Usage{InputTokens: out.usage.PromptTokens, OutputTokens: out.usage.CompletionTokens,
			CacheReadTokens: out.usage.PromptTokensDetails.CachedTokens}}, nil
}

// post sends one chat completions request.
func (c *Client) post(ctx context.Context, body map[string]any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		if c.provider == "azure" {
			hreq.Header.Set("api-key", c.apiKey)
		} else {
			hreq.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
	}
	if c.tokens != nil {
		tok, err := c.tokens.Token()
		if err != nil {
			return nil, fmt.Errorf("llm: access token: %w", err)
		}
		hreq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	}
	for k, v := range c.headers {
		hreq.Header.Set(k, v)
	}
	hresp, err := c.http.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}
	return hresp, nil
}

func httpError(status int, data []byte) error {
	body := strings.TrimSpace(string(data))
	if isContextOverflow(status, body) {
		return fmt.Errorf("llm: %w: HTTP %d: %.400s", llm.ErrContextOverflow, status, body)
	}
	return fmt.Errorf("llm: HTTP %d: %.400s", status, body)
}

// wireUsage is the usage block of a completion, streamed (the last chunk) or not.
type wireUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// completion is the assistant message of one call, however it arrived.
type completion struct {
	message  wireMessage
	finish   string
	usage    wireUsage
	sawUsage bool
}

// maxStream caps a streamed answer, like the 16 MB cap on a plain one.
const maxStream = 16 << 20

// dropStreaming takes out what a 400 says the server refused: stream_options first (older vLLM,
// LiteLLM and Azure versions refuse it but stream fine), then streaming itself. It remembers that on
// the client and reports whether there was something to drop.
func (c *Client) dropStreaming(body map[string]any, refusal []byte) bool {
	r := strings.ToLower(string(refusal))
	if !strings.Contains(r, "stream") {
		return false
	}
	if body["stream_options"] != nil {
		c.noStreamOptions.Store(true)
		return true
	}
	c.noStream.Store(true)
	return true
}

func readBody(data []byte) (completion, error) {
	var out struct {
		Choices []struct {
			Message      wireMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage wireUsage `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return completion{}, fmt.Errorf("llm: decode: %w", err)
	}
	if len(out.Choices) == 0 {
		return completion{}, fmt.Errorf("llm: empty response")
	}
	return completion{message: out.Choices[0].Message, finish: out.Choices[0].FinishReason, usage: out.Usage}, nil
}

// readStream puts a streamed completion back together: text pieces in order, and each tool call
// from its pieces (keyed by index: the id and name come first, the arguments in parts, and provider
// extensions such as Gemini's thought signature on any of them). Servers that leave the index out
// (it then reads 0) start each new call with its id.
func readStream(body io.Reader) (completion, error) {
	var out completion
	var text strings.Builder
	sawText, sawChoice := false, false
	var calls []*wireToolCall
	byIndex := map[int]*wireToolCall{}
	r := &io.LimitedReader{R: body, N: maxStream + 1}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   *string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
						ExtraContent json.RawMessage `json:"extra_content,omitempty"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *wireUsage      `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			if r.N <= 0 { // the cap cut this chunk short
				break
			}
			return out, fmt.Errorf("llm: decode stream: %w", err)
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			// A 200 stream can still end in an error chunk (OpenRouter does); an overflow is still one.
			if llm.IsOverflowText(data) {
				return out, fmt.Errorf("llm: %w: %.400s", llm.ErrContextOverflow, data)
			}
			return out, fmt.Errorf("llm: stream error: %.400s", chunk.Error)
		}
		if chunk.Usage != nil {
			out.usage, out.sawUsage = *chunk.Usage, true
		}
		for _, ch := range chunk.Choices {
			sawChoice = true
			if ch.Delta.Content != nil {
				text.WriteString(*ch.Delta.Content)
				sawText = true
			}
			for _, d := range ch.Delta.ToolCalls {
				tc := byIndex[d.Index]
				if tc == nil || d.ID != "" && tc.ID != "" && d.ID != tc.ID {
					tc = &wireToolCall{Type: "function"}
					byIndex[d.Index] = tc
					calls = append(calls, tc)
				}
				if d.ID != "" {
					tc.ID = d.ID
				}
				if d.Type != "" {
					tc.Type = d.Type
				}
				tc.Function.Name += d.Function.Name
				tc.Function.Arguments += d.Function.Arguments
				if len(d.ExtraContent) > 0 && string(d.ExtraContent) != "null" {
					tc.ExtraContent = d.ExtraContent
				}
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				out.finish = *ch.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("llm: read stream: %w", err)
	}
	if r.N <= 0 {
		return out, fmt.Errorf("llm: the answer stream is over %d MB", maxStream>>20)
	}
	if !sawChoice {
		return out, fmt.Errorf("llm: empty response")
	}
	if sawText {
		t := text.String()
		out.message.Content = &t
	}
	for _, tc := range calls {
		out.message.ToolCalls = append(out.message.ToolCalls, *tc)
	}
	return out, nil
}

// endpoint returns the Chat Completions URL. Azure's legacy API addresses a deployment by path
// with an api-version query; every other provider (and Azure's v1 API) uses {base}/chat/completions.
func (c *Client) endpoint() string {
	if c.provider == "azure" && c.apiVersion != "" {
		return fmt.Sprintf("%s/deployments/%s/chat/completions?api-version=%s",
			c.baseURL, url.PathEscape(c.model), url.QueryEscape(c.apiVersion))
	}
	return c.baseURL + "/chat/completions"
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// isContextOverflow recognizes how OpenAI-compatible servers refuse a prompt that doesn't fit the
// model's window: a 400 (Ollama answers 500) whose body says so. A bare 413 is usually a proxy's
// body limit and keeps its own text.
func isContextOverflow(status int, body string) bool {
	return (status == http.StatusBadRequest || status == http.StatusInternalServerError) && llm.IsOverflowText(body)
}
