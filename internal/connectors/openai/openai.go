// Package openai is the LLM connector for OpenAI and every OpenAI-compatible Chat Completions
// endpoint: DeepSeek, Ollama, vLLM, LocalAI, LM Studio, OpenRouter...
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/llm"
)

// Presets fill in base URL and model for well-known providers.
var presets = map[string]struct{ baseURL, model string }{
	"openai":            {"https://api.openai.com/v1", "gpt-5.2"},
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
		Description: "OpenAI, DeepSeek, or any local OpenAI-compatible server (Ollama, vLLM, LocalAI). The model must support tool calling.",
		Fields: []connector.Field{
			{Key: "provider", Label: "Provider", Type: connector.FieldSelect,
				Options: []string{"openai", "deepseek", "ollama", "vllm", "localai", "openai_compatible"}, Default: "openai"},
			{Key: "base_url", Label: "Base URL", Type: connector.FieldString, Help: "Leave blank to use the provider default"},
			{Key: "model", Label: "Model", Type: connector.FieldString, Help: "Leave blank to use the provider default"},
			{Key: "api_key", Label: "API key", Type: connector.FieldPassword, Secret: true, Help: "Not needed for most local servers"},
			{Key: "max_tokens", Label: "Max output tokens", Type: connector.FieldNumber, Default: 8000},
			{Key: "temperature", Label: "Temperature", Type: connector.FieldString, Help: "Optional, e.g. 0.7. Blank uses the server default."},
		},
	}, New)
}

type Client struct {
	baseURL, model, apiKey, temperature string
	maxTokens                           int
	http                                *http.Client
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	p := presets[cfg.String("provider")]
	c := &Client{
		baseURL:     strings.TrimRight(firstNonEmpty(cfg.String("base_url"), p.baseURL), "/"),
		model:       firstNonEmpty(cfg.String("model"), p.model),
		apiKey:      cfg.String("api_key"),
		temperature: cfg.String("temperature"),
		maxTokens:   cfg.Int("max_tokens", 8000),
		http:        env.HTTPClient,
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("openai: base URL is required for provider %q", cfg.String("provider"))
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
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

func (c *Client) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	msgs := make([]wireMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		s := req.System
		msgs = append(msgs, wireMessage{Role: "system", Content: &s})
	}
	for _, m := range req.Messages {
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
	if strings.Contains(c.baseURL, "api.openai.com") {
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
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	hresp, err := c.http.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}
	defer hresp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(hresp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if hresp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: HTTP %d: %.400s", hresp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out struct {
		Choices []struct {
			Message      wireMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("llm: decode: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("llm: empty response")
	}
	ch := out.Choices[0]
	msg := llm.Message{Role: llm.RoleAssistant}
	if ch.Message.Content != nil {
		msg.Content = *ch.Message.Content
	}
	for _, tc := range ch.Message.ToolCalls {
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
	case ch.FinishReason == "length":
		stop = "max_tokens"
	case ch.FinishReason == "content_filter":
		stop = "refusal"
	}
	return &llm.Response{Message: msg, StopReason: stop,
		Usage: llm.Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}}, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
