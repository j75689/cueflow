package crew

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/store"
)

// TeamVersion is the format version of a team template file.
const TeamVersion = 1

// TeamAgent is one role of a team template: everything an agent needs except the org and the
// connectors, which the install provides.
type TeamAgent struct {
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	Avatar       string   `json:"avatar,omitempty"`
	Role         string   `json:"role,omitempty"`
	Description  string   `json:"description,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
	Capabilities []string `json:"capabilities"`
	Platforms    []string `json:"platforms,omitempty"`
	Language     string   `json:"language,omitempty"`
	// MaxTurns per run; 0 means the default (12).
	MaxTurns int `json:"max_turns,omitempty"`
}

// Team is a team template: a set of roles as data, kept as a JSON file outside the code (the way
// a Grafana dashboard is), imported into an org from the Agents page or POST /agents/template and
// exported from an org with GET /agents/template. Nothing in the binary is a template; the repo
// ships examples under templates/.
type Team struct {
	Version     int         `json:"version"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Agents      []TeamAgent `json:"agents"`
}

// ParseTeam decodes and validates a template file.
func ParseTeam(data []byte) (Team, error) {
	var t Team
	if err := json.Unmarshal(data, &t); err != nil {
		return Team{}, store.Invalid("not a team template: " + err.Error())
	}
	return t, t.Validate()
}

// Validate checks the parts the store cannot: the shape of the file. Capabilities, roles and
// platforms are checked when each agent is created.
func (t Team) Validate() error {
	if t.Version != 0 && t.Version != TeamVersion {
		return store.Invalid(fmt.Sprintf("unsupported team template version %d (this server reads version %d)", t.Version, TeamVersion))
	}
	if strings.TrimSpace(t.Name) == "" {
		return store.Invalid("team template: name is required")
	}
	if len(t.Agents) == 0 {
		return store.Invalid("team template: agents is empty")
	}
	seen := map[string]bool{}
	for i, a := range t.Agents {
		if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Slug) == "" {
			return store.Invalid(fmt.Sprintf("team template: agents[%d] needs a name and a slug", i))
		}
		slug := strings.ToLower(strings.TrimSpace(a.Slug))
		if seen[slug] {
			return store.Invalid("team template: slug " + slug + " appears twice")
		}
		seen[slug] = true
	}
	return nil
}

// Agent builds the store.Agent a template role creates in org (not yet saved).
func (ta TeamAgent) Agent(org uuid.UUID) *store.Agent {
	turns := ta.MaxTurns
	if turns <= 0 {
		turns = 12
	}
	lang := strings.TrimSpace(ta.Language)
	if lang == "" {
		lang = "en"
	}
	caps := append([]string{}, ta.Capabilities...)
	if caps == nil {
		caps = []string{}
	}
	return &store.Agent{OrgID: org, Name: ta.Name, Slug: ta.Slug, Avatar: ta.Avatar, Role: ta.Role,
		Description: ta.Description, Instructions: ta.Instructions, Capabilities: caps,
		Platforms: append([]string{}, ta.Platforms...), Language: lang, MaxTurns: turns, IsActive: true}
}

// TeamFromAgents is the export: an org's agents as a template, without ids, connectors, stats or
// anything else that belongs to one install.
func TeamFromAgents(name string, agents []*store.Agent) Team {
	t := Team{Version: TeamVersion, Name: name, Agents: []TeamAgent{}}
	for _, a := range agents {
		caps := append([]string{}, a.Capabilities...)
		if caps == nil {
			caps = []string{}
		}
		t.Agents = append(t.Agents, TeamAgent{Name: a.Name, Slug: a.Slug, Avatar: a.Avatar, Role: a.Role,
			Description: a.Description, Instructions: a.Instructions, Capabilities: caps,
			Platforms: append([]string{}, a.Platforms...), Language: a.Language, MaxTurns: a.MaxTurns})
	}
	return t
}

// ApplyTeam creates the template's agents in org, skipping the slugs the org already has (so
// importing a template twice, or over a hand-made team, adds only what is missing). It returns the
// created agents and the skipped slugs.
func ApplyTeam(ctx context.Context, st *store.Store, org uuid.UUID, t Team) ([]*store.Agent, []string, error) {
	if err := t.Validate(); err != nil {
		return nil, nil, err
	}
	existing, err := st.ListAgents(ctx, org)
	if err != nil {
		return nil, nil, err
	}
	have := map[string]bool{}
	for _, a := range existing {
		have[a.Slug] = true
	}
	created, skipped := []*store.Agent{}, []string{}
	for _, ta := range t.Agents {
		slug := strings.ToLower(strings.TrimSpace(ta.Slug))
		if have[slug] {
			skipped = append(skipped, slug)
			continue
		}
		a, err := st.CreateAgent(ctx, ta.Agent(org))
		if err != nil {
			return created, skipped, fmt.Errorf("create %s: %w", slug, err)
		}
		created = append(created, a)
	}
	return created, skipped, nil
}
