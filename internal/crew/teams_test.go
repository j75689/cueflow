package crew

import (
	"testing"

	"github.com/google/uuid"
)

func TestTeamRegistry(t *testing.T) {
	RegisterTeam(Team{ID: "t-one", Name: "One", Agents: []TeamAgent{{Name: "A", Slug: "a", Role: "custom", Capabilities: []string{"research"}}}})
	RegisterTeam(Team{ID: "t-two", Name: "Two"})
	RegisterTeam(Team{ID: "t-one", Name: "One again", Agents: []TeamAgent{{Name: "A", Slug: "a"}, {Name: "B", Slug: "b", MaxTurns: 20}}})

	var ids []string
	for _, x := range Teams() {
		ids = append(ids, x.ID)
	}
	if len(ids) < 2 || ids[len(ids)-2] != "t-one" || ids[len(ids)-1] != "t-two" {
		t.Fatalf("order/replace: %v", ids)
	}
	one, ok := TeamByID("t-one")
	if !ok || one.Name != "One again" || len(one.Agents) != 2 {
		t.Fatalf("replaced: %+v %v", one, ok)
	}
	if _, ok := TeamByID("nope"); ok {
		t.Fatal("unknown id found")
	}
	org := uuid.New()
	a := one.Agents[0].Agent(org)
	b := one.Agents[1].Agent(org)
	if a.OrgID != org || !a.IsActive || a.Language != "en" || a.MaxTurns != 12 || b.MaxTurns != 20 || a.Capabilities == nil {
		t.Fatalf("agent defaults: %+v %+v", a, b)
	}
	// Teams() hands out copies.
	Teams()[len(ids)-2].Agents[0].Slug = "changed"
	if again, _ := TeamByID("t-one"); again.Agents[0].Slug != "a" {
		t.Fatal("registry mutated through Teams()")
	}
}
