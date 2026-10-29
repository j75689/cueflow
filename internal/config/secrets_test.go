package config

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSM serves secrets by version name and records what was asked for. With block set, every
// Access waits for it to close or for its ctx to end.
type fakeSM struct {
	values map[string]string
	err    error
	block  chan struct{}

	mu    sync.Mutex
	asked []string
}

func (f *fakeSM) Access(ctx context.Context, name string) ([]byte, error) {
	f.mu.Lock()
	f.asked = append(f.asked, name)
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.values[name]
	if !ok {
		return nil, fmt.Errorf("secret %s not found", name)
	}
	return []byte(v), nil
}

// useSM installs sm as the Secret Manager for the test and counts how many clients were made.
func useSM(t *testing.T, sm SecretManager) *int {
	t.Helper()
	made := 0
	prev := newSecretManager
	newSecretManager = func(context.Context) (SecretManager, error) { made++; return sm, nil }
	t.Cleanup(func() { newSecretManager = prev })
	return &made
}

func writeSecretFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var hexKey = strings.Repeat("ab", 32)

func TestSecretPlainValue(t *testing.T) {
	base(t)
	made := useSM(t, &fakeSM{})
	t.Setenv("CUEFLOW_ADMIN_TOKEN", "plain-token\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AdminToken != "plain-token\n" || *made != 0 {
		t.Fatalf("admin token %q, clients made %d: a plain value is used as is and needs no Secret Manager", c.AdminToken, *made)
	}
}

func TestSecretFromSecretManager(t *testing.T) {
	base(t)
	sm := &fakeSM{values: map[string]string{
		"projects/p/secrets/key/versions/latest":                     hexKey + "\n",
		"projects/p/secrets/admin/versions/3":                        "admin-v3\r\n",
		"projects/p/secrets/mcp/versions/latest":                     "mcp\n\n",
		"projects/p/locations/asia-northeast1/secrets/db/versions/2": "postgres://u:p@db/cueflow",
	}}
	made := useSM(t, sm)
	t.Setenv("CUEFLOW_SECRET_KEY", "gcpsm://projects/p/secrets/key")
	t.Setenv("CUEFLOW_ADMIN_TOKEN", "gcpsm://projects/p/secrets/admin/versions/3")
	t.Setenv("CUEFLOW_MCP_TOKEN", "gcpsm://projects/p/secrets/mcp")
	t.Setenv("DATABASE_URL", "gcpsm://projects/p/locations/asia-northeast1/secrets/db/versions/2")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.SecretKey, bytes.Repeat([]byte{0xab}, 32)) {
		t.Fatalf("secret key from the latest version, newline trimmed: %x", c.SecretKey)
	}
	if c.AdminToken != "admin-v3" || c.MCPToken != "mcp\n" {
		t.Fatalf("one trailing newline is trimmed, as for a file: admin %q, mcp %q", c.AdminToken, c.MCPToken)
	}
	if c.DatabaseURL != "postgres://u:p@db/cueflow" {
		t.Fatalf("a regional secret: %q", c.DatabaseURL)
	}
	if *made != 1 || len(sm.asked) != 4 {
		t.Fatalf("one client, one fetch per reference: clients %d, asked %v", *made, sm.asked)
	}
}

func TestSecretPreviousKeysAreResolvedThenSplit(t *testing.T) {
	base(t)
	other := strings.Repeat("cd", 32)
	useSM(t, &fakeSM{values: map[string]string{"projects/p/secrets/prev/versions/latest": hexKey + ", " + other}})
	t.Setenv("CUEFLOW_SECRET_KEY_PREVIOUS", "gcpsm://projects/p/secrets/prev")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.PreviousSecretKeys) != 2 {
		t.Fatalf("previous keys: %d", len(c.PreviousSecretKeys))
	}
}

func TestSecretPreviousKeysOnePerLine(t *testing.T) {
	base(t)
	t.Setenv("CUEFLOW_SECRET_KEY_PREVIOUS_FILE", writeSecretFile(t, hexKey+"\n"+strings.Repeat("cd", 32)+"\r\n"+strings.Repeat("ef", 32)+"\n"))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.PreviousSecretKeys) != 3 {
		t.Fatalf("one key per line: %d keys", len(c.PreviousSecretKeys))
	}
}

func TestSecretMalformedReference(t *testing.T) {
	for _, ref := range []string{
		"gcpsm://projects/p/secrets",
		"gcpsm://projects/p/secrets/s/versions/",
		"gcpsm://projects/p/secrets/s/extra",
		"gcpsm://projects/p/locations//secrets/s",
		"gcpsm://p/s",
		"gcpsm://projects/p/secrets/s?x=1",
	} {
		t.Run(ref, func(t *testing.T) {
			base(t)
			made := useSM(t, &fakeSM{})
			t.Setenv("CUEFLOW_MCP_TOKEN", ref)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "CUEFLOW_MCP_TOKEN") || !strings.Contains(err.Error(), "malformed") ||
				!strings.Contains(err.Error(), ref) {
				t.Fatalf("want a malformed-reference error naming the variable and the reference, got %v", err)
			}
			if *made != 0 {
				t.Fatalf("nothing is fetched for a malformed reference (clients %d)", *made)
			}
		})
	}
}

