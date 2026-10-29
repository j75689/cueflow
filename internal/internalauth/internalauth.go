// Package internalauth protects /internal/* (Cloud Tasks deliveries and the Cloud Scheduler tick)
// by verifying the Google-signed OIDC token they carry.
package internalauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"google.golang.org/api/idtoken"
)

// Verifier checks a bearer token and returns the caller's email.
type Verifier interface {
	Verify(ctx context.Context, bearer string) (email string, err error)
}

// ErrForbidden means the token is valid but the caller is not on the allow list.
var ErrForbidden = errors.New("internalauth: caller not allowed")

type googleVerifier struct {
	audiences map[string]bool
	allowed   map[string]bool
	validate  func(ctx context.Context, token, audience string) (*idtoken.Payload, error)
}

// NewGoogleVerifier accepts Google ID tokens made out to any of audiences whose email is verified and in
// allowedEmails (case-insensitive). The token's audience is checked only against this list, so it must name
// at least one.
func NewGoogleVerifier(audiences, allowedEmails []string) (Verifier, error) {
	auds := make(map[string]bool, len(audiences))
	for _, a := range audiences {
		if a = strings.TrimSpace(a); a != "" {
			auds[a] = true
		}
	}
	if len(auds) == 0 {
		return nil, errors.New("internalauth: no audience to accept")
	}
	allowed := make(map[string]bool, len(allowedEmails))
	for _, e := range allowedEmails {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			allowed[e] = true
		}
	}
	return &googleVerifier{audiences: auds, allowed: allowed, validate: idtoken.Validate}, nil
}

func (g *googleVerifier) Verify(ctx context.Context, bearer string) (string, error) {
	// "" checks the signature and expiry only; the audience is checked against the list below.
	p, err := g.validate(ctx, bearer, "")
	if err != nil {
		return "", err
	}
	if !g.audiences[p.Audience] {
		return "", errors.New("internalauth: audience mismatch")
	}
	verified, _ := p.Claims["email_verified"].(bool)
	if !verified {
		return "", errors.New("internalauth: email not verified")
	}
	email, _ := p.Claims["email"].(string)
	email = strings.ToLower(email)
	if email == "" {
		return "", errors.New("internalauth: token has no email")
	}
	if !g.allowed[email] {
		return email, ErrForbidden
	}
	return email, nil
}

type ctxKey struct{}

// EmailFrom returns the verified caller email put in the context by Middleware.
func EmailFrom(ctx context.Context) string {
	s, _ := ctx.Value(ctxKey{}).(string)
	return s
}

// Middleware requires a valid Authorization: Bearer token. Missing or invalid tokens get 401; a
// valid token from a caller that is not allowed gets 403 (Verifier returns ErrForbidden).
func Middleware(v Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			scheme, token, ok := strings.Cut(h, " ")
			token = strings.TrimSpace(token)
			if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
				writeErr(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			email, err := v.Verify(r.Context(), token)
			if errors.Is(err, ErrForbidden) {
				writeErr(w, http.StatusForbidden, "caller not allowed")
				return
			}
			if err != nil || email == "" {
				writeErr(w, http.StatusUnauthorized, "invalid token")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, email)))
		})
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
