package searxng

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/googleid"
)

type staticToken string

func (s staticToken) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: string(s)}, nil
}

func fakeIdentity(t *testing.T, src oauth2.TokenSource, err error) {
	t.Helper()
	old := googleid.NewTokenSource
	googleid.NewTokenSource = func(context.Context, string) (oauth2.TokenSource, error) { return src, err }
	t.Cleanup(func() { googleid.NewTokenSource = old })
}

func server(t *testing.T, seen *http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Clone()
		_, _ = w.Write([]byte(`{"results":[{"title":"T","url":"https://e.example","content":"C"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A private Cloud Run SearXNG needs CueFlow's Google identity on each call; an ordinary one must
// not receive it.
func TestGoogleIdentity(t *testing.T) {
	var seen http.Header
	srv := server(t, &seen)

	fakeIdentity(t, staticToken("tok-1"), nil)
	inst, err := New(connector.Config{"endpoint_url": srv.URL, "google_identity": true}, connector.Env{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	res, err := inst.(*Searcher).Search(context.Background(), "go", 3)
	if err != nil || len(res) != 1 || res[0].Title != "T" {
		t.Fatalf("search: %+v %v", res, err)
	}
	if got := seen.Get(googleid.Header); got != "Bearer tok-1" {
		t.Errorf("identity header = %q", got)
	}

	seen = nil
	plain, err := New(connector.Config{"endpoint_url": srv.URL}, connector.Env{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.(*Searcher).Search(context.Background(), "go", 3); err != nil {
		t.Fatal(err)
	}
	if seen.Get(googleid.Header) != "" {
		t.Error("without google_identity no identity header may be sent")
	}
}

func TestGoogleIdentityUnavailable(t *testing.T) {
	fakeIdentity(t, nil, errors.New("could not find default credentials"))
	_, err := New(connector.Config{"endpoint_url": "https://s.run.app", "google_identity": true}, connector.Env{HTTPClient: http.DefaultClient})
	if err == nil || !strings.Contains(err.Error(), "no Google identity") {
		t.Fatalf("an unusable identity must be explained at build time: %v", err)
	}
}
