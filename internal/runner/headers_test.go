package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/runnerapi"
)

const secret = "s3cret-value"

func TestHeadersFromFileAndEnvironment(t *testing.T) {
	t.Setenv("CUEFLOW_RUNNER_HEADERS", "# Cloudflare Access\ncf-access-client-secret: from-env\r\n\nX-Extra:  two  \n")
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
		"control byte":  `{"X-A":"` + secret + `\u0007"}`,
		"DEL":           `{"X-A":"` + secret + `\u007f"}`,
		"empty value":   `{"X-A":""}`,
		"case twice":    `{"x-a":"` + secret + `","X-A":"` + secret + `"}`,
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
			if !strings.Contains(err.Error(), "runner.json") {
				t.Fatalf("the error must name the file: %v", err)
			}
		})
	}
	for name, env := range map[string]string{
		"malformed":  "# ok\nno colon " + secret,
		"owned":      "Host: " + secret,
		"case twice": "x-a: " + secret + "\nX-A: " + secret,
		"control":    "X-A: " + secret + "\x01",
	} {
		t.Setenv("CUEFLOW_RUNNER_HEADERS", env)
		_, err := LoadConfig(writeConfig(t, `{"url":"https://x","token":"t"}`))
		if err == nil || strings.Contains(err.Error(), secret) || !strings.HasPrefix(err.Error(), "CUEFLOW_RUNNER_HEADERS") {
			t.Errorf("%s CUEFLOW_RUNNER_HEADERS: %v", name, err)
		}
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
	err := Main([]string{"register", "--url", ts.URL, "--token", "cfreg", "--config", path, "--header", "x-a: " + secret, "--header", "X-A: " + secret}, "test")
	if err == nil || !strings.HasPrefix(err.Error(), "--header") || strings.Contains(err.Error(), secret) {
		t.Fatalf("--header twice with different case: %v", err)
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
	p, err := startMCPProxy(&Config{URL: ts.URL, Headers: map[string]string{"Cf-Access-Client-Id": "id", "Cf-Access-Client-Secret": secret}}, slog.New(slog.DiscardHandler))
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
	if code, body := get(u + "/"); code != 200 || body != "tools at /mcp/run/tok1/" {
		t.Fatalf("the job's path with a trailing slash, which the server takes too: %d %q", code, body)
	}
	for _, other := range []string{"/api/v1/tasks", "/mcp/run/tok10", "/mcp/run/",
		"/mcp/run/tok1/../../api/v1/tasks", "/mcp/run/tok1/%2e%2e/%2e%2e/api/v1/tasks", "/mcp/run/tok1//x", "/mcp/run/tok1/./"} {
		if code, _ := get(p.base + other); code != http.StatusNotFound {
			t.Fatalf("%s must not be forwarded: %d", other, code)
		}
	}
	if code, _ := get(p.base + "/api/v1/tasks"); code != http.StatusNotFound {
		t.Fatalf("the rest of CueFlow must not be forwarded: %d", code)
	}
	p.release("/mcp/run/tok1")
	if code, _ := get(u); code != http.StatusNotFound {
		t.Fatalf("a finished job's tool server must not be forwarded: %d", code)
	}
}

// A redirect on the same host (http to https, say) is followed with the headers; one to another host, the
// access proxy's login page, is not: the headers must not travel there.
func TestRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		if r.Header.Get("Cf-Access-Client-Secret") != "" {
			t.Error("the headers went to another host")
		}
	}))
	defer other.Close()
	var hits atomic.Int32
	ts := httptest.NewServer(gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case runnerapi.BasePath + "/old/jobs/claim":
			http.Redirect(w, r, runnerapi.BasePath+"/jobs/claim", http.StatusPermanentRedirect)
		case runnerapi.BasePath + "/jobs/claim":
			w.WriteHeader(http.StatusNoContent)
		case runnerapi.BasePath + "/away/jobs/claim":
			http.Redirect(w, r, other.URL+"/login", http.StatusFound)
		}
	})))
	defer ts.Close()
	cfg := &Config{URL: ts.URL, Token: "t", Headers: map[string]string{"Cf-Access-Client-Id": "id", "Cf-Access-Client-Secret": secret}}
	if err := cfg.post(context.Background(), "/old/jobs/claim", runnerapi.ClaimRequest{}, nil, true); !errors.Is(err, errNoContent) || hits.Load() != 2 {
		t.Fatalf("same-host 308: %v after %d requests", err, hits.Load())
	}
	err := cfg.post(context.Background(), "/away/jobs/claim", runnerapi.ClaimRequest{}, nil, true)
	var fatal *fatalError
	if !errors.As(err, &fatal) || !strings.Contains(err.Error(), other.URL+"/login") || elsewhere.Load() != 0 {
		t.Fatalf("cross-host 302: %v (other host asked %d times)", err, elsewhere.Load())
	}
}

// A runner whose access proxy credentials stop working stops, as for a revoked token, instead of
// claiming in vain forever.
func TestLoopStopsWhenTheAccessProxyRefuses(t *testing.T) {
	ts := httptest.NewServer(gate(http.NotFoundHandler()))
	defer ts.Close()
	every := 1
	cfg := &Config{URL: ts.URL, Token: "t", Concurrency: 1, PollInterval: &every,
		Headers: map[string]string{"Cf-Access-Client-Id": "id", "Cf-Access-Client-Secret": "expired"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := New(cfg, slog.New(slog.DiscardHandler)).Loop(ctx)
	var fatal *fatalError
	if !errors.As(err, &fatal) || ctx.Err() != nil {
		t.Fatalf("Loop = %v, want a fatal error before the deadline", err)
	}
}

func TestAgentEnvBypassesProxiesForLoopback(t *testing.T) {
	t.Setenv("NO_PROXY", "corp.internal")
	t.Setenv("no_proxy", "")
	env := agentEnv(map[string]string{"A": "1"}, true)
	if env["A"] != "1" || env["NO_PROXY"] != "corp.internal,127.0.0.1" || env["no_proxy"] != "127.0.0.1" {
		t.Fatalf("proxied: %v", env)
	}
	if env := agentEnv(map[string]string{"no_proxy": "x, 127.0.0.1"}, true); env["no_proxy"] != "x, 127.0.0.1" {
		t.Fatalf("already there: %v", env)
	}
	if env := agentEnv(map[string]string{"A": "1"}, false); len(env) != 1 {
		t.Fatalf("without the proxy nothing is added: %v", env)
	}
}

// https to http on the same host would resend the headers in cleartext: not followed. http to https is.
func TestRedirectPolicy(t *testing.T) {
	req := func(u string) *http.Request {
		r, _ := http.NewRequest(http.MethodPost, u, nil)
		return r
	}
	for _, c := range []struct {
		from, to string
		follow   bool
	}{
		{"https://cf.example.com/a", "https://cf.example.com/b", true},
		{"http://cf.example.com/a", "https://cf.example.com/a", true},
		{"https://cf.example.com/a", "http://cf.example.com/a", false},
		{"https://cf.example.com/a", "https://team.cloudflareaccess.com/login", false},
	} {
		err := followRedirect(req(c.to), []*http.Request{req(c.from)})
		if (err == nil) != c.follow {
			t.Errorf("%s -> %s: %v, want follow=%v", c.from, c.to, err, c.follow)
		}
	}
	// A downgrade later in the chain counts too.
	if followRedirect(req("http://cf.example.com/c"), []*http.Request{req("http://cf.example.com/a"), req("https://cf.example.com/b")}) == nil {
		t.Error("https to http after an upgrade was followed")
	}
}
