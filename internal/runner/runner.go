// Package runner is the machine side of CueFlow Runners: `cueflow runner register|run|status`.
// A runner only makes outbound HTTP calls: it long-polls the server for jobs, runs the agent CLI
// locally (with this machine's credentials, under this machine's policy) and reports back.
package runner

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/j75689/cueflow/internal/acpdrive"
	"github.com/j75689/cueflow/internal/connector"
	_ "github.com/j75689/cueflow/internal/connectors/acpagent"
	"github.com/j75689/cueflow/internal/headerlines"
	"github.com/j75689/cueflow/internal/runnerapi"
)

// Config is what `register` saves and `run` reads (0600: it holds the runner token).
type Config struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	RunnerID string `json:"runner_id"`
	Name     string `json:"name"`
	// Concurrency is how many jobs this machine runs at once. The runner reports it with every claim.
	Concurrency int `json:"concurrency"`
	// Version is the cueflow build running this runner, reported with every claim; it is not saved.
	Version string `json:"-"`
	// AllowPresets limits which agent CLIs this machine will launch; empty allows every preset.
	// The server cannot widen this.
	AllowPresets []string `json:"allow_presets,omitempty"`
	// Commands overrides the launch command per preset (e.g. {"claude-code": "/opt/bin/claude-agent-acp"}).
	Commands map[string]string `json:"commands,omitempty"`
	// AuthMethods names the ACP authenticate method to use per preset, for agents that require one
	// (e.g. {"custom": "oauth-personal"} for Google's Antigravity ACP server).
	AuthMethods map[string]string `json:"auth_methods,omitempty"`
	// Models pins the model per preset; it wins over what the server asks for.
	Models map[string]string `json:"models,omitempty"`
	// Env is added to every agent process's environment (credentials can live here).
	Env map[string]string `json:"env,omitempty"`
	// DenyWeb refuses the agent's web fetch/search tools whatever the server asks for.
	DenyWeb bool `json:"deny_web,omitempty"`
	// AllowShell lets agents run shell commands and edit files on this machine, working in the directory
	// the runner was started in (for coding agents). Off by default, and only this file or
	// CUEFLOW_RUNNER_ALLOW_SHELL can turn it on: the server cannot ask for it. Give it a runner of its
	// own, in a container, with credentials limited to what the agent needs.
	AllowShell bool `json:"allow_shell,omitempty"`
	// PollInterval is how many seconds to wait between claims that found no work (default
	// runnerapi.DefaultPollInterval). Each claim is answered at once, so nothing is held open on the server
	// in between. 0 long-polls instead: work is picked up the moment it appears, but the server is kept
	// busy (and, on Cloud Run, billed) the whole time. CUEFLOW_RUNNER_POLL_INTERVAL overrides it.
	PollInterval *int `json:"poll_interval,omitempty"`
	// Headers are sent on every request to CueFlow, for a server behind an access proxy (e.g. a Cloudflare
	// Access service token: CF-Access-Client-Id and CF-Access-Client-Secret). CUEFLOW_RUNNER_HEADERS adds to
	// them and wins for a name set in both. The values are secrets: never logged or shown, and never given to
	// agents, whose tool server calls go through a loopback proxy that adds them (see mcpProxy).
	Headers map[string]string `json:"headers,omitempty"`
}

// pollEvery is the wait between empty claims; 0 means long polling.
func (c *Config) pollEvery() time.Duration {
	if c.PollInterval == nil {
		return runnerapi.DefaultPollInterval * time.Second
	}
	return time.Duration(max(0, *c.PollInterval)) * time.Second
}

