package api

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/j75689/cueflow/internal/cfaccess"
)

const (
	testTeam = "https://acme.cloudflareaccess.com"
	testAud  = "cueflow-app-aud"
)

func accessToken(t *testing.T, k *rsa.PrivateKey, aud string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: k}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(map[string]any{"iss": testTeam, "aud": []string{aud}, "email": "me@example.com",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The dashboard API accepts a Cloudflare Access login or the admin token, whichever is configured,
// and refuses everything else, including a login for another Access application.
func TestAuthAccessAndToken(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	access := cfaccess.NewWithKeys(testTeam, &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&k.PublicKey}}, testAud)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	do := func(s *Server, path string, mut func(*http.Request)) int {
		r := httptest.NewRequest("GET", path, nil)
		if mut != nil {
			mut(r)
		}
		w := httptest.NewRecorder()
		s.auth(ok).ServeHTTP(w, r)
		return w.Code
	}
	login := func(aud string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set(cfaccess.HeaderJWT, accessToken(t, k, aud)) }
	}
	cookie := func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: cfaccess.CookieJWT, Value: accessToken(t, k, testAud)})
	}
	bearer := func(tok string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
	}

	// Both configured: either way in, nothing else.
	both := &Server{AdminToken: "s3cret", Access: access, Logger: logger}
	for name, c := range map[string]struct {
		mut  func(*http.Request)
		want int
	}{
		"nothing":                      {nil, 401},
		"Access login (header)":        {login(testAud), 204},
		"Access login (cookie)":        {cookie, 204},
		"admin token":                  {bearer("s3cret"), 204},
		"wrong admin token":            {bearer("nope"), 401},
		"login for another app":        {login("another-app"), 401},
		"another app's login + token":  {func(r *http.Request) { login("another-app")(r); bearer("s3cret")(r) }, 204},
		"garbage in the Access header": {func(r *http.Request) { r.Header.Set(cfaccess.HeaderJWT, "x.y.z") }, 401},
	} {
		if got := do(both, "/api/v1/tasks", c.mut); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	if got := do(both, "/api/v1/health", nil); got != 204 {
		t.Errorf("health must stay open: %d", got)
	}
	if got := do(both, "/api/v1/events?token=s3cret", nil); got != 204 {
		t.Errorf("the event stream keeps its ?token fallback: %d", got)
	}

	// Access alone (no admin token): the login is the only way in.
	only := &Server{Access: access, Logger: logger}
	if do(only, "/api/v1/tasks", nil) != 401 || do(only, "/api/v1/tasks", login(testAud)) != 204 {
		t.Error("with Access alone, a login must be required and enough")
	}

	// Neither configured: open, as before.
	if got := do(&Server{Logger: logger}, "/api/v1/tasks", nil); got != 204 {
		t.Errorf("no auth configured must stay open: %d", got)
	}
	// Token alone behaves as before.
	tok := &Server{AdminToken: "s3cret", Logger: logger}
	if do(tok, "/api/v1/tasks", nil) != 401 || do(tok, "/api/v1/tasks", bearer("s3cret")) != 204 {
		t.Error("token-only auth changed")
	}
}

func TestMachineURL(t *testing.T) {
	if got := (&Server{PublicURL: "https://people.example"}).machineURL(); got != "https://people.example" {
		t.Errorf("falls back to PublicURL: %q", got)
	}
	if got := (&Server{PublicURL: "https://people.example", DirectURL: "https://direct.example"}).machineURL(); got != "https://direct.example" {
		t.Errorf("DirectURL wins: %q", got)
	}
}

// With Access configured the dashboard's pages need a login too, so the run.app address shows
// nothing to a visitor; without Access they stay open, as before.
func TestUIGuard(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	access := cfaccess.NewWithKeys(testTeam, &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&k.PublicKey}}, testAud)
	page := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("dashboard")) })

	get := func(h http.Handler, mut func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/board/", nil)
		if mut != nil {
			mut(r)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	guarded := (&Server{Access: access, PublicURL: "https://people.example"}).uiGuard(page)

	if w := get(guarded, nil); w.Code != http.StatusForbidden || w.Body.String() == "dashboard" ||
		!strings.Contains(w.Body.String(), "https://people.example") {
		t.Errorf("signed out: %d %q", w.Code, w.Body.String())
	}
	if w := get(guarded, func(r *http.Request) { r.Header.Set(cfaccess.HeaderJWT, accessToken(t, k, "another-app")) }); w.Code != http.StatusForbidden {
		t.Errorf("a login for another application must not open the pages: %d", w.Code)
	}
	if w := get(guarded, func(r *http.Request) { r.Header.Set(cfaccess.HeaderJWT, accessToken(t, k, testAud)) }); w.Code != http.StatusOK || w.Body.String() != "dashboard" {
		t.Errorf("signed in: %d %q", w.Code, w.Body.String())
	}
	if w := get(guarded, func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }); w.Code != http.StatusForbidden {
		t.Errorf("the admin token opens the API, not the pages: %d", w.Code)
	}
	open := (&Server{}).uiGuard(page)
	if w := get(open, nil); w.Code != http.StatusOK {
		t.Errorf("without Access the pages stay open: %d", w.Code)
	}
}
