package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/store"
)

const teamExample = "../../templates/social-content.json"

// exampleTeam is the shipped example template: the research/writing/editing crew the crew tests
// delegate to, so the file in templates/ is the one the tests run.
func exampleTeam(t *testing.T) (crew.Team, []byte) {
	t.Helper()
	raw, err := os.ReadFile(teamExample)
	if err != nil {
		t.Fatal(err)
	}
	team, err := crew.ParseTeam(raw)
	if err != nil {
		t.Fatal(err)
	}
	return team, raw
}

// testSpecialists is the example team as agents, org to be set by the caller. New orgs only seed the
// Chief; the tests add these themselves (or import the file through the API).
func testSpecialists(t *testing.T) []*store.Agent {
	team, _ := exampleTeam(t)
	var out []*store.Agent
	for _, ta := range team.Agents {
		out = append(out, ta.Agent(uuid.Nil))
	}
	return out
}

// A team template is a JSON file: imported with POST /agents/template (skipping slugs the org has),
// exported with GET /agents/template, and the export imports again elsewhere.
func TestTeamTemplateImportExport(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	team, raw := exampleTeam(t)
	if len(team.Agents) != 3 {
		t.Fatalf("example team: %+v", team)
	}

	// The default org has a hand-made writer: importing the file skips it and adds the other two.
	def, _ := e.store.DefaultOrg(ctx)
	if _, err := e.store.CreateAgent(ctx, &store.Agent{OrgID: def.ID, Name: "My writer", Slug: "writer", Capabilities: []string{"research"}}); err != nil {
		t.Fatal(err)
	}
	var res struct {
		Created []store.Agent `json:"created"`
		Skipped []string      `json:"skipped"`
	}
	if code := e.do("POST", "/agents/template", json.RawMessage(raw), &res); code != 200 {
		t.Fatalf("import: %d", code)
	}
	if len(res.Created) != 2 || len(res.Skipped) != 1 || res.Skipped[0] != "writer" {
		t.Fatalf("import result: %+v", res)
	}
	for _, a := range res.Created {
		if a.OrgID != def.ID || !a.IsActive || a.MaxTurns != 12 || a.Language != "en" {
			t.Fatalf("created agent: %+v", a)
		}
	}
	if code := e.do("POST", "/agents/template", json.RawMessage(raw), &res); code != 200 || len(res.Created) != 0 || len(res.Skipped) != 3 {
		t.Fatalf("second import: %d %+v", code, res)
	}
	for _, bad := range []string{`{"name":`, `{"name":"x","agents":[]}`, `{"name":"x","agents":[{"name":"A","slug":"a","capabilities":["no-such-capability"]}]}`} {
		if code := e.do("POST", "/agents/template", json.RawMessage(bad), nil); code != 400 {
			t.Fatalf("bad template %q: %d", bad, code)
		}
	}

	// Export the default org, import it into a fresh org: same roles, nothing install-specific.
	var exported crew.Team
	if code := e.do("GET", "/agents/template", nil, &exported); code != 200 || exported.Version != crew.TeamVersion || len(exported.Agents) != 3 {
		t.Fatalf("export: %d %+v", code, exported)
	}
	var other store.Org
	e.do("POST", "/orgs", map[string]any{"name": "Other"}, &other)
	if code := e.do("POST", "/agents/template?org=other", exported, &res); code != 200 || len(res.Created) != 3 {
		t.Fatalf("re-import: %d %+v", code, res)
	}
	agents, _ := e.store.ListAgents(ctx, other.ID)
	if len(agents) != 3 || agents[0].OrgID != other.ID {
		t.Fatalf("other org agents: %d", len(agents))
	}
}

// Every template shipped under templates/ imports into a fresh org with all its agents created: the
// roles and capabilities it names exist on a server with the built-in modules, so picking one from
// the repository never ends in the "unknown capability" error the test above provokes on purpose.
func TestShippedTemplatesImport(t *testing.T) {
	e := setup(t)
	files, err := filepath.Glob("../../templates/*.json")
	if err != nil || len(files) < 2 {
		t.Fatalf("templates: %v %v", files, err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		team, err := crew.ParseTeam(raw)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		var org store.Org
		if code := e.do("POST", "/orgs", map[string]any{"name": strings.TrimSuffix(filepath.Base(f), ".json")}, &org); code != 201 || org.Slug == "" {
			t.Fatalf("%s: create org: %d %+v", f, code, org)
		}
		var res struct {
			Created []store.Agent `json:"created"`
			Skipped []string      `json:"skipped"`
		}
		if code := e.do("POST", "/agents/template?org="+org.Slug, json.RawMessage(raw), &res); code != 200 || len(res.Created) != len(team.Agents) || len(res.Skipped) != 0 {
			t.Fatalf("%s: import: %d %+v", f, code, res)
		}
	}
}
