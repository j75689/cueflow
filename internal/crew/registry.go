package crew

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/store"
)

// Capability groups tools an agent may use. The core has four (research, delegation, planning,
// memory); modules register theirs with RegisterCapability. No capability can publish anything:
// what an agent produces waits for a human, and publishers are only reachable from a dispatch worker.
type Capability struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tools       []string `json:"tools"`
	// Prompt, when set, returns lines added to the system prompt of an agent that has this capability.
	Prompt func(a *store.Agent) string `json:"-"`
	// Default marks a capability the starter Chief of a new org gets.
	Default bool `json:"-"`
}

// Deps is what module code gets of the crew: the store, connectors, events and the log.
type Deps struct {
	Store  *store.Store
	Hub    *hub.Hub
	Broker *events.Broker
	Logger *slog.Logger
}

// Run is one agent run as a tool sees it: the task, the agent and per-run counters (a module
// limits, say, the images generated in one run by counting them here).
type Run struct {
	Deps
	Task  *store.Task
	Agent *store.Agent
	env   *runEnv
}

// Count returns how often Inc(key) was called in this run.
func (r *Run) Count(key string) int { return r.env.counts[key] }

// Inc bumps a per-run counter.
func (r *Run) Inc(key string) {
	if r.env.counts == nil {
		r.env.counts = map[string]int{}
	}
	r.env.counts[key]++
}

// ToolFunc runs a tool with the LLM's arguments and returns the result shown to it.
type ToolFunc func(ctx context.Context, run *Run, args json.RawMessage) (string, error)

// ToolSpec is a built-in tool a module adds to agents that have its capability.
type ToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
	Capability  string
	Run         ToolFunc
	// SaveLargeResults marks a tool that returns outside content: a long result is saved as a task
	// file and the model gets its start. Leave it off for tools that report CueFlow's own state.
	SaveLargeResults bool
}

// BriefFunc adds a section to the opening message of every run of a task (what a module has
// attached to the task, say). It returns "" when there is nothing to add.
type BriefFunc func(ctx context.Context, deps Deps, task *store.Task) string

var registry struct {
	mu           sync.RWMutex
	capabilities []Capability
	tools        []tool
	briefs       []BriefFunc
}

// RegisterCapability adds a capability to the catalog. Modules call it at startup, before agents run.
func RegisterCapability(c Capability) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for i := range registry.capabilities {
		if registry.capabilities[i].ID == c.ID {
			registry.capabilities[i] = c
			return
		}
	}
	registry.capabilities = append(registry.capabilities, c)
}

// RegisterTool adds a tool. Its capability must be registered too, or no agent will ever see it.
func RegisterTool(t ToolSpec) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.tools = append(registry.tools, tool{name: t.Name, description: t.Description, schema: t.Schema,
		capability: t.Capability, saveLarge: t.SaveLargeResults, run: func(ctx context.Context, e *runEnv, args json.RawMessage) (string, error) {
			return t.Run(ctx, e.run(), args)
		}})
}

// RegisterBrief adds a section to every run's opening message.
func RegisterBrief(fn BriefFunc) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.briefs = append(registry.briefs, fn)
}

// Capabilities returns the capability catalog for the UI: the core ones, then the modules' in
// registration order, each with its tools.
func Capabilities() []Capability {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	byCap := map[string][]string{}
	for _, t := range allTools() {
		if t.capability != "" {
			byCap[t.capability] = append(byCap[t.capability], t.name)
		}
	}
	out := make([]Capability, 0, len(coreCapabilities)+len(registry.capabilities))
	for _, c := range append(append([]Capability{}, coreCapabilities...), registry.capabilities...) {
		c.Tools = byCap[c.ID]
		if c.Tools == nil {
			c.Tools = []string{}
		}
		out = append(out, c)
	}
	return out
}

// DefaultCapabilities is what the starter Chief gets: the core set plus the modules' defaults.
func DefaultCapabilities() []string {
	out := []string{"planning", "delegation", "memory", "research"}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for _, c := range registry.capabilities {
		if c.Default {
			out = append(out, c.ID)
		}
	}
	return out
}

func validCapability(id string) bool {
	for _, c := range Capabilities() {
		if c.ID == id {
			return true
		}
	}
	return false
}

// allTools is the core tools followed by the registered ones. Callers hold registry.mu.
func allTools() []tool {
	return append(append([]tool{}, coreTools...), registry.tools...)
}

func briefs() []BriefFunc {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return append([]BriefFunc{}, registry.briefs...)
}

// capabilityPrompts returns the prompt additions of the capabilities the agent has, in catalog order.
func capabilityPrompts(a *store.Agent) []string {
	var out []string
	for _, c := range Capabilities() {
		if c.Prompt != nil && a.Has(c.ID) {
			if p := c.Prompt(a); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
