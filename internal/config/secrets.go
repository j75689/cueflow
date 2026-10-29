package config

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
)

// SecretVars are the variables that hold a credential. Load reads each one with secrets.env, which also
// takes a Secret Manager reference (gcpsm://...) or <NAME>_FILE; see loadSecrets. A new credential
// variable belongs here, read the same way: env would silently skip both forms.
var SecretVars = []string{
	"DATABASE_URL", "DATABASE_SESSION_URL",
	"CUEFLOW_SECRET_KEY", "CUEFLOW_SECRET_KEY_PREVIOUS",
	"CUEFLOW_ADMIN_TOKEN", "CUEFLOW_MCP_TOKEN",
}

// SecretManager reads one secret version. name is "projects/P/secrets/S/versions/V", or
// "projects/P/locations/L/secrets/S/versions/V" for a regional secret.
type SecretManager interface {
	Access(ctx context.Context, name string) ([]byte, error)
}

// newSecretManager builds the Secret Manager client the first time a gcpsm:// reference is met, so a
// deployment that uses none needs no Google credentials. Tests replace it.
var newSecretManager = func(ctx context.Context) (SecretManager, error) {
	c, err := google.DefaultClient(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("default credentials: %w", err)
	}
	cc := *c
	cc.Timeout = secretFetchTimeout
	return &gcpSecretManager{client: &cc, endpoint: secretManagerEndpoint}, nil
}

// secretFetchTimeout bounds reading all of Load's Secret Manager references, which are fetched together.
// A var so tests can shorten it.
var secretFetchTimeout = 10 * time.Second

const secretRefPrefix = "gcpsm://"

// secretRef is a Secret Manager reference:
// gcpsm://projects/<project>[/locations/<location>]/secrets/<secret>[/versions/<version>].
var secretRef = regexp.MustCompile(`^gcpsm://projects/([A-Za-z0-9._:-]+)/(locations/[a-z0-9-]+/)?secrets/([A-Za-z0-9_-]+)(?:/versions/([A-Za-z0-9_-]+))?$`)

// parseSecretRef turns a reference into the version name Secret Manager reads.
func parseSecretRef(v string) (string, error) {
	m := secretRef.FindStringSubmatch(v)
	if m == nil {
		return "", fmt.Errorf("malformed Secret Manager reference %q: want gcpsm://projects/<project>[/locations/<location>]/secrets/<secret>[/versions/<version>]", v)
	}
	version := m[4]
	if version == "" {
		version = "latest"
	}
	return "projects/" + m[1] + "/" + m[2] + "secrets/" + m[3] + "/versions/" + version, nil
}

// trimNewline drops one trailing newline ("\n" or "\r\n"): echo and most editors add one, to a file
// and to `gcloud secrets create --data-file=-` alike.
func trimNewline(s string) string {
	if t, ok := strings.CutSuffix(s, "\r\n"); ok {
		return t
	}
	return strings.TrimSuffix(s, "\n")
}

// secrets holds the resolved value of every name loadSecrets was given ("" for unset).
type secrets map[string]string

// env is env for a secret variable: its resolved value, or def when that is empty.
func (s secrets) env(name, def string) string {
	v, ok := s[name]
	if !ok {
		panic("config: " + name + " is read as a secret but is not in SecretVars")
	}
	if v == "" {
		return def
	}
	return v
}