func TestSecretFetchErrorStopsStartup(t *testing.T) {
	base(t)
	useSM(t, &fakeSM{err: errors.New("Permission 'secretmanager.versions.access' denied (HTTP 403 PERMISSION_DENIED)")})
	t.Setenv("CUEFLOW_ADMIN_TOKEN", "gcpsm://projects/p/secrets/admin")
	_, err := Load()
	if err == nil {
		t.Fatal("a reference that can't be read must stop startup, not fall back to empty")
	}
	for _, want := range []string{"CUEFLOW_ADMIN_TOKEN", "projects/p/secrets/admin/versions/latest", "403"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should contain %q", err, want)
		}
	}
}

func TestSecretMissingInSecretManager(t *testing.T) {
	base(t)
	useSM(t, &fakeSM{values: map[string]string{}})
	t.Setenv("CUEFLOW_SECRET_KEY", "gcpsm://projects/p/secrets/gone/versions/7")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CUEFLOW_SECRET_KEY: ") {
		t.Fatalf("want an error naming CUEFLOW_SECRET_KEY, got %v", err)
	}
}

func TestSecretNoCredentials(t *testing.T) {
	base(t)
	prev := newSecretManager
	newSecretManager = func(context.Context) (SecretManager, error) { return nil, errors.New("default credentials: not found") }
	t.Cleanup(func() { newSecretManager = prev })
	t.Setenv("DATABASE_URL", "gcpsm://projects/p/secrets/db")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("want an error naming DATABASE_URL and the credentials, got %v", err)
	}
}

// TestSecretFetchesShareOneTimeout: the references are fetched in parallel, so all of them answering
// late still ends within one timeout, not one per reference.
func TestSecretFetchesShareOneTimeout(t *testing.T) {
	base(t)
	prev := secretFetchTimeout
	secretFetchTimeout = 200 * time.Millisecond
	t.Cleanup(func() { secretFetchTimeout = prev })
	sm := &fakeSM{block: make(chan struct{})}
	useSM(t, sm)
	for _, name := range []string{"CUEFLOW_SECRET_KEY", "CUEFLOW_ADMIN_TOKEN", "CUEFLOW_MCP_TOKEN", "DATABASE_URL"} {
		t.Setenv(name, "gcpsm://projects/p/secrets/"+strings.ToLower(name))
	}
	start := time.Now()
	_, err := Load()
	if took := time.Since(start); took > 2*secretFetchTimeout {
		t.Fatalf("four slow references took %s, want about one timeout (%s)", took, secretFetchTimeout)
	}
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "projects/p/secrets/database_url/versions/latest") {
		t.Fatalf("want a timeout error naming the first variable and its reference, got %v", err)
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if len(sm.asked) != 4 {
		t.Fatalf("all four are fetched at once, asked %v", sm.asked)
	}
}

// TestSecretTimeoutBoundsAStuckClient: a client that never comes back (a token fetch that ignores
// every ctx) still ends Load at the timeout.
func TestSecretTimeoutBoundsAStuckClient(t *testing.T) {
	base(t)
	prevTimeout, prevNew := secretFetchTimeout, newSecretManager
	secretFetchTimeout = 100 * time.Millisecond
	stuck := make(chan struct{})
	newSecretManager = func(context.Context) (SecretManager, error) { <-stuck; return nil, errors.New("never") }
	t.Cleanup(func() { close(stuck); secretFetchTimeout, newSecretManager = prevTimeout, prevNew })
	t.Setenv("CUEFLOW_ADMIN_TOKEN", "gcpsm://projects/p/secrets/admin")
	start := time.Now()
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "no answer within") {
		t.Fatalf("want a timeout error, got %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("Load took %s", took)
	}
}

func TestCheapSettingsAreCheckedBeforeSecrets(t *testing.T) {
	base(t)
	made := useSM(t, &fakeSM{})
	t.Setenv("CUEFLOW_ADMIN_TOKEN", "gcpsm://projects/p/secrets/admin")
	t.Setenv("CUEFLOW_QUEUE", "cloudtask")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CUEFLOW_QUEUE") {
		t.Fatalf("want the CUEFLOW_QUEUE error, got %v", err)
	}
	if *made != 0 {
		t.Fatal("a typo in a cheap setting is reported before any Secret Manager call")
	}
}

func TestSecretFromFile(t *testing.T) {
	base(t)
	t.Setenv("CUEFLOW_SECRET_KEY", "")
	t.Setenv("CUEFLOW_SECRET_KEY_FILE", writeSecretFile(t, hexKey+"\n"))
	t.Setenv("DATABASE_URL_FILE", writeSecretFile(t, "postgres://u:p@db/cueflow\r\n"))
	t.Setenv("CUEFLOW_MCP_TOKEN_FILE", writeSecretFile(t, "mcp-token\n\n"))
	t.Setenv("CUEFLOW_ADMIN_TOKEN_FILE", writeSecretFile(t, "admin-token"))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.SecretKey) != 32 || c.DatabaseURL != "postgres://u:p@db/cueflow" {
		t.Fatalf("file values with a trailing newline: key %d bytes, db %q", len(c.SecretKey), c.DatabaseURL)
	}
	if c.MCPToken != "mcp-token\n" || c.AdminToken != "admin-token" {
		t.Fatalf("only one trailing newline is trimmed: mcp %q, admin %q", c.MCPToken, c.AdminToken)
	}
}

