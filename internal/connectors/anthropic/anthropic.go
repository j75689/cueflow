// Package anthropic provides the Claude LLM connectors, all built on the official Anthropic Go SDK:
//
//   - anthropic          Claude API: API key, OAuth/Bearer token, or an `ant auth login` profile
//   - anthropic_bedrock  Claude on Amazon Bedrock (Mantle endpoint): AWS credentials or a Bedrock API key
//   - anthropic_vertex   Claude on Google Vertex AI: Application Default Credentials or a service account
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/lib/betafallback"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/anthropics/anthropic-sdk-go/vertex"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/llm"
)

const defaultModel = "claude-opus-5-5"

// fallbackModels support refusal fallbacks. On the Claude API the server routes the fallback
// ("fallbacks": "default"); on Bedrock and Vertex the SDK's client-side middleware retries a
// declined request on clientFallbackModel.
var fallbackModels = map[string]bool{
	"claude-fable-5-1": true, "claude-opus-5-5": true, "claude-opus-5": true, "claude-sonnet-5-5": true,
}

const clientFallbackModel = "claude-opus-4-8"

// oauthBeta must accompany Claude API requests authenticated with an OAuth access token.
const oauthBeta = sdk.AnthropicBeta("oauth-2025-04-20")

func commonFields(modelDefault, modelHelp string) []connector.Field {
	return []connector.Field{
		{Key: "model", Label: "Model", Type: connector.FieldString, Default: modelDefault, Help: modelHelp},
		{Key: "effort", Label: "Effort", Type: connector.FieldSelect, Options: []string{"low", "medium", "high", "xhigh", "max"}, Default: "high"},
		{Key: "max_tokens", Label: "Max output tokens", Type: connector.FieldNumber, Default: 16000},
		{Key: "refusal_fallback", Label: "Refusal fallback", Type: connector.FieldBool, Default: true,
			Help: "If a request is declined by a safety classifier, re-serve it with a fallback model"},
	}
}

