package e2e

import (
	"context"
	"errors"
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
		"publisher_connector_id": pub.ID, "submit": true}, &post)
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
	if len(accountsReqs) != 2 || len(accountsReqs[0].Accounts) != 2 || accountsReqs[1].Previous["remote"] != "r1" {
		t.Fatalf("requests: %+v", accountsReqs)
	}
}
