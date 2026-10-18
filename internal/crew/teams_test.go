package crew

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/store"
)

func TestTeamTemplateFile(t *testing.T) {
	good := `{"version":1,"name":"Two","agents":[{"name":"A","slug":"a","capabilities":["research"]},{"name":"B","slug":"B","max_turns":20,"language":"zh-TW"}]}`
	tm, err := ParseTeam([]byte(good))
	if err != nil || len(tm.Agents) != 2 {
		t.Fatalf("parse: %+v %v", tm, err)
	}
	org := uuid.New()
	a, b := tm.Agents[0].Agent(org), tm.Agents[1].Agent(org)
	if a.OrgID != org || !a.IsActive || a.Language != "en" || a.MaxTurns != 12 || b.MaxTurns != 20 || b.Language != "zh-TW" || b.Capabilities == nil {
		t.Fatalf("agent defaults: %+v %+v", a, b)
	}
	for name, bad := range map[string]string{
		"not json":  `{"name":`,
		"no name":   `{"agents":[{"name":"A","slug":"a"}]}`,
		"no agents": `{"name":"x","agents":[]}`,
		"no slug":   `{"name":"x","agents":[{"name":"A"}]}`,
		"dup slug":  `{"name":"x","agents":[{"name":"A","slug":"a"},{"name":"B","slug":"A"}]}`,
		"version":   `{"version":9,"name":"x","agents":[{"name":"A","slug":"a"}]}`,
	} {
		if _, err := ParseTeam([]byte(bad)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// Export carries only what a template holds.
	exp := TeamFromAgents("Org", []*store.Agent{{ID: uuid.New(), OrgID: org, Name: "A", Slug: "a", Role: "writer", MaxTurns: 8, Language: "en"}})
	if exp.Version != TeamVersion || len(exp.Agents) != 1 || exp.Agents[0].Slug != "a" || exp.Agents[0].Capabilities == nil {
		t.Fatalf("export: %+v", exp)
	}
	if err := exp.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTeam([]byte(strings.ReplaceAll(good, `"version":1`, `"version":0`))); err != nil {
		t.Fatalf("version omitted/zero must be accepted: %v", err)
	}
}
