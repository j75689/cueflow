package internalauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/api/idtoken"
)

type fake struct {
	email string
	err   error
}

func (f fake) Verify(_ context.Context, b string) (string, error) {
	if b != "good" && f.err == nil {
		return "", errors.New("bad")
	}
	return f.email, f.err
}

func run(v Verifier, auth string) (*httptest.ResponseRecorder, string) {
	var seen string
	h := Middleware(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = EmailFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	r := httptest.NewRequest("POST", "/internal/tick", nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w, seen
}

func TestMiddleware(t *testing.T) {
	w, seen := run(fake{email: "a@b"}, "Bearer good")
	if w.Code != 204 || seen != "a@b" {
		t.Fatalf("code=%d seen=%q", w.Code, seen)
	}
	for name, auth := range map[string]string{"missing": "", "basic": "Basic x", "empty": "Bearer ", "invalid": "Bearer nope"} {
		w, _ := run(fake{email: "a@b"}, auth)
		if w.Code != 401 || !strings.Contains(w.Body.String(), `"error"`) {
			t.Errorf("%s: code=%d body=%s", name, w.Code, w.Body)
		}
	}
	w, _ = run(fake{err: ErrForbidden}, "Bearer good")
	if w.Code != 403 {
		t.Fatalf("forbidden code=%d", w.Code)
	}
}

func TestGoogleVerifier(t *testing.T) {
	payload := func(aud string, claims map[string]any) func(context.Context, string, string) (*idtoken.Payload, error) {
		return func(context.Context, string, string) (*idtoken.Payload, error) {
			return &idtoken.Payload{Audience: aud, Claims: claims}, nil
		}
	}
	mk := func(fn func(context.Context, string, string) (*idtoken.Payload, error)) *googleVerifier {
		v := NewGoogleVerifier("aud", []string{"Tasks@P.iam.gserviceaccount.com"}).(*googleVerifier)
		v.validate = fn
		return v
	}
	ok := map[string]any{"email": "tasks@p.iam.gserviceaccount.com", "email_verified": true}
	if e, err := mk(payload("aud", ok)).Verify(context.Background(), "t"); err != nil || e != "tasks@p.iam.gserviceaccount.com" {
		t.Fatalf("e=%q err=%v", e, err)
	}
	if _, err := mk(payload("other", ok)).Verify(context.Background(), "t"); err == nil || errors.Is(err, ErrForbidden) {
		t.Fatalf("audience: %v", err)
	}
	if _, err := mk(payload("aud", map[string]any{"email": "tasks@p.iam.gserviceaccount.com"})).Verify(context.Background(), "t"); err == nil {
		t.Fatal("unverified email accepted")
	}
	if _, err := mk(payload("aud", map[string]any{"email": "evil@x.com", "email_verified": true})).Verify(context.Background(), "t"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("want forbidden, got %v", err)
	}
	bad := func(context.Context, string, string) (*idtoken.Payload, error) { return nil, errors.New("sig") }
	if _, err := mk(bad).Verify(context.Background(), "t"); err == nil || errors.Is(err, ErrForbidden) {
		t.Fatalf("invalid token: %v", err)
	}
}
