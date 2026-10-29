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
	"testing"
)

// fakeSM serves secrets by version name and records what was asked for.
type fakeSM struct {
	values map[string]string
	err    error
	asked  []string
}

func (f *fakeSM) Access(_ context.Context, name string) ([]byte, error) {
	f.asked = append(f.asked, name)
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
	newSecretManager = func() (SecretManager, error) { made++; return sm, nil }
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
	t.Setenv("CUEFLOW_ADMIN_TOKEN", "plain-token")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AdminToken != "plain-token" || *made != 0 {
		t.Fatalf("admin token %q, clients made %d: a plain value is used as is and needs no Secret Manager", c.AdminToken, *made)
	}
}

func TestSecretFromSecretManager(t *testing.T) {
	base(t)
	sm := &fakeSM{values: map[string]string{
		"projects/p/secrets/key/versions/latest": hexKey,
		"projects/p/secrets/admin/versions/3":    "admin-v3\n",
	}}
	made := useSM(t, sm)
	t.Setenv("CUEFLOW_SECRET_KEY", "gcpsm://projects/p/secrets/key")
	t.Setenv("CUEFLOW_ADMIN_TOKEN", "gcpsm://projects/p/secrets/admin/versions/3")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.SecretKey, bytes.Repeat([]byte{0xab}, 32)) {
		t.Fatalf("secret key from the latest version: %x", c.SecretKey)
	}
	if c.AdminToken != "admin-v3\n" {
		t.Fatalf("a Secret Manager value is used exactly as stored, got %q", c.AdminToken)
	}
	if *made != 1 || len(sm.asked) != 2 {
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

func TestSecretMalformedReference(t *testing.T) {
	for _, ref := range []string{
		"gcpsm://projects/p/secrets",
		"gcpsm://projects/p/secrets/s/versions/",
		"gcpsm://projects/p/secrets/s/extra",
		"gcpsm://p/s",
		"gcpsm://projects/p/secrets/s?x=1",
	} {
		t.Run(ref, func(t *testing.T) {
			base(t)
			made := useSM(t, &fakeSM{})
			t.Setenv("CUEFLOW_MCP_TOKEN", ref)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "CUEFLOW_MCP_TOKEN") || !strings.Contains(err.Error(), "malformed") {
				t.Fatalf("want a malformed-reference error naming the variable, got %v", err)
			}
			if strings.Contains(err.Error(), ref) || *made != 0 {
				t.Fatalf("the value must not be printed and nothing fetched: %v (clients %d)", err, *made)
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
	newSecretManager = func() (SecretManager, error) { return nil, errors.New("default credentials: not found") }
	t.Cleanup(func() { newSecretManager = prev })
	t.Setenv("DATABASE_URL", "gcpsm://projects/p/secrets/db")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("want an error naming DATABASE_URL and the credentials, got %v", err)
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

func TestSecretEnvWinsOverFile(t *testing.T) {
	base(t)
	t.Setenv("CUEFLOW_ADMIN_TOKEN", "from-env")
	t.Setenv("CUEFLOW_ADMIN_TOKEN_FILE", filepath.Join(t.TempDir(), "absent"))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AdminToken != "from-env" {
		t.Fatalf("<NAME> wins over <NAME>_FILE, got %q", c.AdminToken)
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
	var path string
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
	g := &gcpSecretManager{client: srv.Client(), endpoint: srv.URL + "/v1/"}
	ctx := context.Background()

	b, err := g.Access(ctx, "projects/p/secrets/ok/versions/latest")
	if err != nil || string(b) != value || path != "/v1/projects/p/secrets/ok/versions/latest:access" {
		t.Fatalf("access: %q %v (path %s)", b, err, path)
	}
	if _, err := g.Access(ctx, "projects/p/secrets/corrupt/versions/1"); err == nil || strings.Contains(err.Error(), value) {
		t.Fatalf("a checksum mismatch is an error without the value, got %v", err)
	}
	if _, err := g.Access(ctx, "projects/p/secrets/denied/versions/1"); err == nil || !strings.Contains(err.Error(), "PERMISSION_DENIED") {
		t.Fatalf("want the API's error, got %v", err)
	}
}
