// Package connector is CueFlow's plugin system. Every integration - publishers (Postiz),
// sources (RSS, IMAP), search engines, LLM providers and notifiers - is a connector type that
// registers a Spec (its config form) and a Factory. Adding an integration means adding one package
// under internal/connectors and blank-importing it from internal/connectors/all.
package connector

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/llm"
)

type Kind string

const (
	KindPublisher Kind = "publisher"
	KindSource    Kind = "source"
	KindSearch    Kind = "search"
	KindLLM       Kind = "llm"
	KindNotifier  Kind = "notifier"
	// KindRuntime describes how to launch an external agent (spoken to over ACP) that can work
	// tasks instead of the built-in LLM loop.
	KindRuntime Kind = "runtime"
)

func (k Kind) Valid() bool {
	switch k {
	case KindPublisher, KindSource, KindSearch, KindLLM, KindNotifier, KindRuntime:
		return true
	}
	return false
}

// Instance is a configured, ready-to-use connector.
type Instance interface {
	// Test verifies connectivity and credentials.
	Test(ctx context.Context) (*TestResult, error)
}

type TestResult struct {
	OK      bool           `json:"ok"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Publisher hands an approved post to a social platform (directly or via a driver like Postiz).
// Implementations never need to check authorization themselves: Build wraps every publisher so
// Publish fails unless the context carries a dispatch grant for req.PostID (see package guard).
type Publisher interface {
	Instance
	Publish(ctx context.Context, req PublishRequest) (*PublishResult, error)
}

type PublishRequest struct {
	PostID      uuid.UUID
	Title       string
	Platforms   []string
	Parts       []string // ordered thread parts; a single element for a plain post
	Hashtags    []string
	MediaURLs   []string
	ScheduledAt *time.Time // nil = publish now
}

type PublishResult struct {
	ExternalIDs map[string]string `json:"external_ids"` // platform -> post id
	Details     map[string]any    `json:"details,omitempty"`
}

// Source produces raw material for the agent.
type Source interface {
	Instance
	Fetch(ctx context.Context, opts FetchOptions) ([]Item, error)
}

type FetchOptions struct {
	Since time.Time
	Limit int
}

type Item struct {
	ExternalID  string
	Title       string
	URL         string
	Author      string
	Content     string
	PublishedAt *time.Time
}

// Searcher answers ad-hoc web queries for the agent.
type Searcher interface {
	Instance
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
}

type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Date    string `json:"date,omitempty"`
}

// LLM is a chat model provider.
type LLM interface {
	Instance
	llm.Chatter
}

// Notifier delivers messages to the operator (e.g. "3 drafts waiting for review").
type Notifier interface {
	Instance
	Notify(ctx context.Context, n Notification) error
}

type Notification struct {
	Subject string
	Text    string
	HTML    string
}

// Runtime launches an external agent process that speaks the Agent Client Protocol on stdio.
type Runtime interface {
	Instance
	Launch() RuntimeLaunch
}

type RuntimeLaunch struct {
	Command string
	Args    []string
	// Env is appended to the server's environment for the agent process (KEY=VALUE).
	Env []string
	// Timeout bounds one agent run.
	Timeout time.Duration
	// Model, when set, is requested from the agent (if it supports choosing one).
	Model string
}

// ToolLister is implemented by MCP-backed connectors so the UI can show their tools.
type ToolLister interface {
	ListTools(ctx context.Context) ([]ToolInfo, error)
}

type ToolInfo struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	AgentAllowed bool   `json:"agent_allowed"`
	Reason       string `json:"reason,omitempty"`
}

// StateStore lets a connector persist a small cursor (e.g. IMAP UIDVALIDITY/last UID).
type StateStore interface {
	Load(ctx context.Context) (map[string]any, error)
	Save(ctx context.Context, state map[string]any) error
}

// Env is what the host provides to a connector instance.
type Env struct {
	ID         uuid.UUID
	Name       string
	Logger     *slog.Logger
	HTTPClient *http.Client
	State      StateStore
}

type Factory func(cfg Config, env Env) (Instance, error)

type registration struct {
	spec    Spec
	factory Factory
}

var (
	mu       sync.RWMutex
	registry = map[string]registration{}
)

// Register adds a connector type. It panics on programmer errors (duplicate type, bad kind),
// which surface at startup because registration happens in init().
func Register(spec Spec, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if spec.Type == "" || !spec.Kind.Valid() || f == nil {
		panic(fmt.Sprintf("connector: invalid registration %q (kind %q)", spec.Type, spec.Kind))
	}
	if _, dup := registry[spec.Type]; dup {
		panic(fmt.Sprintf("connector: type %q registered twice", spec.Type))
	}
	registry[spec.Type] = registration{spec: spec, factory: f}
}

func Lookup(typ string) (Spec, bool) {
	mu.RLock()
	defer mu.RUnlock()
	r, ok := registry[typ]
	return r.spec, ok
}

// Specs returns every registered type ordered by kind, then name.
func Specs() []Spec {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Spec, 0, len(registry))
	for _, r := range registry {
		out = append(out, r.spec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Build instantiates a connector from its merged (config + decrypted secrets) settings.
func Build(typ string, cfg Config, env Env) (Instance, error) {
	mu.RLock()
	r, ok := registry[typ]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("connector: unknown type %q", typ)
	}
	cfg = r.spec.WithDefaults(cfg)
	if err := r.spec.Validate(cfg); err != nil {
		return nil, err
	}
	if env.Logger == nil {
		env.Logger = slog.Default()
	}
	if env.HTTPClient == nil {
		env.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	env.Logger = env.Logger.With("connector", env.Name, "type", typ)
	inst, err := r.factory(cfg, env)
	if err != nil {
		return nil, err
	}
	if err := checkKind(r.spec.Kind, inst); err != nil {
		return nil, fmt.Errorf("connector %q: %w", typ, err)
	}
	if p, ok := inst.(Publisher); ok && r.spec.Kind == KindPublisher {
		return &guardedPublisher{Publisher: p}, nil
	}
	return inst, nil
}

func checkKind(k Kind, inst Instance) error {
	var ok bool
	switch k {
	case KindPublisher:
		_, ok = inst.(Publisher)
	case KindSource:
		_, ok = inst.(Source)
	case KindSearch:
		_, ok = inst.(Searcher)
	case KindLLM:
		_, ok = inst.(LLM)
	case KindNotifier:
		_, ok = inst.(Notifier)
	case KindRuntime:
		_, ok = inst.(Runtime)
	}
	if !ok {
		return fmt.Errorf("instance does not implement the %s interface", k)
	}
	return nil
}

// guardedPublisher is the single choke point for FR-4.2.
type guardedPublisher struct {
	Publisher
}

func (g *guardedPublisher) Publish(ctx context.Context, req PublishRequest) (*PublishResult, error) {
	if err := guard.CheckPublish(ctx, req.PostID); err != nil {
		return nil, err
	}
	return g.Publisher.Publish(ctx, req)
}

// ListTools forwards to the wrapped publisher when it is MCP-backed.
func (g *guardedPublisher) ListTools(ctx context.Context) ([]ToolInfo, error) {
	if tl, ok := g.Publisher.(ToolLister); ok {
		return tl.ListTools(ctx)
	}
	return nil, fmt.Errorf("connector does not expose tools")
}

// Close forwards to the wrapped publisher when it holds resources.
func (g *guardedPublisher) Close() error {
	if c, ok := g.Publisher.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}