// DefaultConfigPath is cueflow/runner.json under the OS user config dir (macOS: ~/Library/Application
// Support, Linux: ~/.config), or $CUEFLOW_RUNNER_CONFIG.
func DefaultConfigPath() string {
	if p := os.Getenv("CUEFLOW_RUNNER_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "cueflow", "runner.json")
}

func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("this machine is not registered (no %s): run `cueflow runner register` first", path)
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.URL == "" || c.Token == "" {
		return nil, fmt.Errorf("%s: missing url or token; register again", path)
	}
	if c.Concurrency < 1 {
		c.Concurrency = 1
	}
	// For a container, where the setting belongs next to the rest of its definition rather than in a
	// file the registration wrote.
	if v := os.Getenv("CUEFLOW_RUNNER_ALLOW_SHELL"); v != "" {
		allow, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("CUEFLOW_RUNNER_ALLOW_SHELL=%q: want true or false", v)
		}
		c.AllowShell = allow
	}
	if v := os.Getenv("CUEFLOW_RUNNER_POLL_INTERVAL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("CUEFLOW_RUNNER_POLL_INTERVAL=%q: want a number of seconds (0 long-polls)", v)
		}
		c.PollInterval = &n
	}
	file := make([]headerlines.Header, 0, len(c.Headers))
	for name, value := range c.Headers {
		file = append(file, headerlines.Header{Name: name, Value: value})
	}
	if c.Headers, err = checkHeaders(path, file); err != nil {
		return nil, err
	}
	env, err := envHeaders()
	if err != nil {
		return nil, err
	}
	maps.Copy(c.Headers, env) // the environment wins for a name in both
	return &c, nil
}

// envHeaders reads CUEFLOW_RUNNER_HEADERS: one "Name: value" per line, "#" starting a comment line.
func envHeaders() (map[string]string, error) {
	hs, err := headerlines.Parse(os.Getenv("CUEFLOW_RUNNER_HEADERS"))
	if err != nil {
		return nil, fmt.Errorf("CUEFLOW_RUNNER_HEADERS %w", err)
	}
	return checkHeaders("CUEFLOW_RUNNER_HEADERS", hs)
}

// ownedHeaders are set by the runner itself on every request.
var ownedHeaders = []string{"Authorization", "Content-Type", "Host"}

// checkHeaders validates the headers of one source (named by src in errors) and returns them by canonical
// name. A name set twice in one source (names are case-insensitive) is refused: which one won would depend on
// map order. Errors name the header, never its value.
func checkHeaders(src string, hs []headerlines.Header) (map[string]string, error) {
	out := map[string]string{}
	for _, h := range hs {
		if !httpguts.ValidHeaderFieldName(h.Name) {
			return nil, fmt.Errorf("%s: header %q: not a valid header name", src, h.Name)
		}
		canon := http.CanonicalHeaderKey(h.Name)
		if slices.Contains(ownedHeaders, canon) {
			return nil, fmt.Errorf("%s: header %q: the runner sets it itself and it cannot be overridden", src, canon)
		}
		if h.Value == "" || !httpguts.ValidHeaderFieldValue(h.Value) {
			return nil, fmt.Errorf("%s: header %q: the value is empty or contains a control character", src, canon)
		}
		if _, dup := out[canon]; dup {
			return nil, fmt.Errorf("%s: header %q is set more than once (names are case-insensitive)", src, canon)
		}
		out[canon] = h.Value
	}
	return out, nil
}

// headerFlag collects repeated --header "Name: value" flags. A malformed one is reported after parsing, by
// position: an error from Set would be printed by the flag package with the value in it.
type headerFlag struct {
	hs  []headerlines.Header
	n   int
	bad int // 1-based position of the first malformed flag
}

func (f *headerFlag) String() string { return "" }

func (f *headerFlag) Set(s string) error {
	f.n++
	h, err := headerlines.ParseLine(s)
	if err != nil {
		f.bad = cmp.Or(f.bad, f.n)
		return nil
	}
	f.hs = append(f.hs, h)
	return nil
}

func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(path, raw, 0o600)
}

// Main runs a `cueflow runner ...` subcommand. version is the cueflow version.
func Main(args []string, version string) error {
	if len(args) == 0 {
		return errors.New("usage: cueflow runner register|run|status")
	}
	switch args[0] {
	case "register":
		return register(args[1:], version)
	case "run":
		return run(args[1:], version)
	case "status":
		return status(args[1:])
	}
	return fmt.Errorf("unknown runner command %q (use register, run or status)", args[0])
}

