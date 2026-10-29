package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/config"
	"github.com/j75689/cueflow/internal/internalauth"
)

// tokenVerifier knows one token per caller and allows the accounts it was built with.
type tokenVerifier struct{ allowed []string }

func (v tokenVerifier) Verify(_ context.Context, bearer string) (string, error) {
	email, ok := map[string]string{"render-token": "render@p.iam", "tasks-token": "tasks@p.iam"}[bearer]
	if !ok {
		return "", errors.New("bad token")
	}
	if !slices.Contains(v.allowed, email) {
		return email, internalauth.ErrForbidden
	}
	return email, nil
}

func TestInternalRoutesByMode(t *testing.T) {
	var audiences [][]string
	verifier := func(auds, accounts []string) (internalauth.Verifier, error) {
		audiences = append(audiences, auds)
		return tokenVerifier{accounts}, nil
	}
	call := func(h http.Handler, path, token string) int {
		r := httptest.NewRequest("POST", path, strings.NewReader("not json"))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	handler := func(cfg *config.Config) http.Handler {
		s := &api.Server{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		var err error
		if s.InternalAuth, s.MediaRegisterAuth, err = internalGuards(cfg, verifier); err != nil {
			t.Fatal(err)
		}
		return s.Handler()
	}

	auds := []string{"https://cueflow.example.com", "http://10.0.0.2:3000"}
	single := handler(&config.Config{Tick: "internal", InternalAudiences: auds,
		InternalServiceAccounts: []string{"tasks@p.iam"}, MediaRegisterAccounts: []string{"render@p.iam"}})
	if len(audiences) != 1 || !slices.Equal(audiences[0], auds) {
		t.Fatalf("media register must accept every listed audience: %v", audiences)
	}
	for path, want := range map[string]int{"/internal/tick": 404, "/internal/work/crew/00000000-0000-0000-0000-000000000000": 404} {
		if got := call(single, path, "tasks-token"); got != want {
			t.Errorf("single mode %s: %d, want %d", path, got, want)
		}
	}
	for token, want := range map[string]int{
		"":             401,
		"forged":       401,
		"tasks-token":  403, // a valid caller that is not a media producer
		"render-token": 400, // past the guard: the handler refuses the body
	} {
		if got := call(single, "/internal/media/register", token); got != want {
			t.Errorf("single mode register with %q: %d, want %d", token, got, want)
		}
	}

	none := handler(&config.Config{Tick: "internal", InternalAudiences: auds})
	for _, path := range []string{"/internal/media/register", "/internal/tick"} {
		if got := call(none, path, "render-token"); got != 404 {
			t.Errorf("%s with no internal route on: %d, want 404", path, got)
		}
	}

	// The real verifier refuses an empty audience list rather than accepting any audience.
	if _, _, err := internalGuards(&config.Config{Tick: "internal", MediaRegisterAccounts: []string{"render@p.iam"}},
		internalauth.NewGoogleVerifier); err == nil || !strings.Contains(err.Error(), "CUEFLOW_INTERNAL_AUDIENCE") {
		t.Errorf("no audience: %v", err)
	}

	cloud := handler(&config.Config{Tick: "external", InternalAudiences: auds,
		InternalServiceAccounts: []string{"tasks@p.iam"}, MediaRegisterAccounts: []string{"render@p.iam"}})
	if got := call(cloud, "/internal/tick", "render-token"); got != 403 {
		t.Errorf("cloud tick with a media producer's token: %d, want 403", got)
	}
	if got := call(cloud, "/internal/media/register", "render-token"); got != 400 {
		t.Errorf("cloud register: %d, want 400", got)
	}
}