func init() {
	connector.Register(connector.Spec{
		Type:        "anthropic",
		Kind:        connector.KindLLM,
		Name:        "Anthropic Claude",
		Description: "Claude models through the Anthropic API. Authenticate with an API key, an OAuth/Bearer token, or an `ant auth login` profile.",
		Fields: append([]connector.Field{
			{Key: "api_key", Label: "API key", Type: connector.FieldPassword, Secret: true,
				Help: "Takes precedence over the token and profile"},
			{Key: "auth_token", Label: "Auth token", Type: connector.FieldPassword, Secret: true,
				Help: "OAuth access token or gateway Bearer token, sent as Authorization: Bearer"},
			{Key: "auth_token_type", Label: "Token type", Type: connector.FieldSelect, Options: []string{"oauth", "bearer"}, Default: "oauth",
				Help: "oauth adds the OAuth beta header required by the Claude API; bearer sends the token as-is (gateways)"},
			{Key: "profile", Label: "Profile", Type: connector.FieldString,
				Help: "Name of an `ant auth login` profile on the server. With no key, token or profile, the server environment is used (ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_PROFILE, default profile)"},
			{Key: "base_url", Label: "Base URL", Type: connector.FieldString, Help: "Optional proxy/gateway URL"},
		}, commonFields(defaultModel, "")...),
		DefaultName: func(cfg connector.Config) string { return connector.WithDetail("Claude", cfg.String("model")) },
	}, newFirstParty)

	connector.Register(connector.Spec{
		Type:        "anthropic_bedrock",
		Kind:        connector.KindLLM,
		Name:        "Claude on Amazon Bedrock",
		Description: "Claude through Amazon Bedrock. Leave credentials blank to use the server's default AWS credential chain (env vars, ~/.aws, IAM role).",
		Fields: append([]connector.Field{
			{Key: "region", Label: "AWS region", Type: connector.FieldString, Required: true, Placeholder: "us-east-1"},
			{Key: "aws_profile", Label: "AWS profile", Type: connector.FieldString, Help: "Named profile from the server's AWS config"},
			{Key: "aws_access_key_id", Label: "AWS access key ID", Type: connector.FieldString},
			{Key: "aws_secret_access_key", Label: "AWS secret access key", Type: connector.FieldPassword, Secret: true},
			{Key: "aws_session_token", Label: "AWS session token", Type: connector.FieldPassword, Secret: true, Help: "Only for temporary credentials"},
			{Key: "api_key", Label: "Bedrock API key", Type: connector.FieldPassword, Secret: true, Help: "Alternative to AWS credentials; takes precedence"},
			{Key: "base_url", Label: "Base URL", Type: connector.FieldString, Help: "Optional endpoint override"},
		}, commonFields("anthropic."+defaultModel, "Bedrock model IDs take an anthropic. prefix")...),
		DefaultName: func(cfg connector.Config) string {
			return connector.WithDetail("Claude on Bedrock", strings.TrimPrefix(cfg.String("model"), "anthropic."))
		},
	}, newBedrock)

	connector.Register(connector.Spec{
		Type:        "anthropic_vertex",
		Kind:        connector.KindLLM,
		Name:        "Claude on Google Vertex AI",
		Description: "Claude through Google Cloud Vertex AI. Leave the service account blank to use Application Default Credentials.",
		Fields: append([]connector.Field{
			{Key: "project_id", Label: "GCP project ID", Type: connector.FieldString, Required: true},
			{Key: "region", Label: "Region", Type: connector.FieldString, Default: "global", Help: "global (recommended), us, eu, or a specific region"},
			{Key: "service_account_json", Label: "Service account key (JSON)", Type: connector.FieldText, Secret: true,
				Help: "Blank uses Application Default Credentials (GOOGLE_APPLICATION_CREDENTIALS, gcloud, metadata server)"},
		}, commonFields(defaultModel, "")...),
		DefaultName: func(cfg connector.Config) string {
			return connector.WithDetail("Claude on Vertex", cfg.String("model"))
		},
	}, newVertex)
}

type Client struct {
	messages  *sdk.BetaMessageService
	callOpts  []option.RequestOption
	betas     []sdk.AnthropicBeta
	model     string
	effort    string
	maxTokens int
	// serverFallback uses the Claude API's server-side fallback routing.
	serverFallback bool
	// noServerClear is set once the endpoint has turned down server-side clearing of old tool results
	// (see Chat); the connector then sheds them itself.
	noServerClear atomic.Bool
}

// ShedsToolHistory marks the connector as an llm.HistoryShedder: crew must not cut the history it
// sends, or the thinking blocks after the cut stop matching the turns before them.
func (c *Client) ShedsToolHistory() {}

func modelOf(cfg connector.Config, def string) string {
	if m := cfg.String("model"); m != "" {
		return m
	}
	return def
}

// baseModel strips a platform prefix ("anthropic.claude-opus-5-5" -> "claude-opus-5-5").
func baseModel(m string) string { return strings.TrimPrefix(m, "anthropic.") }

func newClient(cfg connector.Config, model string) *Client {
	return &Client{model: model, effort: cfg.String("effort"), maxTokens: cfg.Int("max_tokens", 16000)}
}

func newFirstParty(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	opts := []option.RequestOption{option.WithHTTPClient(env.HTTPClient), option.WithMaxRetries(3)}
	c := newClient(cfg, modelOf(cfg, defaultModel))
	switch {
	case cfg.String("api_key") != "":
		opts = append(opts, option.WithAPIKey(cfg.String("api_key")))
	case cfg.String("auth_token") != "":
		opts = append(opts, option.WithAuthToken(cfg.String("auth_token")))
		if cfg.String("auth_token_type") != "bearer" {
			c.betas = append(c.betas, oauthBeta)
		}
	case cfg.String("profile") != "":
		opts = append(opts, option.WithProfile(cfg.String("profile")))
	}
	if u := cfg.String("base_url"); u != "" {
		opts = append(opts, option.WithBaseURL(u))
	}
	client := sdk.NewClient(opts...)
	c.messages = &client.Beta.Messages
	c.serverFallback = cfg.Bool("refusal_fallback") && fallbackModels[c.model]
	return c, nil
}