func register(args []string, version string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	url := fs.String("url", "", "CueFlow server URL, e.g. https://cueflow.example.com")
	token := fs.String("token", "", "one-time registration token (from the Runners page)")
	name := fs.String("name", "", "a name for this runner (default: hostname)")
	labels := fs.String("labels", "", "comma-separated labels, used when the registration token carries none")
	conc := fs.Int("concurrency", 1, "jobs to run at once")
	presets := fs.String("allow-presets", "", "comma-separated agent presets this machine may run (default: all)")
	denyWeb := fs.Bool("deny-web", false, "never let agents use web fetch/search on this machine")
	allowShell := fs.Bool("allow-shell", false, "let agents run shell commands and edit files here (coding agents); use a dedicated, isolated runner")
	cfgPath := fs.String("config", DefaultConfigPath(), "where to save the runner credentials")
	headers := &headerFlag{}
	fs.Var(headers, "header", `"Name: value" to send on every request to CueFlow, e.g. a Cloudflare Access service token (repeatable; saved to the config)`)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *url == "" || *token == "" {
		return errors.New("--url and --token are required")
	}
	if headers.bad > 0 {
		return fmt.Errorf("--header #%d: %w", headers.bad, headerlines.ErrMalformed)
	}
	saved, err := checkHeaders("--header", headers.hs)
	if err != nil {
		return err
	}
	env, err := envHeaders()
	if err != nil {
		return err
	}
	sent := maps.Clone(saved)
	maps.Copy(sent, env)
	if *name == "" {
		*name, _ = os.Hostname()
	}
	req := runnerapi.RegisterRequest{Token: *token, Name: *name, Version: version, Protocol: runnerapi.ProtocolVersion,
		Platform: runtime.GOOS + "/" + runtime.GOARCH, Labels: splitList(*labels), Concurrency: *conc}
	var resp runnerapi.RegisterResponse
	c := &Config{URL: strings.TrimRight(*url, "/"), Headers: sent}
	if err := c.post(context.Background(), "/register", req, &resp, false); err != nil {
		return err
	}
	c.Headers = saved // CUEFLOW_RUNNER_HEADERS stays in the environment, where it was set
	c.Token, c.RunnerID, c.Name, c.Concurrency = resp.Token, resp.RunnerID, resp.Name, max(*conc, 1)
	c.AllowPresets, c.DenyWeb, c.AllowShell = splitList(*presets), *denyWeb, *allowShell
	if err := c.Save(*cfgPath); err != nil {
		return err
	}
	fmt.Printf("Registered runner %q (labels: %s). Credentials saved to %s.\nStart it with: cueflow runner run\n",
		resp.Name, strings.Join(resp.Labels, ", "), *cfgPath)
	return nil
}

