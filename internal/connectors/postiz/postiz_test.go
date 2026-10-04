package postiz_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	_ "github.com/j75689/cueflow/internal/connectors/postiz"
	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/postizmock"
)

func setup(t *testing.T, extra connector.Config) (*postizmock.Server, connector.Publisher) {
	t.Helper()
	mock := postizmock.New("test-key")
	srv := httptest.NewServer(mock.Handler())
	t.Cleanup(srv.Close)
	cfg := connector.Config{"endpoint_url": srv.URL + "/api/mcp", "api_key": "test-key"}
	for k, v := range extra {
		cfg[k] = v
	}
	inst, err := connector.Build("postiz_mcp", cfg, connector.Env{Name: "postiz"})
	if err != nil {
		t.Fatal(err)
	}
	return mock, inst.(connector.Publisher)
}

func TestConnectionTest(t *testing.T) {
	_, pub := setup(t, nil)
	res, err := pub.Test(context.Background())
	if err != nil || !res.OK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.Contains(res.Message, "4 active channels") {
		t.Errorf("message: %s", res.Message)
	}
}

func TestBadAPIKey(t *testing.T) {
	mock := postizmock.New("right")
	srv := httptest.NewServer(mock.Handler())
	defer srv.Close()
	inst, _ := connector.Build("postiz_mcp", connector.Config{"endpoint_url": srv.URL + "/api/mcp", "api_key": "wrong"}, connector.Env{})
	res, _ := inst.Test(context.Background())
	if res.OK {
		t.Fatal("expected failure with wrong key")
	}
}

func TestPublishRequiresGrant(t *testing.T) {
	mock, pub := setup(t, nil)
	_, err := pub.Publish(context.Background(), connector.PublishRequest{PostID: uuid.New(), Platforms: []string{"threads"}, Parts: []string{"x"}})
	if !errors.Is(err, guard.ErrPublishForbidden) {
		t.Fatalf("got %v", err)
	}
	if len(mock.Posts()) != 0 {
		t.Fatal("mock received a post without authorization")
	}
}

func TestPublishThreadsAndInstagram(t *testing.T) {
	mock, pub := setup(t, nil)
	id := uuid.New()
	ctx := guard.WithPublishGrant(context.Background(), id)
	res, err := pub.Publish(ctx, connector.PublishRequest{
		PostID: id, Platforms: []string{"threads", "instagram"},
		Parts: []string{"part one", "part <two>"}, Hashtags: []string{"ai"},
		MediaURLs: []string{"https://example.com/a.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalIDs["threads"] == "" || res.ExternalIDs["instagram"] == "" {
		t.Fatalf("external ids: %v", res.ExternalIDs)
	}
	posts := mock.Posts()
	if len(posts) != 2 {
		t.Fatalf("mock got %d posts", len(posts))
	}
	th, ig := posts[0].Post, posts[1].Post
	if th.IntegrationID != "int-threads-1" || len(th.PostsAndComments) != 2 {
		t.Fatalf("threads payload: %+v", th)
	}
	if th.PostsAndComments[1].Content != "<p>part &lt;two&gt;</p><p></p><p>#ai</p>" {
		t.Errorf("html rendering: %q", th.PostsAndComments[1].Content)
	}
	if len(th.PostsAndComments[0].Attachments) != 1 || th.PostsAndComments[0].Attachments[0] != "/uploads/media-1" {
		t.Errorf("media should be re-uploaded and attached to the first part: %v", th.PostsAndComments[0].Attachments)
	}
	if ig.IntegrationID != "int-instagram-1" || len(ig.PostsAndComments) != 1 {
		t.Fatalf("instagram should get a single joined post: %+v", ig)
	}
	if len(ig.Settings) != 1 || ig.Settings[0]["key"] != "post_type" || ig.Settings[0]["value"] != "post" {
		t.Errorf("instagram settings: %v", ig.Settings)
	}
	if th.Type != "now" {
		t.Errorf("type = %s", th.Type)
	}
}

func TestScheduledAndDraftMode(t *testing.T) {
	mock, pub := setup(t, connector.Config{"post_mode": "draft", "integrations": map[string]any{"threads": "int-threads-1"}})
	id := uuid.New()
	at := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	_, err := pub.Publish(guard.WithPublishGrant(context.Background(), id), connector.PublishRequest{
		PostID: id, Platforms: []string{"threads"}, Parts: []string{"later"}, ScheduledAt: &at,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := mock.Posts()[0].Post
	if p.Type != "draft" || p.Date != at.Format(time.RFC3339) {
		t.Errorf("type=%s date=%s", p.Type, p.Date)
	}
}

func TestPostizValidationErrorBecomesError(t *testing.T) {
	mock, pub := setup(t, nil)
	mock.Fail.Store(true)
	id := uuid.New()
	_, err := pub.Publish(guard.WithPublishGrant(context.Background(), id), connector.PublishRequest{
		PostID: id, Platforms: []string{"threads"}, Parts: []string{"x"},
	})
	if err == nil || !strings.Contains(err.Error(), "simulated rate limit") {
		t.Fatalf("got %v", err)
	}
}

func TestUnknownPlatformChannel(t *testing.T) {
	mock, pub := setup(t, nil)
	mock.Channels = mock.Channels[:1] // only threads connected
	id := uuid.New()
	_, err := pub.Publish(guard.WithPublishGrant(context.Background(), id), connector.PublishRequest{
		PostID: id, Platforms: []string{"x"}, Parts: []string{"x"},
	})
	if err == nil || !strings.Contains(err.Error(), "no active x channel") {
		t.Fatalf("got %v", err)
	}
}

func TestListToolsNeverAgentAllowed(t *testing.T) {
	_, pub := setup(t, nil)
	tools, err := pub.(connector.ToolLister).ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) < 3 {
		t.Fatalf("tools: %v", tools)
	}
	for _, tl := range tools {
		if tl.AgentAllowed {
			t.Errorf("%s must not be agent-allowed", tl.Name)
		}
	}
}
