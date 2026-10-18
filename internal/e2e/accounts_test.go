package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/j75689/cueflow/internal/llm"
	"strings"
	"sync"
	"testing"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/content"
	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/store"
)

// accountsPublisher plays a per-account publisher (like the MCP publisher): the first attempt reaches
// one account and fails the other; a retry with Previous finishes it.
type accountsPublisher struct{}

var (
	accountsMu   sync.Mutex
	accountsReqs []connector.PublishRequest
)

func (accountsPublisher) Test(context.Context) (*connector.TestResult, error) {
	return &connector.TestResult{OK: true}, nil
}

func (accountsPublisher) ListAccounts(context.Context) ([]connector.Account, error) {
	return []connector.Account{{ID: "a1", Platform: "threads", Name: "@one"}, {ID: "a2", Platform: "threads", Name: "@two"}}, nil
}

func (accountsPublisher) Publish(_ context.Context, req connector.PublishRequest) (*connector.PublishResult, error) {
	accountsMu.Lock()
	accountsReqs = append(accountsReqs, req)
	accountsMu.Unlock()
	if req.Previous["remote"] == "" {
		return nil, &connector.PartialError{ExternalIDs: map[string]string{"remote": "r1", "threads @one": "url1"}, Err: errors.New("@two failed")}
	}
	return &connector.PublishResult{ExternalIDs: map[string]string{"remote": "r1", "threads @one": "url1", "threads @two": "url2"}}, nil
}

func init() {
	connector.Register(connector.Spec{Type: "fake_accounts", Kind: connector.KindPublisher, Name: "Fake accounts", Accounts: true},
		func(connector.Config, connector.Env) (connector.Instance, error) { return accountsPublisher{}, nil })
}

func TestPerAccountPublishAndPartialRetry(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var pub store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Accounts", "type": "fake_accounts", "config": map[string]any{}}, &pub); code != 201 {
		t.Fatalf("create connector: %d", code)
	}
	var accts []connector.Account
	if code := e.do("GET", "/connectors/"+pub.ID.String()+"/accounts", nil, &accts); code != 200 || len(accts) != 2 {
		t.Fatalf("accounts: %d %+v", code, accts)
	}

	var post deliverable.Deliverable
	e.do("POST", "/deliverables", map[string]any{"kind": "post", "title": "hi", "payload": map[string]any{"content": "hi", "platforms": []string{"threads"},
		"accounts": []string{"a1"}, "topic": " AI 新聞 ", "link_url": "https://example.com/a", "publisher_connector_id": pub.ID}, "submit": true}, &post)
	pp, err := content.DecodePostPayload(post.Payload)
	if err != nil || pp.Topic != "AI 新聞" || pp.LinkURL != "https://example.com/a" {
		t.Fatalf("topic/link not stored: %+v %v", pp, err)
	}
	pp.Accounts = []string{"a1", "a2"}
	payload, _ := json.Marshal(pp)
	if code := e.do("PATCH", "/deliverables/"+post.ID.String(), map[string]any{"version": post.Version, "payload": json.RawMessage(payload)}, &post); code != 200 {
		t.Fatalf("update accounts: %d", code)
	}
	if pp, _ = content.DecodePostPayload(post.Payload); len(pp.Accounts) != 2 {
		t.Fatalf("accounts not stored: %+v", pp.Accounts)
	}

	e.do("POST", "/deliverables/"+post.ID.String()+"/approve", map[string]any{}, nil)
	e.deliverer.Tick(ctx)
	got, _ := e.ds.Get(ctx, post.ID)
	if got.Status != deliverable.StatusFailed || got.ExternalIDs["remote"] != "r1" {
		t.Fatalf("partial failure must keep external ids: %s %v", got.Status, got.ExternalIDs)
	}

	e.do("POST", "/deliverables/"+post.ID.String()+"/retry", nil, nil)
	e.deliverer.Tick(ctx)
	got, _ = e.ds.Get(ctx, post.ID)
	if got.Status != deliverable.StatusDone || got.ExternalIDs["threads @two"] != "url2" {
		t.Fatalf("retry: %s %v", got.Status, got.ExternalIDs)
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	if len(accountsReqs) != 2 || len(accountsReqs[0].Accounts) != 2 || accountsReqs[1].Previous["remote"] != "r1" ||
		accountsReqs[0].Topic != "AI 新聞" || accountsReqs[0].Link != "https://example.com/a" {
		t.Fatalf("requests: %+v", accountsReqs)
	}
}

