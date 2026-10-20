// Package llm defines provider-neutral chat types used by the agent tool loop. Provider plugins
// (OpenAI-compatible, Anthropic) translate to and from their own wire formats.
package llm

import (
	"context"
	"encoding/json"
	"errors"
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
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type Response struct {
	Message    Message
	StopReason string // "end", "tool_use", "max_tokens", "refusal"
	Usage      Usage
}

// Chatter is implemented by every LLM connector.
type Chatter interface {
	Chat(ctx context.Context, req Request) (*Response, error)
	Model() string
}

// ErrContextOverflow is wrapped by a connector's Chat error when the request was refused for being
// longer than the model's context window.
var ErrContextOverflow = errors.New("the conversation is longer than the model's context window")
