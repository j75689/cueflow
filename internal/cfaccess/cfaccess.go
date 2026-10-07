// Package cfaccess recognizes requests that passed Cloudflare Access (Zero Trust). Access sits in
// front of a hostname and, once someone has logged in, forwards their requests with a JWT signed
// by the team's keys for that application's audience (the Cf-Access-Jwt-Assertion header, and the
// CF_Authorization cookie in a browser).
//
// Cloudflare cannot protect the service's own *.run.app address, which stays reachable by anyone,
// so the service checks the JWT itself instead of trusting that traffic came through Cloudflare.
package cfaccess

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// HeaderJWT is the header Access adds to every request it lets through.
const HeaderJWT = "Cf-Access-Jwt-Assertion"

// CookieJWT is the cookie a logged-in browser carries.
const CookieJWT = "CF_Authorization"

// Verifier checks Access JWTs for one application.
type Verifier struct {
	v *oidc.IDTokenVerifier
}

// Issuer is the token issuer of a team, "https://<team>.cloudflareaccess.com". The team may be
// given with or without the scheme.
func Issuer(teamDomain string) string {
	t := strings.TrimSuffix(strings.TrimSpace(teamDomain), "/")
	return "https://" + strings.TrimPrefix(t, "https://")
}

// New verifies against the team's published keys (fetched lazily and cached by the oidc package).
// ctx must outlive the verifier: it is used for every later key refresh.
func New(ctx context.Context, teamDomain, audience string) *Verifier {
	issuer := Issuer(teamDomain)
	keys := oidc.NewRemoteKeySet(ctx, issuer+"/cdn-cgi/access/certs")
	return NewWithKeys(issuer, keys, audience)
}

// NewWithKeys is New with a caller-supplied key set (tests).
func NewWithKeys(issuer string, keys oidc.KeySet, audience string) *Verifier {
	return &Verifier{v: oidc.NewVerifier(issuer, keys, &oidc.Config{ClientID: audience})}
}

// ErrNoToken means the request did not come through Access.
var ErrNoToken = errors.New("cfaccess: no Access token")

// Verify returns the logged-in user's email if r carries a valid Access JWT for this
// application. ErrNoToken means there was none; any other error means it was invalid or expired.
func (v *Verifier) Verify(r *http.Request) (email string, err error) {
	token := r.Header.Get(HeaderJWT)
	if token == "" {
		if c, cerr := r.Cookie(CookieJWT); cerr == nil {
			token = c.Value
		}
	}
	if token == "" {
		return "", ErrNoToken
	}
	t, err := v.v.Verify(r.Context(), token)
	if err != nil {
		return "", err
	}
	var claims struct {
		Email      string `json:"email"`
		CommonName string `json:"common_name"`
	}
	if err := t.Claims(&claims); err != nil {
		return "", err
	}
	// Service tokens (machine clients) have no email; they are identified by their common name.
	if claims.Email == "" {
		return claims.CommonName, nil
	}
	return claims.Email, nil
}