func TestSchedulePublishDefaultsReachDrafts(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	var pub store.Connector
	e.do("POST", "/connectors", map[string]any{"name": "Accounts", "type": "fake_accounts", "config": map[string]any{}}, &pub)

	var sc store.Schedule
	if code := e.do("POST", "/schedules", map[string]any{"task_title": "Daily AI news", "cron_expr": "30 7 * * *",
		"timezone": "Asia/Taipei", "assignee_agent_id": team["writer"].ID,
		"publisher_connector_id": pub.ID, "publish_accounts": []string{"a1", "ig-only"}}, &sc); code != 201 {
		t.Fatalf("create schedule: %d", code)
	}
	if sc.PublisherConnectorID == nil || len(sc.PublishAccounts) != 2 {
		t.Fatalf("schedule defaults: %+v", sc)
	}
	var briefText string
	var refused string
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		res := toolResults(req)
		switch len(res) {
		case 0:
			briefText = brief(req)
			return calls("create_post_draft", map[string]any{"platform": "instagram", "content": "AI news of the day"})
		case 1:
			refused = res[0].Content
			return calls("create_post_draft", map[string]any{"platform": "threads", "content": "AI news of the day",
				"source_urls": []string{"https://example.com/story"}})
		}
		return calls("complete_task", map[string]any{"summary": "drafted"})
	})
	var task store.Task
	if code := e.do("POST", "/schedules/"+sc.ID.String()+"/run", nil, &task); code >= 300 {
		t.Fatalf("run schedule: %d", code)
	}
	tasks, _ := e.store.ListTasks(ctx, store.TaskFilter{All: true})
	if len(tasks) != 1 || tasks[0].PublisherConnectorID == nil || *tasks[0].PublisherConnectorID != pub.ID {
		t.Fatalf("task did not get the schedule's defaults: %+v", tasks)
	}
	e.crew.RunPending(ctx, 2)
	if !strings.Contains(briefText, "Only write drafts for: threads.") || !strings.Contains(briefText, "threads @one") {
		t.Fatalf("brief should name the publishing target:\n%s", briefText)
	}
	if !strings.Contains(refused, "publishes only to threads") {
		t.Fatalf("instagram draft should be refused: %q", refused)
	}
	posts, _ := e.ds.ForTask(ctx, tasks[0].ID)
	if len(posts) != 1 {
		t.Fatalf("posts: %+v", posts)
	}
	d, _ := e.ds.Get(ctx, posts[0].ID)
	p, _ := content.DecodePostPayload(d.Payload)
	if p.LinkURL != "https://example.com/story" {
		t.Fatalf("link card should default to the first source URL: %q", p.LinkURL)
	}
	if p.PublisherConnectorID == nil || *p.PublisherConnectorID != pub.ID || strings.Join(p.Accounts, ",") != "a1" {
		t.Fatalf("draft defaults (accounts filtered to the draft's platform): %v %v", p.PublisherConnectorID, p.Accounts)
	}

	// Editing a task's defaults by hand.
	var edited store.Task
	if code := e.do("PATCH", "/tasks/"+tasks[0].ID.String(), map[string]any{"publisher_connector_id": nil, "publish_accounts": []string{}}, &edited); code != 200 ||
		edited.PublisherConnectorID != nil || len(edited.PublishAccounts) != 0 {
		t.Fatalf("clear defaults: %d %+v", code, edited)
	}
}