func TestSecretEnvAndFileBothSet(t *testing.T) {
	base(t)
	t.Setenv("CUEFLOW_ADMIN_TOKEN", "from-env")
	t.Setenv("CUEFLOW_ADMIN_TOKEN_FILE", writeSecretFile(t, "from-file"))
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "CUEFLOW_ADMIN_TOKEN and CUEFLOW_ADMIN_TOKEN_FILE are both set") {
		t.Fatalf("setting both is a config error, got %v", err)
	}
	if strings.Contains(err.Error(), "from-env") || strings.Contains(err.Error(), "from-file") {
		t.Fatalf("the error must not print either value: %v", err)
	}
}

func TestSecretMissingFile(t *testing.T) {
	base(t)
	t.Setenv("CUEFLOW_SECRET_KEY", "")
	t.Setenv("CUEFLOW_SECRET_KEY_FILE", filepath.Join(t.TempDir(), "absent"))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CUEFLOW_SECRET_KEY_FILE") {
		t.Fatalf("want an error naming CUEFLOW_SECRET_KEY_FILE, got %v", err)
	}
}

func TestSecretManagerREST(t *testing.T) {
	const value = "s3cret-value"
	crc := crc32.Checksum([]byte(value), crc32.MakeTable(crc32.Castagnoli))
	var path, location string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		switch {
		case strings.Contains(r.URL.Path, "/secrets/ok/"):
			fmt.Fprintf(w, `{"name":"x","payload":{"data":%q,"dataCrc32c":"%d"}}`, base64.StdEncoding.EncodeToString([]byte(value)), crc)
		case strings.Contains(r.URL.Path, "/secrets/corrupt/"):
			fmt.Fprintf(w, `{"payload":{"data":%q,"dataCrc32c":"%d"}}`, base64.StdEncoding.EncodeToString([]byte(value)), crc+1)
		default:
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"code":403,"message":"Permission 'secretmanager.versions.access' denied","status":"PERMISSION_DENIED"}}`)
		}
	}))
	defer srv.Close()
	g := &gcpSecretManager{client: srv.Client(), endpoint: func(loc string) string { location = loc; return srv.URL + "/v1/" }}
	ctx := context.Background()

	b, err := g.Access(ctx, "projects/p/secrets/ok/versions/latest")
	if err != nil || string(b) != value || path != "/v1/projects/p/secrets/ok/versions/latest:access" || location != "" {
		t.Fatalf("access: %q %v (path %s, location %q)", b, err, path, location)
	}
	if _, err := g.Access(ctx, "projects/p/locations/asia-northeast1/secrets/ok/versions/2"); err != nil || location != "asia-northeast1" {
		t.Fatalf("a regional secret goes to its region's endpoint: location %q, %v", location, err)
	}
	if _, err := g.Access(ctx, "projects/p/secrets/corrupt/versions/1"); err == nil || strings.Contains(err.Error(), value) {
		t.Fatalf("a checksum mismatch is an error without the value, got %v", err)
	}
	if _, err := g.Access(ctx, "projects/p/secrets/denied/versions/1"); err == nil || !strings.Contains(err.Error(), "PERMISSION_DENIED") {
		t.Fatalf("want the API's error, got %v", err)
	}
	if got := secretManagerEndpoint("asia-northeast1"); got != "https://secretmanager.asia-northeast1.rep.googleapis.com/v1/" {
		t.Fatalf("regional endpoint: %s", got)
	}
}

// TestSecretEmptyIsAnError: an empty mount or payload must not quietly turn off the admin token or
// pick the default database.
func TestSecretEmptyIsAnError(t *testing.T) {
	for _, content := range []string{"", "\n", "\r\n"} {
		t.Run(fmt.Sprintf("file %q", content), func(t *testing.T) {
			base(t)
			t.Setenv("CUEFLOW_ADMIN_TOKEN_FILE", writeSecretFile(t, content))
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CUEFLOW_ADMIN_TOKEN_FILE") || !strings.Contains(err.Error(), "empty") {
				t.Fatalf("want an empty-file error naming the variable, got %v", err)
			}
		})
		t.Run(fmt.Sprintf("gcpsm %q", content), func(t *testing.T) {
			base(t)
			useSM(t, &fakeSM{values: map[string]string{"projects/p/secrets/db/versions/latest": content}})
			t.Setenv("DATABASE_URL", "gcpsm://projects/p/secrets/db")
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "empty") {
				t.Fatalf("want an empty-secret error naming the variable, got %v", err)
			}
		})
	}
}
