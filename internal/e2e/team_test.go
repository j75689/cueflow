package e2e

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/content"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/store"
)

// testSpecialists is the research/writing/editing crew the crew tests delegate to: the content
// module's team template, so the tests run the roles a new install gets. New orgs only seed the
// Chief; the tests add these themselves (or apply the template through the API).
func testSpecialists() []*store.Agent {
	team, ok := crew.TeamByID(content.TeamID)
	if !ok {
		panic("content team template not registered")
	}
	var out []*store.Agent
	for _, ta := range team.Agents {
		out = append(out, ta.Agent(uuid.Nil))
	}
	return out
}

// A team template is listed in /meta, applied when an org is created or later through
// POST /agents/template, and never duplicates a slug the org already has.
func TestTeamTemplates(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	var meta struct {
		Teams []crew.Team `json:"agent_teams"`
	}
	e.do("GET", "/meta", nil, &meta)
	if len(meta.Teams) != 1 || meta.Teams[0].ID != content.TeamID || len(meta.Teams[0].Agents) != 3 {
		t.Fatalf("meta teams: %+v", meta.Teams)
	}

	// Created with the template: Chief + the three specialists.
	var org store.Org
	if code := e.do("POST", "/orgs", map[string]any{"name": "Content", "seed_agents": true, "team": content.TeamID}, &org); code != 201 {
		t.Fatalf("create org: %d", code)
	}
	agents, _ := e.store.ListAgents(ctx, org.ID)
	if len(agents) != 4 {
		t.Fatalf("org agents: %d", len(agents))
	}
	if code := e.do("POST", "/orgs", map[string]any{"name": "Nope", "team": "no-such-team"}, nil); code != 400 {
		t.Fatalf("unknown team on create: %d", code)
	}

	// Applied later to the default org, which has only a hand-made writer: the writer is skipped.
	def, _ := e.store.DefaultOrg(ctx)
	if _, err := e.store.CreateAgent(ctx, &store.Agent{OrgID: def.ID, Name: "My writer", Slug: "writer", Capabilities: []string{"research"}}); err != nil {
		t.Fatal(err)
	}
	var res struct {
		Created []store.Agent `json:"created"`
		Skipped []string      `json:"skipped"`
	}
	if code := e.do("POST", "/agents/template", map[string]any{"team": content.TeamID}, &res); code != 200 {
		t.Fatalf("apply: %d", code)
	}
	if len(res.Created) != 2 || len(res.Skipped) != 1 || res.Skipped[0] != "writer" {
		t.Fatalf("apply result: %+v", res)
	}
	for _, a := range res.Created {
		if a.OrgID != def.ID || !a.IsActive || a.MaxTurns != 12 || a.Language != "en" {
			t.Fatalf("created agent: %+v", a)
		}
	}
	// Applying again adds nothing.
	if code := e.do("POST", "/agents/template", map[string]any{"team": content.TeamID}, &res); code != 200 || len(res.Created) != 0 || len(res.Skipped) != 3 {
		t.Fatalf("second apply: %d %+v", code, res)
	}
	if code := e.do("POST", "/agents/template", map[string]any{"team": "no-such-team"}, nil); code != 400 {
		t.Fatalf("unknown team: %d", code)
	}
}
