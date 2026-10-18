package crew

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/store"
)

// TeamAgent is one role of a team template: everything an agent needs except the org and the
// connectors, which the install provides.
type TeamAgent struct {
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	Avatar       string   `json:"avatar"`
	Role         string   `json:"role"`
	Description  string   `json:"description"`
	Instructions string   `json:"instructions"`
	Capabilities []string `json:"capabilities"`
	Platforms    []string `json:"platforms,omitempty"`
	// MaxTurns per run; 0 means the default (12).
	MaxTurns int `json:"max_turns,omitempty"`
}

// Team is a template team a module offers: a set of roles written from a team that actually ran,
// applied to an org at creation or later from the Agents page. Templates are data; the agents they
// create are ordinary agents the human edits afterwards.
type Team struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Agents      []TeamAgent `json:"agents"`
}

var teams struct {
	list []Team
}

// RegisterTeam adds or replaces a team template. Modules call it at startup, next to their
// capabilities.
func RegisterTeam(t Team) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for i := range teams.list {
		if teams.list[i].ID == t.ID {
			teams.list[i] = t
			return
		}
	}
	teams.list = append(teams.list, t)
}

// Teams lists the registered templates in registration order (for /meta).
func Teams() []Team {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]Team, 0, len(teams.list))
	for _, t := range teams.list {
		t.Agents = append([]TeamAgent{}, t.Agents...)
		out = append(out, t)
	}
	return out
}

// TeamByID looks a template up.
func TeamByID(id string) (Team, bool) {
	for _, t := range Teams() {
		if t.ID == id {
			return t, true
		}
	}
	return Team{}, false
}

// Agent builds the store.Agent a template role creates in org (not yet saved).
func (ta TeamAgent) Agent(org uuid.UUID) *store.Agent {
	turns := ta.MaxTurns
	if turns <= 0 {
		turns = 12
	}
	return &store.Agent{OrgID: org, Name: ta.Name, Slug: ta.Slug, Avatar: ta.Avatar, Role: ta.Role,
		Description: ta.Description, Instructions: ta.Instructions, Capabilities: append([]string{}, ta.Capabilities...),
		Platforms: append([]string{}, ta.Platforms...), Language: "en", MaxTurns: turns, IsActive: true}
}

// ApplyTeam creates the template's agents in org, skipping the slugs the org already has (so
// applying a template twice, or over a hand-made team, adds only what is missing). It returns the
// created agents and the skipped slugs.
func ApplyTeam(ctx context.Context, st *store.Store, org uuid.UUID, id string) ([]*store.Agent, []string, error) {
	t, ok := TeamByID(strings.TrimSpace(id))
	if !ok {
		var ids []string
		for _, x := range Teams() {
			ids = append(ids, x.ID)
		}
		return nil, nil, store.Invalid(fmt.Sprintf("unknown team %q (available: %s)", id, strings.Join(ids, ", ")))
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
		if have[ta.Slug] {
			skipped = append(skipped, ta.Slug)
			continue
		}
		a, err := st.CreateAgent(ctx, ta.Agent(org))
		if err != nil {
			return created, skipped, fmt.Errorf("create %s: %w", ta.Slug, err)
		}
		created = append(created, a)
	}
	return created, skipped, nil
}
