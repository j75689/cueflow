package deliverable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
	"github.com/j75689/cueflow/internal/textutil"
)

const maxPerRun = 10

// registerTools adds the deliverables capability to the crew: tools that write deliverables into
// the review queue and revise the ones still pending. Nothing here acts: the worker does, after a
// human approved.
func registerTools() {
	crew.RegisterCapability(crew.Capability{ID: "deliverables", Name: "Deliverables", Default: true,
		Description: "Produce documents and other deliverables for human review, and revise them while they are pending.",
		Prompt: func(*store.Agent) string {
			return `
- Deliverables you create (documents and the like) go to a human review queue; nothing is filed or sent until a human approves. Say so honestly.
- Name the deliverable ids you created in your complete_task summary.`
		}})
	for _, t := range tools {
		crew.RegisterTool(t)
	}
	crew.RegisterBrief(brief)
}

var tools = []crew.ToolSpec{
	{Name: "create_deliverable", Capability: "deliverables",
		Description: "Create a deliverable (default kind: document, a Markdown text) and send it to the human review queue. It is NOT acted on until a human approves.",
		Schema: crew.Obj(map[string]any{
			"kind":    crew.Str("Deliverable kind (default document)"),
			"title":   crew.Str("Short title for the reviewer"),
			"payload": map[string]any{"type": "object", "description": "The deliverable's content as the kind expects it; for a document: {\"content\": \"...markdown...\", \"summary\": \"optional\"}"},
			"draft":   map[string]any{"type": "boolean", "description": "Keep it as a draft instead of submitting it for review (default false)"},
		}, "title", "payload"), Run: toolCreate},
	{Name: "list_deliverables", Capability: "deliverables",
		Description: "List recent deliverables with their review status.",
		Schema: crew.Obj(map[string]any{"status": crew.Str("Optional status filter, e.g. PENDING_REVIEW"), "kind": crew.Str("Optional kind filter"),
			"limit": crew.Integer("Max items (default 10)")}), Run: toolList},
	{Name: "revise_deliverable", Capability: "deliverables",
		Description: "Revise a deliverable that is still DRAFT or PENDING_REVIEW.",
		Schema: crew.Obj(map[string]any{
			"deliverable_id": crew.Str("Deliverable ID"), "title": crew.Str("New title"),
			"payload": map[string]any{"type": "object", "description": "The full new payload (replaces the old one)"},
			"note":    crew.Str("Why you changed it"),
		}, "deliverable_id"), Run: toolRevise},
}

func toolCreate(ctx context.Context, r *crew.Run, raw json.RawMessage) (string, error) {
	var a struct {
		Kind    string          `json:"kind"`
		Title   string          `json:"title"`
		Payload json.RawMessage `json:"payload"`
		Draft   bool            `json:"draft"`
	}
	if err := crew.DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	if r.Count("deliverables") >= maxPerRun {
		return "", fmt.Errorf("deliverable limit reached (%d per run)", maxPerRun)
	}
	if strings.TrimSpace(a.Kind) == "" {
		a.Kind = DocumentKind
	}
	if strings.TrimSpace(a.Title) == "" {
		return "", errors.New("title is required")
	}
	taskID := r.Task.ID
	d, err := NewStore(r.Store).Create(ctx, New{TaskID: &taskID, Kind: a.Kind, Title: a.Title, Payload: a.Payload}, ActorAgent, !a.Draft)
	if err != nil {
		return "", err
	}
	r.Inc("deliverables")
	r.Broker.Publish(d.OrgID, "deliverable.updated", map[string]any{"id": d.ID, "status": d.Status})
	if d.Status == StatusDraft {
		return fmt.Sprintf("Deliverable %s (%s) saved as a draft.", d.ID, d.Kind), nil
	}
	return fmt.Sprintf("Deliverable %s (%s) created and queued for human review (not acted on).", d.ID, d.Kind), nil
}

func inOrg(ctx context.Context, r *crew.Run, id uuid.UUID) (*Deliverable, error) {
	d, err := NewStore(r.Store).Get(ctx, id)
	if err == nil && d.OrgID != r.Task.OrgID {
		err = domain.ErrNotFound
	}
	return d, err
}

func toolList(ctx context.Context, r *crew.Run, raw json.RawMessage) (string, error) {
	var a struct {
		Status string
		Kind   string
		Limit  int
	}
	if err := crew.DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	f := Filter{OrgID: &r.Task.OrgID, Kind: strings.TrimSpace(a.Kind), Limit: a.Limit}
	if f.Limit <= 0 || f.Limit > 30 {
		f.Limit = 10
	}
	if st := Status(strings.ToUpper(strings.TrimSpace(a.Status))); st != "" {
		if !st.Valid() {
			return "", fmt.Errorf("unknown status %q", a.Status)
		}
		f.Statuses = []Status{st}
	}
	items, _, err := NewStore(r.Store).List(ctx, f)
	if err != nil {
		return "", err
	}
	type row struct {
		ID      uuid.UUID `json:"id"`
		Kind    string    `json:"kind"`
		Status  Status    `json:"status"`
		Title   string    `json:"title"`
		Excerpt string    `json:"excerpt"`
	}
	out := []row{}
	for _, d := range items {
		out = append(out, row{d.ID, d.Kind, d.Status, d.Title, textutil.Clip(string(d.Payload), 240)})
	}
	return crew.JSONResult(out)
}

func toolRevise(ctx context.Context, r *crew.Run, raw json.RawMessage) (string, error) {
	var a struct {
		DeliverableID string          `json:"deliverable_id"`
		Title         *string         `json:"title"`
		Payload       json.RawMessage `json:"payload"`
		Note          string          `json:"note"`
	}
	if err := crew.DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	id, err := uuid.Parse(strings.TrimSpace(a.DeliverableID))
	if err != nil {
		return "", errors.New("deliverable_id must be a UUID")
	}
	cur, err := inOrg(ctx, r, id)
	if err != nil {
		return "", err
	}
	if !cur.Status.Editable() {
		return "", fmt.Errorf("deliverable is %s and can no longer be edited", cur.Status)
	}
	note := fmt.Sprintf("revised by @%s", r.Agent.Slug)
	if a.Note != "" {
		note += ": " + a.Note
	}
	d, err := NewStore(r.Store).Update(ctx, id, cur.Version, Patch{Title: a.Title, Payload: a.Payload}, ActorAgent, note)
	if err != nil {
		return "", err
	}
	r.Broker.Publish(d.OrgID, "deliverable.updated", map[string]any{"id": d.ID, "status": d.Status})
	return "Deliverable revised.", nil
}

// brief tells the agent which deliverables this task already has.
func brief(ctx context.Context, d crew.Deps, task *store.Task) string {
	var b strings.Builder
	if refs, err := NewStore(d.Store).ForTask(ctx, task.ID); err == nil && len(refs) > 0 {
		b.WriteString("\n## Deliverables created for this task\n")
		for _, ref := range refs {
			fmt.Fprintf(&b, "- %s [%s] %s: %s\n", ref.ID, ref.Status, ref.Kind, ref.Title)
		}
	}
	return b.String()
}