// clientFallback returns the SDK middleware that retries refused requests on fallbackModel
// (used where server-side fallback routing is unavailable: Bedrock and Vertex).
func clientFallback(cfg connector.Config, model, fallbackModel string) ([]option.RequestOption, []option.RequestOption) {
	if !cfg.Bool("refusal_fallback") || !fallbackModels[baseModel(model)] {
		return nil, nil
	}
	mw := option.WithMiddleware(betafallback.BetaRefusalFallbackMiddleware([]sdk.BetaFallbackParam{{Model: fallbackModel}}))
	// One state per connector instance (instances live for one pipeline/agent run), so a fallback
	// sticks for the rest of that conversation.
	state := betafallback.WithBetaFallbackState(&betafallback.BetaFallbackState{})
	return []option.RequestOption{mw}, []option.RequestOption{state}
}

func newBedrock(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	model := modelOf(cfg, "anthropic."+defaultModel)
	c := newClient(cfg, model)
	// The fallback middleware must be registered before the platform transform.
	mw, state := clientFallback(cfg, model, "anthropic."+clientFallbackModel)
	opts := append(mw, option.WithHTTPClient(env.HTTPClient), option.WithMaxRetries(3))
	mc, err := bedrock.NewMantleClient(context.Background(), bedrock.MantleClientConfig{
		APIKey:             cfg.String("api_key"),
		AWSAccessKey:       cfg.String("aws_access_key_id"),
		AWSSecretAccessKey: cfg.String("aws_secret_access_key"),
		AWSSessionToken:    cfg.String("aws_session_token"),
		AWSProfile:         cfg.String("aws_profile"),
		AWSRegion:          cfg.String("region"),
		BaseURL:            cfg.String("base_url"),
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("bedrock: %w", err)
	}
	c.messages = &mc.Beta.Messages
	c.callOpts = state
	return c, nil
}

func newVertex(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	// Token fetches use the connector's HTTP client (timeouts, proxies) instead of the default one.
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, env.HTTPClient)
	const scope = "https://www.googleapis.com/auth/cloud-platform"
	var (
		creds *google.Credentials
		err   error
	)
	if js := cfg.String("service_account_json"); js != "" {
		creds, err = google.CredentialsFromJSON(ctx, []byte(js), scope) //nolint:staticcheck // key supplied by the operator
	} else {
		creds, err = google.FindDefaultCredentials(ctx, scope)
	}
	if err != nil {
		return nil, fmt.Errorf("vertex: credentials: %w", err)
	}
	region := cfg.String("region")
	if region == "" {
		region = "global"
	}
	model := modelOf(cfg, defaultModel)
	c := newClient(cfg, model)
	mw, state := clientFallback(cfg, model, clientFallbackModel)
	opts := append(mw, option.WithMaxRetries(3), vertex.WithCredentials(ctx, region, cfg.String("project_id"), creds))
	client := sdk.NewClient(opts...)
	c.messages = &client.Beta.Messages
	c.callOpts = state
	return c, nil
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
	}
	serverClear := req.ToolHistoryBudget > 0 && !c.noServerClear.Load()
	if req.ToolHistoryBudget > 0 && !serverClear {
		params.Messages = toParams(trimDroppingThinking(req.Messages, req.ToolHistoryBudget))
	} else {
		params.Messages = toParams(req.Messages)
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
	params.Betas = append([]sdk.AnthropicBeta{}, c.betas...)
	if serverClear {
		params.ContextManagement = clearOldToolResults(req)
		params.Betas = append(params.Betas, sdk.AnthropicBetaContextManagement2025_06_27)
	}
	if c.serverFallback {
		params.Betas = append(params.Betas, sdk.AnthropicBetaServerSideFallback2026_07_01)
		params.Fallbacks = sdk.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}

	resp, err := c.messages.New(ctx, params, c.callOpts...)
	if err != nil {
		var apiErr *sdk.Error
		if errors.As(err, &apiErr) {
			if serverClear && turnsDownServerClear(apiErr) {
				c.noServerClear.Store(true)
				return c.Chat(ctx, req)
			}
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

// charsPerToken turns the character budget into tokens for the API's trigger. It is a rough
// average for mixed code and prose; the trigger only needs to land near the budget.
const charsPerToken = 3

// clearOldToolResults has the API shed old tool results instead of the client cutting them: a cut
// would change the turns a thinking block is bound to and the API would reject the request. The
// trigger counts the whole prompt, so it is the budget plus this request's other content (system
// prompt, tool schemas, messages), in tokens. The newest turn's results are always kept, however
// many tools it called.
func clearOldToolResults(req llm.Request) sdk.BetaContextManagementConfigParam {
	other := utf8.RuneCountInString(req.System)
	for _, t := range req.Tools {
		schema, _ := json.Marshal(t.Schema)
		other += utf8.RuneCountInString(t.Name) + utf8.RuneCountInString(t.Description) + len(schema)
	}
	newest := 0 // tool results after the last assistant turn
	for _, m := range req.Messages {
		if m.Role == llm.RoleTool {
			newest++
			continue
		}
		newest = 0
		other += utf8.RuneCountInString(m.Content)
		for _, tc := range m.ToolCalls {
			other += len(tc.Arguments)
		}
	}
	return sdk.BetaContextManagementConfigParam{Edits: []sdk.BetaContextManagementConfigEditUnionParam{{
		OfClearToolUses20250919: &sdk.BetaClearToolUses20250919EditParam{
			Trigger: sdk.BetaClearToolUses20250919EditTriggerUnionParam{OfInputTokens: &sdk.BetaInputTokensTriggerParam{
				Value: int64((other + req.ToolHistoryBudget) / charsPerToken)}},
			Keep: sdk.BetaToolUsesKeepParam{Value: int64(max(newest, 1))},
		},
	}}}
}

// turnsDownServerClear tells whether the API (or a gateway in front of it) refused the request
// over context management, or let the prompt grow too long because it ignored it.
func turnsDownServerClear(err *sdk.Error) bool {
	if err.StatusCode != 400 {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "context_management") || strings.Contains(msg, "context-management") ||
		strings.Contains(msg, "prompt is too long")
}

// trimDroppingThinking sheds old tool results on the client, for endpoints without server-side
// clearing: the oldest results are cut as for any other connector, and every assistant turn after the
// first cut loses its thinking blocks, which no longer match the turns before them. The API accepts
// a turn without its thinking; it refuses one whose thinking was bound to different content.
func trimDroppingThinking(msgs []llm.Message, budget int) []llm.Message {
	cut := llm.TrimToolHistory(msgs, budget)
	first := -1
	for i := range cut {
		if cut[i].Content != msgs[i].Content {
			first = i
			break
		}
	}
	if first < 0 {
		return cut
	}
	for i := first + 1; i < len(cut); i++ {
		p, ok := cut[i].ProviderState.(sdk.BetaMessageParam)
		if !ok {
			continue
		}
		var kept []sdk.BetaContentBlockParamUnion
		for _, b := range p.Content {
			if b.OfThinking == nil && b.OfRedactedThinking == nil {
				kept = append(kept, b)
			}
		}
		if len(kept) > 0 && len(kept) < len(p.Content) {
			p.Content = kept
			cut[i].ProviderState = p
		}
	}
	return cut
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
