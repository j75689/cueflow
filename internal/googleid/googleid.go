// Package googleid lets a connector call a private Cloud Run service: it signs each request with a
// Google ID token of the account CueFlow itself runs as (the metadata server hands it out; no key
// file), made out to the service's URL.
//
// Cloud Run accepts the token in X-Serverless-Authorization, which it strips before the app sees
// the request, so the Authorization header stays free for the app's own bearer token and the two
// protections can be used together.
package googleid

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
)

// Header is where Cloud Run reads the caller's ID token without taking over Authorization.
const Header = "X-Serverless-Authorization"

// NewTokenSource is idtoken.NewTokenSource; tests replace it.
var NewTokenSource = func(ctx context.Context, audience string) (oauth2.TokenSource, error) {
	return idtoken.NewTokenSource(ctx, audience)
}

// Audience is the origin of a service URL ("https://svc-123.a.run.app"), which is what Cloud Run
// wants an ID token made out to, whatever path the endpoint has.
func Audience(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("googleid: %q is not a full URL", endpoint)
	}
	return u.Scheme + "://" + u.Host, nil
}

type transport struct {
	base http.RoundTripper
	src  oauth2.TokenSource
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	tok, err := t.src.Token() // cached and refreshed by the token source
	if err != nil {
		return nil, fmt.Errorf("googleid: ID token: %w", err)
	}
	r = r.Clone(r.Context()) // never modify the caller's request
	r.Header.Set(Header, "Bearer "+tok.AccessToken)
	return t.base.RoundTrip(r)
}

// Client returns a copy of base (nil means a default client) whose requests carry an ID token made
// out to the origin of endpoint. It fails when this process has no Google identity to sign with
// (outside Google Cloud and without application default credentials).
func Client(ctx context.Context, base *http.Client, endpoint string) (*http.Client, error) {
	aud, err := Audience(endpoint)
	if err != nil {
		return nil, err
	}
	src, err := NewTokenSource(ctx, aud)
	if err != nil {
		return nil, fmt.Errorf("googleid: no Google identity to sign requests with (it is available on Cloud Run, or after `gcloud auth application-default login`): %w", err)
	}
	if src == nil {
		return nil, errors.New("googleid: no token source")
	}
	c := &http.Client{}
	if base != nil {
		*c = *base
	}
	rt := c.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	c.Transport = &transport{base: rt, src: src}
	return c, nil
}
