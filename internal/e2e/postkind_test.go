package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/content"
	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// An agent moved from drafting to post_deliverables keeps calling the same tools; its drafts land
// in deliverables as posts, list_posts still shows the old posts next to them, and approval
// publishes through the publisher connector, under the deliverable's grant.
func TestPostDeliverablesFlow(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()

	var publisher store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Publisher", "type": "mcp_publisher", "config": map[string]any{
		"endpoint_url": os.Getenv("PUBLISHER_URL") + "/mcp", "auth_token": "pub-token"}}, &publisher); code != 201 {
		t.Fatalf("create publisher: %d", code)
	}
	// A legacy post, so the merged listing has something from the posts table.
	var legacy content.Post
	e.do("POST", "/posts", map[string]any{"title": "Old one", "content": "from the posts table", "platforms": []string{"threads"}, "submit": true}, &legacy)

	// The writer, as in production: drafting swapped for post_deliverables, nothing else changed.
	writer := team["writer"]
	caps := []string{content.PostDeliverablesCapability, "research", "memory"}
	if code := e.do("PATCH", "/agents/"+writer.ID.String(), map[string]any{"capabilities": caps}, nil); code != 200 {
		t.Fatalf("swap capability: %d", code)
	}
	var seenTools []string
	var listed string
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		seenTools = nil
		for _, tl := range req.Tools {
			seenTools = append(seenTools, tl.Name)
		}
		res := toolResults(req)
		switch len(res) {
		case 0:
			return calls("list_posts", map[string]any{"limit": 10})
		case 1:
			listed = res[0].Content
			return calls("create_post_draft", map[string]any{"platform": "threads", "title": "AI news today",
				"content": "Three things happened in AI today.", "hashtags": []string{"ai"}, "source_urls": []string{"https://example.com/a"}})
		case 2:
			id := strings.Fields(res[1].Content)[1]
			return calls("revise_draft", map[string]any{"post_id": id, "content": "Three things happened in AI today. Here they are.", "note": "fuller"})
		}
		return calls("complete_task", map[string]any{"summary": "drafted " + strings.Fields(res[1].Content)[1]})
	})
	// The schedule's publishing defaults, as the task carries them.
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Daily top 5 AI news for Threads", "assignee_agent_id": writer.ID,
		"publisher_connector_id": publisher.ID, "publish_accounts": []string{"th1"}}, &task)
	e.crew.RunPending(ctx, 2)

	for _, want := range []string{"create_post_draft", "list_posts", "revise_draft"} {
		if !containsString(seenTools, want) {
			t.Fatalf("tool %s not offered: %v", want, seenTools)
		}
	}
	if !strings.Contains(listed, "from the posts table") {
		t.Fatalf("list_posts must still show the posts table: %s", listed)
	}
	refs, err := e.ds.ForTask(ctx, task.ID)
	if err != nil || len(refs) != 1 || refs[0].Kind != content.PostKind || refs[0].Status != deliverable.StatusPendingReview {
		t.Fatalf("post deliverable for task: %+v %v", refs, err)
	}
	d, _ := e.ds.Get(ctx, refs[0].ID)
	payload, err := content.DecodePostPayload(d.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if payload.PublisherConnectorID == nil || *payload.PublisherConnectorID != publisher.ID || strings.Join(payload.Accounts, ",") != "th1" ||
		!strings.Contains(payload.Content, "Here they are") || payload.LinkURL != "https://example.com/a" || len(payload.Platforms) != 1 {
		t.Fatalf("payload: %+v", payload)
	}
	var meta struct {
		Kinds []deliverable.Kind `json:"deliverable_kinds"`
	}
	e.do("GET", "/meta", nil, &meta)
	hasPost := false
	for _, k := range meta.Kinds {
		hasPost = hasPost || (k.ID == "post" && k.BuiltIn)
	}
	if !hasPost {
		t.Fatalf("post kind missing or not built in: %+v", meta.Kinds)
	}

	// Reviewer approves; the deliverable worker publishes through the publisher connector.
	if code := e.do("POST", "/deliverables/"+d.ID.String()+"/approve", nil, nil); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	e.deliverer.Tick(ctx)
	e.do("GET", "/deliverables/"+d.ID.String(), nil, &d)
	if d.Status != deliverable.StatusDone || d.ExternalIDs["post_id"] == "" {
		t.Fatalf("not published: %+v", d)
	}
	posts := e.mock.Posts()
	if len(posts) != 1 || !strings.Contains(posts[0].Text(), "Here they are") || strings.Join(posts[0].Request.Accounts, ",") != "th1" {
		t.Fatalf("publisher received %+v", posts)
	}
	var audit []deliverable.AuditLog
	e.do("GET", "/deliverables/"+d.ID.String()+"/audit", nil, &audit)
	if !strings.Contains(audit[len(audit)-2].Note, "via the post kind") {
		t.Fatalf("audit: %+v", audit)
	}

	// A failing publisher fails the item; a retry publishes it.
	e.mock.Fail.Store(true)
	raw, _ := json.Marshal(map[string]any{"content": "retry me", "platforms": []string{"threads"}, "accounts": []string{"th1"},
		"publisher_connector_id": publisher.ID})
	f, err := e.ds.Create(ctx, deliverable.New{OrgID: task.OrgID, Kind: content.PostKind, Title: "Retry", Payload: raw}, deliverable.ActorReviewer, true)
	if err != nil {
		t.Fatal(err)
	}
	e.do("POST", "/deliverables/"+f.ID.String()+"/approve", nil, nil)
	e.deliverer.Tick(ctx)
	f, _ = e.ds.Get(ctx, f.ID)
	if f.Status != deliverable.StatusFailed || !strings.Contains(f.ErrorMessage, "simulated rate limit") {
		t.Fatalf("expected FAILED: %+v", f)
	}
	e.mock.Fail.Store(false)
	e.do("POST", "/deliverables/"+f.ID.String()+"/retry", nil, nil)
	e.deliverer.Tick(ctx)
	f, _ = e.ds.Get(ctx, f.ID)
	if f.Status != deliverable.StatusDone || f.Attempts != 2 {
		t.Fatalf("retry: %+v", f)
	}

	// The payload validator refuses what the platform rules refuse.
	if _, err := e.ds.Create(ctx, deliverable.New{OrgID: task.OrgID, Kind: content.PostKind, Title: "x",
		Payload: json.RawMessage(`{"content":"hi","platforms":["myspace"]}`)}, deliverable.ActorReviewer, false); err == nil {
		t.Fatal("unknown platform accepted")
	}
}
