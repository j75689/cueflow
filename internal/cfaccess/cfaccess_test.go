package cfaccess

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	team = "https://acme.cloudflareaccess.com"
	aud  = "app-aud-0123456789abcdef"
)

func key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mint(t *testing.T, k *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: k}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func valid(extra map[string]any) map[string]any {
	c := map[string]any{"iss": team, "aud": []string{aud}, "email": "me@example.com",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	for k, v := range extra {
		c[k] = v
	}
	return c
}

func TestVerify(t *testing.T) {
	k, other := key(t), key(t)
	v := NewWithKeys(team, &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&k.PublicKey}}, aud)

	req := func(header, cookie string) *http.Request {
		r := httptest.NewRequest("GET", "/api/v1/tasks", nil)
		if header != "" {
			r.Header.Set(HeaderJWT, header)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: CookieJWT, Value: cookie})
		}
		return r
	}

	good := mint(t, k, valid(nil))
	if email, err := v.Verify(req(good, "")); err != nil || email != "me@example.com" {
		t.Fatalf("header: %q %v", email, err)
	}
	if email, err := v.Verify(req("", good)); err != nil || email != "me@example.com" {
		t.Fatalf("cookie: %q %v", email, err)
	}
	svc := mint(t, k, valid(map[string]any{"email": "", "common_name": "cueflow-access.access"}))
	if who, err := v.Verify(req(svc, "")); err != nil || who != "cueflow-access.access" {
		t.Fatalf("service token: %q %v", who, err)
	}

	if _, err := v.Verify(req("", "")); !errors.Is(err, ErrNoToken) {
		t.Fatalf("no token must be ErrNoToken, got %v", err)
	}
	for name, tok := range map[string]string{
		"another application's audience": mint(t, k, valid(map[string]any{"aud": []string{"some-other-app"}})),
		"another team":                   mint(t, k, valid(map[string]any{"iss": "https://evil.cloudflareaccess.com"})),
		"expired":                        mint(t, k, valid(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})),
		"signed with an unknown key":     mint(t, other, valid(nil)),
		"garbage":                        "not.a.jwt",
	} {
		if _, err := v.Verify(req(tok, "")); err == nil || errors.Is(err, ErrNoToken) {
			t.Errorf("%s must be refused as invalid, got %v", name, err)
		}
	}
}

func TestIssuer(t *testing.T) {
	for in, want := range map[string]string{
		"acme.cloudflareaccess.com":          team,
		"https://acme.cloudflareaccess.com/": team,
		" acme.cloudflareaccess.com ":        team,
	} {
		if got := Issuer(in); got != want {
			t.Errorf("Issuer(%q) = %q, want %q", in, got, want)
		}
	}
}
