// Package anthropic is the LLM connector for Claude via the official Anthropic Go SDK.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/llm"
)

const defaultModel = "claude-opus-5-5"

// fallbackModels accept the server-side refusal fallback ("fallbacks": "default").
var fallbackModels = map[string]bool{
	"claude-fable-5-1": true, "claude-opus-5-5": true, "claude-opus-5": true, "claude-sonnet-5-5": true,
}

func init() {
	connector.Register(connector.Spec{
		Type:        "anthropic",
		Kind:        connector.KindLLM,
		Name:        "Anthropic Claude",
		Description: "Claude models through the Anthropic API.",
		Fields: []connector.Field{
			{Key: "api_key", Label: "API key", Type: connector.FieldPassword, Secret: true,
				Help: "Blank uses ANTHROPIC_API_KEY (or an `ant auth login` profile) from the server environment"},
			{Key: "model", Label: "Model", Type: connector.FieldString, Default: defaultModel},
			{Key: "effort", Label: "Effort", Type: connector.FieldSelect, Options: []string{"low", "medium", "high", "xhigh", "max"}, Default: "high"},
			{Key: "max_tokens", Label: "Max output tokens", Type: connector.FieldNumber, Default: 16000},
			{Key: "server_fallback", Label: "Server-side refusal fallback", Type: connector.FieldBool, Default: true,
				Help: "If a request is declined by a safety classifier, let the API re-serve it with a fallback model"},
			{Key: "base_url", Label: "Base URL", Type: connector.FieldString, Help: "Optional proxy/gateway URL"},
		},
	}, New)
}

type Client struct {
	client    sdk.Client
	model     string
	effort    string
	maxTokens int
	fallback  bool
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	opts := []option.RequestOption{option.WithHTTPClient(env.HTTPClient), option.WithMaxRetries(3)}
	if k := cfg.String("api_key"); k != "" {
		opts = append(opts, option.WithAPIKey(k))
	}
	if u := cfg.String("base_url"); u != "" {
		opts = append(opts, option.WithBaseURL(u))
	}
	model := cfg.String("model")
	if model == "" {
		model = defaultModel
	}
	return &Client{
		client:    sdk.NewClient(opts...),
		model:     model,
		effort:    cfg.String("effort"),
		maxTokens: cfg.Int("max_tokens", 16000),
		fallback:  cfg.Bool("server_fallback") && fallbackModels[model],
	}, nil
}

func (c *Client) Model() string { return c.model }

func (c *Client) Test(ctx context.Context) (*connector.TestResult, error) {
	resp, err := c.Chat(ctx, llm.Request{
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: "Reply with the single word: pong"}},
		MaxTokens: 1024,
	})
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	return &connector.TestResult{OK: true, Message: fmt.Sprintf("%s replied: %.60s", c.model, strings.TrimSpace(resp.Message.Content))}, nil
}

func (c *Client) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = c.maxTokens
	}
	params := sdk.BetaMessageNewParams{
		Model:     c.model,
		MaxTokens: int64(maxTokens),
		Messages:  toParams(req.Messages),
	}
	if req.System != "" {
		params.System = []sdk.BetaTextBlockParam{{Text: req.System}}
	}
	if c.effort != "" {
		params.OutputConfig = sdk.BetaOutputConfigParam{Effort: sdk.BetaOutputConfigEffort(c.effort)}
	}
	for _, t := range req.Tools {
		params.Tools = append(params.Tools, sdk.BetaToolUnionParam{OfTool: toolParam(t)})
	}
	if c.fallback {
		params.Betas = []sdk.AnthropicBeta{sdk.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = sdk.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}

	resp, err := c.client.Beta.Messages.New(ctx, params)
	if err != nil {
		var apiErr *sdk.Error
		if errors.As(err, &apiErr) {
			return nil, fmt.Errorf("anthropic: HTTP %d: %s", apiErr.StatusCode, apiErr.Error())
		}
		return nil, fmt.Errorf("anthropic: %w", err)
	}

	msg := llm.Message{Role: llm.RoleAssistant, ProviderState: resp.ToParam()}
	var text []string
	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case sdk.BetaTextBlock:
			text = append(text, b.Text)
		case sdk.BetaToolUseBlock:
			args := json.RawMessage(b.JSON.Input.Raw())
			if !json.Valid(args) {
				args, _ = json.Marshal(b.Input)
			}
			msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{ID: b.ID, Name: b.Name, Arguments: args})
		}
	}
	msg.Content = strings.Join(text, "\n")

	stop := "end"
	switch resp.StopReason {
	case sdk.BetaStopReasonToolUse:
		stop = "tool_use"
	case sdk.BetaStopReasonMaxTokens:
		stop = "max_tokens"
	case sdk.BetaStopReasonRefusal:
		return nil, fmt.Errorf("anthropic: request declined (refusal, category %q)", resp.StopDetails.Category)
	}
	return &llm.Response{Message: msg, StopReason: stop,
		Usage: llm.Usage{InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens}}, nil
}

func toolParam(t llm.Tool) *sdk.BetaToolParam {
	schema := sdk.BetaToolInputSchemaParam{ExtraFields: map[string]any{}}
	for k, v := range t.Schema {
		switch k {
		case "type":
		case "properties":
			schema.Properties = v
		case "required":
			if req, ok := v.([]string); ok {
				schema.Required = req
			} else if req, ok := v.([]any); ok {
				for _, r := range req {
					if s, ok := r.(string); ok {
						schema.Required = append(schema.Required, s)
					}
				}
			}
		default:
			schema.ExtraFields[k] = v
		}
	}
	return &sdk.BetaToolParam{Name: t.Name, Description: param.NewOpt(t.Description), InputSchema: schema}
}

// toParams converts neutral messages. Assistant turns produced by this provider are replayed
// verbatim from ProviderState (keeps thinking blocks intact); consecutive tool results are merged
// into one user turn as the API requires.
func toParams(msgs []llm.Message) []sdk.BetaMessageParam {
	var out []sdk.BetaMessageParam
	var pending []sdk.BetaContentBlockParamUnion
	flush := func() {
		if len(pending) > 0 {
			out = append(out, sdk.NewBetaUserMessage(pending...))
			pending = nil
		}
	}
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleTool:
			content := m.Content
			if content == "" {
				content = "(empty)"
			}
			pending = append(pending, sdk.NewBetaToolResultBlock(m.ToolCallID, content, m.IsError))
		case llm.RoleAssistant:
			flush()
			if p, ok := m.ProviderState.(sdk.BetaMessageParam); ok {
				out = append(out, p)
				continue
			}
			var blocks []sdk.BetaContentBlockParamUnion
			if m.Content != "" {
				blocks = append(blocks, sdk.NewBetaTextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, sdk.NewBetaToolUseBlock(tc.ID, tc.Arguments, tc.Name))
			}
			out = append(out, sdk.BetaMessageParam{Role: sdk.BetaMessageParamRoleAssistant, Content: blocks})
		default:
			flush()
			out = append(out, sdk.NewBetaUserMessage(sdk.NewBetaTextBlock(m.Content)))
		}
	}
	flush()
	return out
}
