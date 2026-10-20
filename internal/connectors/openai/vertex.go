package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/llm"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// Overridable in tests.
var (
	findDefaultCredentials = google.FindDefaultCredentials
	credentialsFromJSON    = google.CredentialsFromJSON //nolint:staticcheck // key supplied by the operator
)

func init() {
	connector.Register(connector.Spec{
		Type:        "gemini_vertex",
		Kind:        connector.KindLLM,
		Name:        "Gemini on Google Vertex AI",
		Description: "Gemini models through Vertex AI's OpenAI-compatible endpoint. Uses Application Default Credentials (e.g. `gcloud auth application-default login`) unless a service account key is given. Requires a GCP project with billing enabled.",
		Fields: []connector.Field{
			{Key: "project_id", Label: "GCP project ID", Type: connector.FieldString, Required: true},
			{Key: "region", Label: "Region", Type: connector.FieldString, Default: "global", Help: "global (recommended) or a region such as us-central1"},
			{Key: "model", Label: "Model", Type: connector.FieldString, Required: true, Placeholder: "google/gemini-2.5-pro",
				Help: "Vertex model ID with the google/ prefix (added automatically if missing)"},
			{Key: "service_account_json", Label: "Service account key (JSON)", Type: connector.FieldText, Secret: true,
				Help: "Blank uses Application Default Credentials (GOOGLE_APPLICATION_CREDENTIALS, gcloud application-default login, metadata server)"},
			{Key: "max_tokens", Label: "Max output tokens", Type: connector.FieldNumber, Default: 8000},
			contextWindowField,
			{Key: "temperature", Label: "Temperature", Type: connector.FieldString, Help: "Optional, e.g. 0.7"},
			{Key: "base_url", Label: "Endpoint override", Type: connector.FieldString,
				Help: "Optional, e.g. a Private Service Connect endpoint. Must end in .../endpoints/openapi"},
		},
		DefaultName: func(cfg connector.Config) string {
			return connector.WithDetail("Gemini on Vertex", strings.TrimPrefix(cfg.String("model"), "google/"))
		},
	}, newVertexGemini)
}

// vertexBaseURL is Vertex AI's OpenAI-compatible base for a project and location.
func vertexBaseURL(project, region string) string {
	host := "aiplatform.googleapis.com"
	if region != "global" {
		host = region + "-aiplatform.googleapis.com"
	}
	return fmt.Sprintf("https://%s/v1/projects/%s/locations/%s/endpoints/openapi", host, project, region)
}

func newVertexGemini(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	// Token fetches use the connector's HTTP client (timeouts) instead of the default one.
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, env.HTTPClient)
	var (
		creds *google.Credentials
		err   error
	)
	if js := cfg.String("service_account_json"); js != "" {
		creds, err = credentialsFromJSON(ctx, []byte(js), cloudPlatformScope)
	} else {
		creds, err = findDefaultCredentials(ctx, cloudPlatformScope)
	}
	if err != nil {
		return nil, fmt.Errorf("vertex: credentials: %w (run `gcloud auth application-default login` or provide a service account key)", err)
	}

	project := cfg.String("project_id")
	region := cfg.String("region")
	if region == "" {
		region = "global"
	}
	model := cfg.String("model")
	if !strings.Contains(model, "/") {
		model = "google/" + model
	}
	base := strings.TrimRight(cfg.String("base_url"), "/")
	if base == "" {
		base = vertexBaseURL(project, region)
	}
	c := &Client{
		provider:    "gemini_vertex",
		baseURL:     base,
		model:       model,
		temperature: cfg.String("temperature"),
		maxTokens:   cfg.Int("max_tokens", 8000),
		headers:     map[string]string{},
		http:        llm.HTTPClient(env.HTTPClient, 0),
		tokens:      oauth2.ReuseTokenSource(nil, creds.TokenSource),
		logger:      env.Logger,
	}
	c.contextWindow = int64(cfg.Int("context_window", 0))
	// gcloud user credentials have no project of their own; Vertex needs the billing/quota
	// project named explicitly.
	if credentialType(creds) == "authorized_user" {
		c.headers["x-goog-user-project"] = project
	}
	return c, nil
}

func credentialType(creds *google.Credentials) string {
	var meta struct {
		Type string `json:"type"`
	}
	if len(creds.JSON) > 0 {
		_ = json.Unmarshal(creds.JSON, &meta)
	}
	return meta.Type
}
