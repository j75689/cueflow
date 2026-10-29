package runner

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/runnerapi"
)

const secret = "s3cret-value"

func TestHeadersFromFileAndEnvironment(t *testing.T) {
	t.Setenv("CUEFLOW_RUNNER_HEADERS", "cf-access-client-secret: from-env\r\n\nX-Extra:  two  \n")
	cfg, err := LoadConfig(writeConfig(t, `{"url":"https://x","token":"t","headers":{"CF-Access-Client-Id":"id","CF-Access-Client-Secret":"from-file"}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Cf-Access-Client-Id": "id", "Cf-Access-Client-Secret": "from-env", "X-Extra": "two"}
	if len(cfg.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", cfg.Headers, want)
	}
	for k, v := range want {
		if cfg.Headers[k] != v {
			t.Errorf("%s = %q, want %q (the environment wins for a name in both)", k, cfg.Headers[k], v)
		}
	}
}

func TestHeadersRefused(t *testing.T) {
	for name, body := range map[string]string{
		"Authorization": `{"authorization":"Bearer ` + secret + `"}`,
		"Content-Type":  `{"Content-Type":"` + secret + `"}`,
		"Host":          `{"Host":"` + secret + `"}`,
		"bad name":      `{"Bad Name":"` + secret + `"}`,
		"line break":    `{"X-A":"` + secret + `\nX-B: 1"}`,
		"empty value":   `{"X-A":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CUEFLOW_RUNNER_HEADERS", "")
			_, err := LoadConfig(writeConfig(t, `{"url":"https://x","token":"t","headers":`+body+`}`))
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("the error shows the value: %v", err)
			}
		})
	}
	t.Setenv("CUEFLOW_RUNNER_HEADERS", "no colon "+secret)
	_, err := LoadConfig(writeConfig(t, `{"url":"https://x","token":"t"}`))
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("malformed CUEFLOW_RUNNER_HEADERS: %v", err)
	}
}

// gate stands in for an access proxy: requests without the headers get its login page.
func gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("CF-Access-Client-Id") != "id" || r.Header.Get("CF-Access-Client-Secret") != secret {
			http.Redirect(w, r, "https://team.cloudflareaccess.com/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// captureStdout returns what f printed.
func captureStdout(t *testing.T, f func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	ferr := f()
	os.Stdout = old
	w.Close()
	var b bytes.Buffer
	_, _ = io.Copy(&b, r)
	if ferr != nil {
		t.Fatal(ferr)
	}
	return b.String()
}

func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	f()
	os.Stderr = old
	w.Close()
	var b bytes.Buffer
	_, _ = io.Copy(&b, r)
	return b.String()
}

// register sends --header and saves it; CUEFLOW_RUNNER_HEADERS is sent but stays in the environment.
// status sends them too and shows only the names.
func TestRegisterAndStatusSendHeaders(t *testing.T) {
	var registered bool
	ts := httptest.NewServer(gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case runnerapi.BasePath + "/register":
			if r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("register headers: %v", r.Header)
			}
			registered = true
			_ = json.NewEncoder(w).Encode(runnerapi.RegisterResponse{Token: "cfr_x", RunnerID: "r1", Name: "box"})
		case "/api/v1/health":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})))
	defer ts.Close()
	path := t.TempDir() + "/runner.json"

	t.Setenv("CUEFLOW_RUNNER_HEADERS", "")
	if err := Main([]string{"register", "--url", ts.URL, "--token", "cfreg", "--config", path, "--header", "CF-Access-Client-Id: id"}, "test"); err == nil ||
		!strings.Contains(err.Error(), "cloudflareaccess.com") {
		t.Fatalf("a request the access proxy turns away must say where it was sent: %v", err)
	}

	t.Setenv("CUEFLOW_RUNNER_HEADERS", "CF-Access-Client-Secret: "+secret)
	captureStdout(t, func() error {
		return Main([]string{"register", "--url", ts.URL, "--token", "cfreg", "--config", path, "--header", "CF-Access-Client-Id: id"}, "test")
	})
	if !registered {
		t.Fatal("not registered")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"Cf-Access-Client-Id": "id"`) || strings.Contains(string(raw), secret) {
		t.Fatalf("runner.json must hold --header, not CUEFLOW_RUNNER_HEADERS:\n%s", raw)
	}

	out := captureStdout(t, func() error { return Main([]string{"status", "--config", path}, "test") })
	if !strings.Contains(out, "reachable (HTTP 200)") || !strings.Contains(out, "Cf-Access-Client-Id, Cf-Access-Client-Secret") || strings.Contains(out, secret) {
		t.Fatalf("status:\n%s", out)
	}

	if err := Main([]string{"register", "--url", ts.URL, "--token", "cfreg", "--config", path, "--header", "Authorization: Bearer x"}, "test"); err == nil {
		t.Fatal("--header must not override Authorization")
	}
	stderr := captureStderr(t, func() {
		err := Main([]string{"register", "--url", ts.URL, "--token", "cfreg", "--config", path, "--header", "X-A: 1", "--header", secret}, "test")
		if err == nil || !strings.Contains(err.Error(), "#2") || strings.Contains(err.Error(), secret) {
			t.Errorf("malformed --header: %v", err)
		}
	})
	if strings.Contains(stderr, secret) {
		t.Fatalf("a malformed --header was printed:\n%s", stderr)
	}
}

// An agent reaches its job's tool server through the loopback proxy, which adds the headers; nothing else
// is forwarded, and the agent's URL carries no header value.
func TestMCPProxy(t *testing.T) {
	ts := httptest.NewServer(gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "tools at "+r.URL.Path)
	})))
	defer ts.Close()
	p, err := startMCPProxy(&Config{URL: ts.URL, Headers: map[string]string{"Cf-Access-Client-Id": "id", "Cf-Access-Client-Secret": secret}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	get := func(u string) (int, string) {
		resp, err := http.Post(u, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	u := p.serve("/mcp/run/tok1")
	if !strings.HasPrefix(u, "http://127.0.0.1:") || strings.Contains(u, secret) {
		t.Fatalf("agent URL %q", u)
	}
	if code, body := get(u); code != 200 || body != "tools at /mcp/run/tok1" {
		t.Fatalf("job's tool server: %d %q", code, body)
	}
	if code, _ := get(p.base + "/api/v1/tasks"); code != http.StatusNotFound {
		t.Fatalf("the rest of CueFlow must not be forwarded: %d", code)
	}
	p.release("/mcp/run/tok1")
	if code, _ := get(u); code != http.StatusNotFound {
		t.Fatalf("a finished job's tool server must not be forwarded: %d", code)
	}
}
