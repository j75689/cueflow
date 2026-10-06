package e2e

import (
	"context"
	"errors"
	"github.com/j75689/cueflow/internal/llm"
	"strings"
	"sync"
	"testing"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

// accountsPublisher plays a per-account publisher (like CueSocial): the first attempt reaches
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

	var post domain.Post
	e.do("POST", "/posts", map[string]any{"content": "hi", "platforms": []string{"threads"}, "accounts": []string{"a1"},
		"topic": " AI 新聞 ", "link_url": "https://example.com/a", "publisher_connector_id": pub.ID, "submit": true}, &post)
	if post.Topic != "AI 新聞" || post.LinkURL != "https://example.com/a" {
		t.Fatalf("topic/link not stored: %q %q", post.Topic, post.LinkURL)
	}
	if code := e.do("PATCH", "/posts/"+post.ID.String(), map[string]any{"version": post.Version, "accounts": []string{"a1", "a2"}}, &post); code != 200 {
		t.Fatalf("update accounts: %d", code)
	}
	if len(post.Accounts) != 2 {
		t.Fatalf("accounts not stored: %+v", post.Accounts)
	}

	e.do("POST", "/posts/"+post.ID.String()+"/approve", map[string]any{}, nil)
	e.worker.Tick(ctx)
	got, _ := e.store.GetPost(ctx, post.ID)
	if got.Status != domain.StatusFailed || got.ExternalPostIDs["remote"] != "r1" {
		t.Fatalf("partial failure must keep external ids: %s %v", got.Status, got.ExternalPostIDs)
	}

	e.do("POST", "/posts/"+post.ID.String()+"/retry", nil, nil)
	e.worker.Tick(ctx)
	got, _ = e.store.GetPost(ctx, post.ID)
	if got.Status != domain.StatusPublished || got.ExternalPostIDs["threads @two"] != "url2" {
		t.Fatalf("retry: %s %v", got.Status, got.ExternalPostIDs)
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
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		if len(toolResults(req)) == 0 {
			return calls("create_post_draft", map[string]any{"platform": "threads", "content": "AI news of the day"})
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
	posts, _ := e.store.PostsForTask(ctx, tasks[0].ID)
	if len(posts) != 1 {
		t.Fatalf("posts: %+v", posts)
	}
	p, _ := e.store.GetPost(ctx, posts[0].ID)
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