// loadSecrets resolves each of names:
//
//  1. <NAME> set and non-empty: a value starting with gcpsm:// is read from Secret Manager, anything
//     else is used as is;
//  2. <NAME>_FILE set: that file;
//  3. neither: "".
//
// Setting both is an error. A Secret Manager value and a file lose one trailing newline; one that is
// then empty is an error, so an empty mount can't quietly turn off the admin token or pick the default
// database. The references
// are fetched in parallel within secretFetchTimeout. A reference that can't be read is an error naming
// the variable and the reference, never the value.
func loadSecrets(ctx context.Context, names []string) (secrets, error) {
	out := secrets{}
	type fetch struct{ name, ref string }
	var fetches []fetch
	for _, name := range names {
		v, file := os.Getenv(name), os.Getenv(name+"_FILE")
		switch {
		case v != "" && file != "":
			return nil, fmt.Errorf("%s and %s_FILE are both set: set only one of them", name, name)
		case strings.HasPrefix(v, secretRefPrefix):
			ref, err := parseSecretRef(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			fetches = append(fetches, fetch{name, ref})
		case v != "":
			out[name] = v
		case file != "":
			b, err := os.ReadFile(file)
			if err != nil {
				return nil, fmt.Errorf("%s_FILE: %w", name, err) // the error names the path, not the content
			}
			if out[name] = trimNewline(string(b)); out[name] == "" {
				return nil, fmt.Errorf("%s_FILE: %s is empty", name, file)
			}
		default:
			out[name] = ""
		}
	}
	if len(fetches) == 0 {
		return out, nil
	}

	// The wait below, not the client, enforces the timeout: the OAuth token fetch inside the client's
	// RoundTrip sees neither the request's ctx nor Client.Timeout.
	ctx, cancel := context.WithTimeout(ctx, secretFetchTimeout)
	defer cancel()
	answers := make([]chan secretAnswer, len(fetches))
	for i := range answers {
		answers[i] = make(chan secretAnswer, 1)
	}
	newSM := newSecretManager // read once: a stuck goroutine may outlive this call
	go func() {
		sm, err := newSM(ctx)
		for i, f := range fetches {
			if err != nil {
				answers[i] <- secretAnswer{err: err}
				continue
			}
			go func() {
				b, err := sm.Access(ctx, f.ref)
				answers[i] <- secretAnswer{trimNewline(string(b)), err}
			}()
		}
	}()
	for i, f := range fetches {
		r, ok := awaitAnswer(ctx, answers[i])
		switch {
		case !ok:
			return nil, fmt.Errorf("%s: reading Secret Manager secret %s: no answer within %s", f.name, f.ref, secretFetchTimeout)
		case r.err != nil:
			return nil, fmt.Errorf("%s: reading Secret Manager secret %s: %w", f.name, f.ref, r.err)
		case r.v == "":
			return nil, fmt.Errorf("%s: Secret Manager secret %s is empty", f.name, f.ref)
		}
		out[f.name] = r.v
	}
	return out, nil
}

// secretAnswer is one fetch's outcome.
type secretAnswer struct {
	v   string
	err error
}

// awaitAnswer waits for ch until ctx ends; false means no answer came. An answer already in ch wins
// over an ended ctx: select picks at random among ready cases, so it is checked again before giving up.
func awaitAnswer(ctx context.Context, ch <-chan secretAnswer) (secretAnswer, bool) {
	select {
	case r := <-ch:
		return r, true
	case <-ctx.Done():
		select {
		case r := <-ch:
			return r, true
		default:
			return secretAnswer{}, false
		}
	}
}

var locationOf = regexp.MustCompile(`^projects/[^/]+/locations/([^/]+)/`)

// secretManagerEndpoint is the API base for a location: global secrets use the global endpoint,
// regional ones their region's.
func secretManagerEndpoint(location string) string {
	if location == "" {
		return "https://secretmanager.googleapis.com/v1/"
	}
	return "https://secretmanager." + location + ".rep.googleapis.com/v1/"
}

// gcpSecretManager is Secret Manager's REST API: GET v1/{name}:access.
type gcpSecretManager struct {
	client   *http.Client
	endpoint func(location string) string
}

func (g *gcpSecretManager) Access(ctx context.Context, name string) ([]byte, error) {
	var location string
	if m := locationOf.FindStringSubmatch(name); m != nil {
		location = m[1]
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.endpoint(location)+name+":access", nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// An error body carries a message such as "Permission ... denied", never a payload.
		var e struct {
			Error struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("%s (HTTP %d %s)", e.Error.Message, resp.StatusCode, e.Error.Status)
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Payload struct {
			Data       string `json:"data"`
			DataCrc32C string `json:"dataCrc32c"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, errors.New("unreadable response")
	}
	data, err := base64.StdEncoding.DecodeString(out.Payload.Data)
	if err != nil {
		return nil, errors.New("unreadable payload")
	}
	if out.Payload.DataCrc32C != "" {
		want, err := strconv.ParseUint(out.Payload.DataCrc32C, 10, 32)
		if err != nil || crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli)) != uint32(want) {
			return nil, errors.New("payload checksum mismatch")
		}
	}
	return data, nil
}