func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	cfgPath := fs.String("config", DefaultConfigPath(), "runner credentials file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	fmt.Printf("runner %q (%s)\nserver  %s\nconcurrency %d\n", c.Name, c.RunnerID, c.URL, c.Concurrency)
	if len(c.AllowPresets) > 0 {
		fmt.Printf("allowed presets: %s\n", strings.Join(c.AllowPresets, ", "))
	}
	if c.AllowShell {
		fmt.Println("shell: allowed (agents may run commands and edit files here)")
	}
	if every := c.pollEvery(); every > 0 {
		fmt.Printf("polling every %s\n", every)
	} else {
		fmt.Println("long polling (the server is kept busy while it waits)")
	}
	if len(c.Headers) > 0 {
		fmt.Printf("headers: %s (values hidden)\n", strings.Join(slices.Sorted(maps.Keys(c.Headers)), ", "))
	}
	req, err := http.NewRequest(http.MethodGet, c.URL+"/api/v1/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req, 5*time.Second)
	if err != nil {
		fmt.Printf("server  unreachable: %v\n", err)
		return nil
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "" {
		fmt.Printf("server  HTTP %d, redirects to %s (a login page? see headers in runner.json)\n", resp.StatusCode, loc)
		return nil
	}
	fmt.Printf("server  reachable (HTTP %d)\n", resp.StatusCode)
	return nil
}

func run(args []string, version string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfgPath := fs.String("config", DefaultConfigPath(), "runner credentials file")
	conc := fs.Int("concurrency", 0, "override the configured concurrency")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if *conc > 0 {
		c.Concurrency = *conc
	}
	c.Version = version
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("runner started", "name", c.Name, "server", c.URL, "concurrency", c.Concurrency)
	return New(c, logger).Loop(ctx)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// --- HTTP ---

// fatalError means retrying is pointless: the token was revoked or the protocol is too old.
type fatalError struct{ msg string }

func (e *fatalError) Error() string { return e.msg }

var errNoContent = errors.New("no content")

// do sends req to CueFlow with the configured headers. A redirect is followed only on the same host and never
// from https to http (http to https is fine): any other is returned as is, so the headers never reach it. That is how an access proxy
// answers a request without valid credentials, with its login page, and the caller reports it.
func (c *Config) do(req *http.Request, timeout time.Duration) (*http.Response, error) {
	for name, value := range c.Headers {
		req.Header.Set(name, value)
	}
	hc := &http.Client{Timeout: timeout, CheckRedirect: followRedirect}
	return hc.Do(req)
}

// followRedirect is do's redirect policy.
func followRedirect(req *http.Request, via []*http.Request) error {
	from := via[len(via)-1].URL
	if req.URL.Host != via[0].URL.Host || (from.Scheme == "https" && req.URL.Scheme != "https") {
		return http.ErrUseLastResponse // another host, or https to cleartext
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// post sends a JSON request. A 204 leaves out untouched and returns errNoContent.
func (c *Config) post(ctx context.Context, path string, in, out any, auth bool) error {
	var body io.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+runnerapi.BasePath+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.do(req, (runnerapi.ClaimWait+15)*time.Second)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return errNoContent
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &e)
	msg := e.Error.Message
	if loc := resp.Header.Get("Location"); msg == "" && loc != "" {
		msg = "redirected to " + loc + " (a login page? set the access proxy's headers: see `cueflow runner register --header`)"
	}
	if msg == "" {
		msg = strings.TrimSpace(string(data))
	}
	err = fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, msg)
	// A redirect left unfollowed went to another host: an access proxy refusing the runner's headers, which
	// like a refused token will not get better by retrying.
	refused := resp.StatusCode == http.StatusUnauthorized || resp.Header.Get("Location") != ""
	if (refused && auth) || resp.StatusCode == http.StatusUpgradeRequired {
		return &fatalError{err.Error()}
	}
	return err
}

// --- the loop ---

type Runner struct {
	cfg   *Config
	log   *slog.Logger
	proxy *mcpProxy // set while Loop runs, when cfg has headers
}

// New returns a runner for cfg (used by `cueflow runner run` and tests).
func New(cfg *Config, log *slog.Logger) *Runner { return &Runner{cfg: cfg, log: log} }

// Loop claims and runs jobs until ctx ends, then waits for running jobs to wind down.
func (r *Runner) Loop(ctx context.Context) error {
	if len(r.cfg.Headers) > 0 {
		p, err := startMCPProxy(r.cfg, r.log)
		if err != nil {
			return err
		}
		defer p.close() // after the jobs below wound down
		r.proxy = p
	}
	slots := make(chan struct{}, r.cfg.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	backoff := time.Second
	every := r.cfg.pollEvery()
	claim := runnerapi.ClaimRequest{Protocol: runnerapi.ProtocolVersion, Version: r.cfg.Version, Concurrency: r.cfg.Concurrency}
	if every > 0 {
		now := 0 // answer at once; this runner asks again after `every`
		claim.Wait = &now
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case slots <- struct{}{}:
		}
		var job runnerapi.Job
		err := r.cfg.post(ctx, "/jobs/claim", claim, &job, true)
		if err != nil {
			<-slots
			if errors.Is(err, errNoContent) {
				backoff = time.Second
				if every > 0 {
					select {
					case <-ctx.Done():
						return nil
					case <-time.After(every):
					}
				}
				continue
			}
			var fatal *fatalError
			if errors.As(err, &fatal) {
				return fatal
			}
			if ctx.Err() != nil {
				return nil
			}
			r.log.Warn("claim failed", "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			r.runJob(ctx, job)
		}()
	}
}

// sink batches the agent's activity and ships it to the server.
type sink struct {
	mu  sync.Mutex
	buf []runnerapi.Event
}

func (s *sink) Say(_ context.Context, kind, content string, data any) {
	s.mu.Lock()
	s.buf = append(s.buf, runnerapi.Event{Kind: kind, Content: content, Data: data})
	s.mu.Unlock()
}

func (s *sink) System(_ context.Context, text string) {
	s.Say(context.Background(), "system", text, nil)
}

func (s *sink) drain() []runnerapi.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.buf
	s.buf = nil
	return out
}

func (r *Runner) runJob(ctx context.Context, job runnerapi.Job) {
	log := r.log.With("job", job.ID, "task", job.Title)
	log.Info("job claimed", "preset", job.Preset)
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sk := &sink{}
	done := make(chan struct{})
	go r.report(jobCtx, cancel, job.ID, sk, done)

	res := r.execute(jobCtx, job, sk)
	close(done)
	// Final flush and completion use their own context: ctx may already be canceled by Ctrl-C.
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer fcancel()
	r.sendEvents(fctx, job.ID, sk.drain())

	req := runnerapi.CompleteRequest{
		StopReason: string(res.StopReason), FinalText: res.FinalText, AgentName: res.AgentName,
		ToolCalls: res.ToolCalls, TimedOut: res.TimedOut, Canceled: res.Canceled,
	}
	if res.Usage != nil {
		req.InputTokens, req.OutputTokens = res.Usage.InputTokens, res.Usage.OutputTokens
	}
	if res.Err != nil {
		req.Error = res.Err.Error()
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := r.cfg.post(fctx, "/jobs/"+job.ID+"/complete", req, nil, true)
		if err == nil {
			break
		}
		if attempt == 2 {
			log.Error("could not report completion", "err", err)
		} else {
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
		}
	}
	log.Info("job finished", "stop", req.StopReason, "error", req.Error)
}

func (r *Runner) execute(ctx context.Context, job runnerapi.Job, sk *sink) acpdrive.Result {
	fail := func(format string, a ...any) acpdrive.Result {
		return acpdrive.Result{Err: fmt.Errorf(format, a...)}
	}
	if len(r.cfg.AllowPresets) > 0 && !slices.Contains(r.cfg.AllowPresets, job.Preset) {
		return fail("runner %q does not allow the %q agent (allowed: %s)", r.cfg.Name, job.Preset, strings.Join(r.cfg.AllowPresets, ", "))
	}
	cc := connector.Config{"preset": job.Preset}
	if cmd := r.cfg.Commands[job.Preset]; cmd != "" {
		cc["command"] = cmd
	}
	if m := r.cfg.AuthMethods[job.Preset]; m != "" {
		cc["auth_method"] = m
	}
	if env := agentEnv(r.cfg.Env, r.proxy != nil); len(env) > 0 {
		cc["env"] = env
	}
	inst, err := connector.Build("acp_agent", cc, connector.Env{})
	if err != nil {
		return fail("%v", err)
	}
	rt, ok := inst.(connector.Runtime)
	if !ok {
		return fail("agent %q is not available on this runner", job.Preset)
	}
	launch := rt.Launch()
	launch.Timeout = time.Duration(job.TimeoutSeconds) * time.Second
	allowWeb, auth := job.AllowWeb && !r.cfg.DenyWeb, ""
	if o, ok := inst.(interface{ AuthMethod() string }); ok {
		auth = o.AuthMethod()
	}
	hint := ""
	if h, ok := inst.(interface{ CredentialHint() string }); ok {
		hint = h.CredentialHint()
		if hint != "" {
			hint = "set the agent's credentials in this runner's environment (e.g. CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY, OPENAI_API_KEY)"
		}
	}
	dir := "" // a fresh temporary directory, unless the agent may work here
	if r.cfg.AllowShell {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}
	mcpURL := r.cfg.URL + job.MCPPath
	if r.proxy != nil {
		mcpURL = r.proxy.serve(job.MCPPath)
		defer r.proxy.release(job.MCPPath)
	}
	return acpdrive.Run(ctx, acpdrive.Params{
		Launch: launch, AuthMethod: auth, AllowWeb: allowWeb, AllowHost: r.cfg.AllowShell, Dir: dir,
		MCPURL: mcpURL, Prompt: job.Prompt, Sink: sk, CredentialHint: hint,
		Model: cmp.Or(r.cfg.Models[job.Preset], job.Model),
	})
}

// agentEnv is what is added to an agent's environment: the runner's env and, when the agent reaches its tool
// server through the loopback proxy, 127.0.0.1 in NO_PROXY, so an HTTP proxy set for the machine's egress
// (HTTP_PROXY, which some agents' clients apply even to loopback) is not asked for it.
func agentEnv(cfgEnv map[string]string, proxied bool) map[string]any {
	env := map[string]any{}
	for k, v := range cfgEnv {
		env[k] = v
	}
	if proxied {
		get := func(k string) (string, bool) {
			if v, ok := cfgEnv[k]; ok {
				return v, true
			}
			return os.LookupEnv(k)
		}
		upper, hasUpper := get("NO_PROXY")
		lower, hasLower := get("no_proxy")
		// Tools read one or the other: one set alone stands for both.
		if hasUpper && !hasLower {
			lower = upper
		} else if hasLower && !hasUpper {
			upper = lower
		}
		for k, v := range map[string]string{"NO_PROXY": upper, "no_proxy": lower} {
			if !slices.ContainsFunc(strings.Split(v, ","), func(e string) bool { return strings.TrimSpace(e) == "127.0.0.1" }) {
				env[k] = strings.TrimPrefix(v+",127.0.0.1", ",")
			}
		}
	}
	return env
}

// report ships buffered events every few seconds (which also keeps the job's lease alive) and
// cancels the agent when the server says the job was canceled.
func (r *Runner) report(ctx context.Context, cancel context.CancelFunc, id string, sk *sink, done <-chan struct{}) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	lastBeat := time.Now()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-t.C:
		}
		events := sk.drain()
		if len(events) == 0 && time.Since(lastBeat) < 30*time.Second {
			continue
		}
		lastBeat = time.Now()
		if r.sendEvents(ctx, id, events) {
			r.log.Info("job canceled by the server", "job", id)
			cancel()
			return
		}
	}
}

