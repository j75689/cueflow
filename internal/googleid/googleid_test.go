package googleid

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type fakeSource struct {
	calls atomic.Int32
	err   error
}

func (f *fakeSource) Token() (*oauth2.Token, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return &oauth2.Token{AccessToken: "id-token-123"}, nil
}

func use(t *testing.T, src oauth2.TokenSource, err error) *string {
	t.Helper()
	var audience string
	old := NewTokenSource
	NewTokenSource = func(_ context.Context, aud string) (oauth2.TokenSource, error) {
		audience = aud
		return src, err
	}
	t.Cleanup(func() { NewTokenSource = old })
	return &audience
}

func TestAudience(t *testing.T) {
	for in, want := range map[string]string{
		"https://svc-123.asia-east1.run.app":            "https://svc-123.asia-east1.run.app",
		"https://svc-123.asia-east1.run.app/mcp":        "https://svc-123.asia-east1.run.app",
		"https://svc-123.asia-east1.run.app/search?q=1": "https://svc-123.asia-east1.run.app",
		"http://localhost:8080/x":                       "http://localhost:8080",
	} {
		if got, err := Audience(in); err != nil || got != want {
			t.Errorf("Audience(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "svc.run.app", "/just/a/path"} {
		if _, err := Audience(bad); err == nil {
			t.Errorf("Audience(%q) must fail", bad)
		}
	}
}

// Requests carry the ID token in X-Serverless-Authorization, leave Authorization to the app, do not
// change the caller's request, and the token is made out to the endpoint's origin.
func TestClientSignsRequests(t *testing.T) {
	var gotServerless, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotServerless, gotAuth = r.Header.Get(Header), r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	src := &fakeSource{}
	aud := use(t, src, nil)
	c, err := Client(context.Background(), &http.Client{}, srv.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if *aud != srv.URL {
		t.Errorf("audience %q, want the endpoint's origin %q", *aud, srv.URL)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer apps-own-token")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotServerless != "Bearer id-token-123" {
		t.Errorf("X-Serverless-Authorization = %q", gotServerless)
	}
	if gotAuth != "Bearer apps-own-token" {
		t.Errorf("the app's own Authorization must be left alone, got %q", gotAuth)
	}
	if req.Header.Get(Header) != "" {
		t.Error("the caller's request must not be modified")
	}
}

func TestClientKeepsTheBaseClientSettings(t *testing.T) {
	use(t, &fakeSource{}, nil)
	base := &http.Client{Timeout: 7 * time.Second}
	c, err := Client(context.Background(), base, "https://svc.run.app")
	if err != nil || c.Timeout != base.Timeout || c == base {
		t.Fatalf("copy of the base client expected: %v %v", c, err)
	}
	if base.Transport != nil {
		t.Error("the base client must not be modified")
	}
}

func TestClientErrors(t *testing.T) {
	use(t, nil, errors.New("could not find default credentials"))
	if _, err := Client(context.Background(), nil, "https://svc.run.app"); err == nil ||
		!strings.Contains(err.Error(), "no Google identity") {
		t.Errorf("missing identity must be explained: %v", err)
	}
	if _, err := Client(context.Background(), nil, "not a url"); err == nil {
		t.Error("a bad endpoint must be refused")
	}

	// A token that cannot be minted fails the request, not the process.
	use(t, &fakeSource{err: errors.New("metadata server unreachable")}, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the request must not be sent without a token")
	}))
	defer srv.Close()
	c, err := Client(context.Background(), nil, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(srv.URL); err == nil || !strings.Contains(err.Error(), "ID token") {
		t.Errorf("token failure must surface: %v", err)
	}
}
