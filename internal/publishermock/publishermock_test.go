package publishermock_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/connectors/mcppublisher"
	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/publishermock"
)

// The mock is driven by the real MCP publisher connector, so it stays a valid server for the contract.
func setup(t *testing.T, token string) (*publishermock.Server, connector.Publisher) {
	t.Helper()
	mock := publishermock.New("dev-token")
	srv := httptest.NewServer(mock.Handler())
	t.Cleanup(srv.Close)
	inst, err := connector.Build("mcp_publisher", connector.Config{"endpoint_url": srv.URL + "/mcp", "auth_token": token}, connector.Env{Name: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	return mock, inst.(connector.Publisher)
}

func TestTestAndAccounts(t *testing.T) {
	_, pub := setup(t, "dev-token")
	res, err := pub.Test(context.Background())
	if err != nil || !res.OK || !strings.Contains(res.Message, "4 accounts") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	accts, err := pub.(connector.AccountLister).ListAccounts(context.Background())
	if err != nil || len(accts) != 4 || accts[0] != (connector.Account{ID: "th1", Platform: "threads", Name: "@demo", Tag: "brand"}) {
		t.Fatalf("accounts: %+v %v", accts, err)
	}
}

func TestBadToken(t *testing.T) {
	_, pub := setup(t, "wrong")
	if res, _ := pub.Test(context.Background()); res.OK {
		t.Fatal("expected failure with the wrong token")
	}
}

func TestPublishRequiresGrant(t *testing.T) {
	mock, pub := setup(t, "dev-token")
	_, err := pub.Publish(context.Background(), connector.PublishRequest{PostID: uuid.New(), Platforms: []string{"threads"}, Parts: []string{"x"}, Accounts: []string{"th1"}})
	if !errors.Is(err, guard.ErrPublishForbidden) || len(mock.Posts()) != 0 {
		t.Fatalf("got %v, %d posts", err, len(mock.Posts()))
	}
}

func TestPublishFailAndRetry(t *testing.T) {
	mock, pub := setup(t, "dev-token")
	id := uuid.New()
	ctx := guard.WithPublishGrant(context.Background(), id)
	req := connector.PublishRequest{PostID: id, Platforms: []string{"threads"}, Parts: []string{"one", "two"},
		Hashtags: []string{"ai"}, Accounts: []string{"th1"}}

	mock.Fail.Store(true)
	if _, err := pub.Publish(ctx, req); err == nil || !strings.Contains(err.Error(), "simulated rate limit") {
		t.Fatalf("fail: %v", err)
	}
	if len(mock.Posts()) != 0 {
		t.Fatal("a refused post must not be recorded")
	}
	mock.Fail.Store(false)
	res, err := pub.Publish(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	posts := mock.Posts()
	if res.ExternalIDs[mcppublisher.PostIDKey] != "post-1" || res.ExternalIDs["threads @demo"] == "" || len(posts) != 1 {
		t.Fatalf("result %+v posts %+v", res, posts)
	}
	if got := posts[0].Text(); got != "one\n\ntwo\n\n#ai" {
		t.Fatalf("text %q", got)
	}
	// A retry of a post the publisher already has goes through publish_post, not create_post.
	req.Previous = res.ExternalIDs
	if _, err := pub.Publish(ctx, req); err != nil || len(mock.Posts()) != 1 {
		t.Fatalf("retry: %v, %d posts", err, len(mock.Posts()))
	}
}

func TestVideoUpload(t *testing.T) {
	mock, pub := setup(t, "dev-token")
	id := uuid.New()
	ctx := guard.WithPublishGrant(context.Background(), id)
	video := []byte("\x00\x00\x00\x18ftypmp42-fake-video")
	_, err := pub.Publish(ctx, connector.PublishRequest{PostID: id, Platforms: []string{"instagram"}, Parts: []string{"reel"},
		Accounts: []string{"ig1"}, MediaURLs: []string{"http://cueflow:3000/media/abc.mp4"},
		Media: []connector.MediaFile{{URL: "http://cueflow:3000/media/abc.mp4", Stored: true,
			Load: func(context.Context) ([]byte, string, error) { return video, "video/mp4", nil }}}})
	if err != nil {
		t.Fatal(err)
	}
	up := mock.Uploads()
	posts := mock.Posts()
	if len(up) != 1 || up["uploads/upload-1"] != len(video) || len(posts) != 1 || posts[0].Request.Parts[0].VideoKeys[0] != "uploads/upload-1" {
		t.Fatalf("uploads %v parts %+v", up, posts[0].Request.Parts)
	}
}