// sendEvents posts events (an empty list is a heartbeat) and reports whether the server wants the
// job stopped. Events that could not be sent are not retried: the thread is a log, not a ledger.
func (r *Runner) sendEvents(ctx context.Context, id string, events []runnerapi.Event) (cancel bool) {
	var st runnerapi.JobStatus
	if err := r.cfg.post(ctx, "/jobs/"+id+"/events", runnerapi.EventsRequest{Events: events}, &st, true); err != nil {
		if ctx.Err() == nil {
			r.log.Warn("report failed", "job", id, "err", err)
		}
		return false
	}
	return st.Cancel
}

// mcpProxy forwards an agent's calls to its job's tool server, adding the runner's headers. The agent is
// given a loopback URL, so it can reach a server behind an access proxy without ever seeing the headers.
// Only the tool servers of running jobs are forwarded: the proxy is no way into the rest of CueFlow.
type mcpProxy struct {
	base string // http://127.0.0.1:port
	srv  *http.Server
	mu   sync.Mutex
	jobs map[string]int // MCP path -> running jobs using it
}

func startMCPProxy(c *Config, log *slog.Logger) (*mcpProxy, error) {
	target, err := url.Parse(c.URL)
	if err != nil {
		return nil, fmt.Errorf("server URL: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("tool server proxy: %w", err)
	}
	p := &mcpProxy{base: "http://" + ln.Addr().String(), jobs: map[string]int{}}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			for name, value := range c.Headers {
				pr.Out.Header.Set(name, value)
			}
		},
		// An access proxy answers a request with valid service-token headers with a session cookie too
		// (Cloudflare Access: CF_Authorization). Passed on, it would let the agent through on its own.
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Set-Cookie")
			return nil
		},
		FlushInterval: -1, // stream server-sent events as they come
		ErrorLog:      slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	p.srv = &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !p.serving(req.URL.Path) {
			http.NotFound(w, req)
			return
		}
		rp.ServeHTTP(w, req)
	})}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// serve starts forwarding path and returns the URL the agent uses for it.
func (p *mcpProxy) serve(path string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs[path]++
	return p.base + path
}

func (p *mcpProxy) release(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.jobs[path]--; p.jobs[path] <= 0 {
		delete(p.jobs, path)
	}
}

// serving reports whether p is a running job's tool server path, as is or with one trailing slash (the
// server takes both). Nothing else matches: no prefix, no dot segments, no backslashes, which an access proxy
// in front may turn into "/.." and so into the rest of CueFlow. A run token never contains "/".
func (p *mcpProxy) serving(reqPath string) bool {
	jp := strings.TrimSuffix(reqPath, "/")
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jobs[jp] > 0
}

func (p *mcpProxy) close() { _ = p.srv.Close() }
