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
}

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
		Messages:  toParams(req.Messages),
	}
	// Prompt caching. The history is append-only, so every request repeats the previous one and adds
	// to the end: one breakpoint after the fixed prefix (tools, then system) and one on the newest
	// user turn make each turn read everything before it from the cache.
	if req.System != "" {
		params.System = []sdk.BetaTextBlockParam{{Text: req.System, CacheControl: sdk.NewBetaCacheControlEphemeralParam()}}
	}
	if c.effort != "" {
		params.OutputConfig = sdk.BetaOutputConfigParam{Effort: sdk.BetaOutputConfigEffort(c.effort)}
	}
	for _, t := range req.Tools {
		params.Tools = append(params.Tools, sdk.BetaToolUnionParam{OfTool: toolParam(t)})
	}
	if req.System == "" && len(params.Tools) > 0 {
		params.Tools[len(params.Tools)-1].OfTool.CacheControl = sdk.NewBetaCacheControlEphemeralParam()
	}
	cacheNewestUserTurn(params.Messages)
	params.Betas = append([]sdk.AnthropicBeta{}, c.betas...)
	if c.serverFallback {
		params.Betas = append(params.Betas, sdk.AnthropicBetaServerSideFallback2026_07_01)
		params.Fallbacks = sdk.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}

	resp, err := c.messages.New(ctx, params, c.callOpts...)
	if err != nil {
		var apiErr *sdk.Error
		if errors.As(err, &apiErr) {
			if apiErr.StatusCode == 413 || apiErr.StatusCode == 400 && strings.Contains(apiErr.Error(), "prompt is too long") {
				return nil, fmt.Errorf("anthropic: %w: HTTP %d: %s", llm.ErrContextOverflow, apiErr.StatusCode, apiErr.Error())
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

// cacheNewestUserTurn sets a cache breakpoint on the last block of the newest user turn. User turns
// are built afresh by toParams on every call; assistant turns replay blocks held in ProviderState,
// which a breakpoint would write into.
func cacheNewestUserTurn(msgs []sdk.BetaMessageParam) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != sdk.BetaMessageParamRoleUser || len(msgs[i].Content) == 0 {
			continue
		}
		if cc := msgs[i].Content[len(msgs[i].Content)-1].GetCacheControl(); cc != nil {
			*cc = sdk.NewBetaCacheControlEphemeralParam()
		}
		return
	}
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
