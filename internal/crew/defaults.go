package crew

import (
	"context"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/store"
)

// DefaultAgents is the starter team of a new org: a single Chief who plans, delegates to the
// agents you add, and does the work itself when nobody fits. Everything is editable in the UI.
// Its instructions are English; it writes to the human (and drafts posts) in its configured
// language, which defaults to English.
func DefaultAgents() []*store.Agent {
	return []*store.Agent{
		{
			Name: "Chief", Slug: "chief", Avatar: "🧭", Role: "manager", IsActive: true, MaxTurns: 16,
			Description:  "Understands your request, breaks it into tasks for the team (or does the work itself), sets up recurring schedules, and reports back with the results.",
			Capabilities: []string{"planning", "delegation", "memory", "research", "sources", "drafting"},
			Instructions: `You are the team lead and the human's point of contact. When a request comes in:
1. Make sure you understand the goal. Ask the human only if information is missing and you cannot make a reasonable assumption.
2. Hand the work to the best teammate from your team list, with a clear goal, context and expected deliverable for every subtask. If no teammate fits, do the work yourself.
3. For recurring requests ("every day", "every week"), create a schedule with schedule_task and explain what you set up.
4. When subtasks finish, check the results against the request. Finish with complete_task: what was done, which drafts were created (remind the human to approve them in Review), and suggested next steps.
5. Remember preferences the human expresses (tone, topics, posting times) so you can apply them next time.`,
		},
	}
}

// SeedDefaults creates the starter team in an org that has no agents yet.
func SeedDefaults(ctx context.Context, st *store.Store, org uuid.UUID) (int, error) {
	n, err := st.CountAgents(ctx, org)
	if err != nil || n > 0 {
		return 0, err
	}
	created := 0
	for _, a := range DefaultAgents() {
		a.OrgID = org
		if _, err := st.CreateAgent(ctx, a); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}
