// Package config loads CueFlow runtime settings from environment variables.
package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Addr is the listen address. Defaults to 127.0.0.1:3000 so nothing is exposed publicly
	// unless the operator opts in.
	Addr        string
	DatabaseURL string
	// DatabaseSessionURL (DATABASE_SESSION_URL) is a session-mode URL for the work that needs a
	// session: LISTEN and the migration lock. Setting it means DatabaseURL goes through a
	// transaction-mode pooler (e.g. Supabase's on port 6543), and the pool is opened for one.
	// Empty: everything uses DatabaseURL, which must then be a direct or session-mode connection.
	DatabaseSessionURL string
	// SecretKey is the 32-byte AES-256-GCM key used to encrypt connector secrets.
	SecretKey []byte
	// PreviousSecretKeys still decrypt during a key rotation (CUEFLOW_SECRET_KEY_PREVIOUS,
	// comma-separated); new data is always encrypted with SecretKey.
	PreviousSecretKeys [][]byte
	AdminToken         string
	MCPToken           string
	// PublicURL is used to build links in notification emails.
	PublicURL string
	// MediaBaseURL prefixes stored images ("/media/{id}") when handing them to publishers; it must
	// be reachable from the publishing service (e.g. http://cueflow:3000 for a publisher in the same compose file).
	MediaBaseURL string
	// MediaStorage is where new media and task-file bytes go: empty (the database), file:///data/media
	// or gcs://bucket/prefix.
	MediaStorage     string
	DispatchEvery    time.Duration
	DispatchLease    time.Duration
	PublishTimeout   time.Duration
	LogLevel         string
	DisableScheduler bool
	// AgentConcurrency is how many agent tasks may run at once.
	AgentConcurrency int
	// ContextHandOffAt is how full a run's context (prompt over the model's window) may get before the
	// run hands off to a fresh one; the task page colours the size from here. Between 0 and 1.
	ContextHandOffAt float64
	// Warnings are settings that were invalid and replaced by their default, for main to log.
	Warnings []string
	// Modules are the feature modules to mount (CUEFLOW_MODULES, comma-separated). Default: all of
	// them. A module that is not mounted has no routes, tools, workers or cron; its tables stay.
	Modules []string
	// AgentAllowPrivateURLs lets agents' read_url reach private/LAN addresses (off by default).
	AgentAllowPrivateURLs bool

	// Queue is how background work is requested: "local" (in-process loops, the default) or
	// "cloudtasks" (each request becomes an HTTP delivery to /internal/work/{kind}/{id}).
	Queue string
	// Events is how live dashboard updates reach every instance (CUEFLOW_EVENTS): "postgres"
	// (LISTEN/NOTIFY, the default) or "pubsub" (Google Pub/Sub on PubSubTopic, which takes no
	// database connection; see events/pubsub).
	Events string
	// PubSubTopic is "projects/P/topics/T" for CUEFLOW_EVENTS=pubsub (CUEFLOW_PUBSUB_TOPIC).
	PubSubTopic string
	// PubSubDeleteTimeout bounds deleting this instance's subscription on shutdown
	// (CUEFLOW_PUBSUB_DELETE_TIMEOUT, default 8 s).
	PubSubDeleteTimeout time.Duration
	// MCPRequestTimeout bounds one request to the stateless MCP endpoints, /mcp and /mcp/run
	// (CUEFLOW_MCP_REQUEST_TIMEOUT, default 10 min).
	MCPRequestTimeout time.Duration
	// Tick is who runs the clock: "internal" (in-process cron and loops) or "external" (Cloud
	// Scheduler calls /internal/tick every minute; no in-process loops start).
	Tick string
	// TasksQueue is the Cloud Tasks queue, "projects/P/locations/L/queues/Q".
	TasksQueue string
	// TasksTargetURL is this service's own URL as Cloud Tasks reaches it (default PublicURL).
	TasksTargetURL string
	// TasksServiceAccount signs the OIDC token of each Cloud Tasks delivery.
	TasksServiceAccount string
	// InternalAudience is the audience /internal/* requires in the Google ID token (default
	// TasksTargetURL).
	InternalAudience string
	// InternalServiceAccounts are the service account emails allowed to call /internal/*
	// (Cloud Tasks and Cloud Scheduler identities).
	InternalServiceAccounts []string
	// CFAccessTeamDomain and CFAccessAudience turn on Cloudflare Access login: requests carrying a
	// valid Access JWT for that application are accepted like requests with the admin token.
	CFAccessTeamDomain string
	CFAccessAudience   string
	// DirectURL is how machines reach this server (runners, MCP clients, publishers fetching media):
	// they cannot log in through a browser-oriented gate such as Cloudflare Access. Default PublicURL.
	DirectURL string
	// MediaRetentionDays, when positive, deletes media older than that many days which no unfinished
	// post uses (checked by the external clock; locally use `cueflow media prune`).
	MediaRetentionDays int
	// MediaRegisterAccounts may call /internal/media/register (the service accounts of media producers
	// such as a video renderer that writes to the bucket itself).
	MediaRegisterAccounts []string
	// DBMaxConns caps this process's connection pool (0 = driver default). Set it low (e.g. 5) in
	// the cloud so several instances stay under a small database's connection limit.
	DBMaxConns int
}

// Cloud reports whether work is delivered by an external queue and clock.
func (c *Config) Cloud() bool { return c.Queue == "cloudtasks" || c.Tick == "external" }

func Load() (*Config, error) {
	c := &Config{
		Addr:                  env("CUEFLOW_ADDR", "127.0.0.1:3000"),
		DatabaseURL:           env("DATABASE_URL", "postgres://cueflow:cueflow@localhost:5432/cueflow?sslmode=disable"),
		DatabaseSessionURL:    os.Getenv("DATABASE_SESSION_URL"),
		AdminToken:            os.Getenv("CUEFLOW_ADMIN_TOKEN"),
		MCPToken:              os.Getenv("CUEFLOW_MCP_TOKEN"),
		PublicURL:             env("CUEFLOW_PUBLIC_URL", "http://localhost:3000"),
		LogLevel:              env("CUEFLOW_LOG_LEVEL", "info"),
		DisableScheduler:      envBool("CUEFLOW_DISABLE_SCHEDULER"),
		AgentConcurrency:      envInt("CUEFLOW_AGENT_CONCURRENCY", 3),
		AgentAllowPrivateURLs: envBool("CUEFLOW_AGENT_ALLOW_PRIVATE_URLS"),
	}
	warn := func(msg string) { c.Warnings = append(c.Warnings, msg) }
	c.ContextHandOffAt = envFraction("CUEFLOW_CONTEXT_HANDOFF", DefaultContextHandOffAt, warn)
	c.DispatchEvery = envDurationOrWarn("CUEFLOW_DISPATCH_INTERVAL", 3*time.Second, warn)
	c.DispatchLease = envDurationOrWarn("CUEFLOW_DISPATCH_LEASE", 10*time.Minute, warn)
	c.PublishTimeout = envDurationOrWarn("CUEFLOW_PUBLISH_TIMEOUT", 5*time.Minute, warn)
	var err error
	if c.Modules, err = parseModules(os.Getenv("CUEFLOW_MODULES")); err != nil {
		return nil, err
	}
	if os.Getenv("CUEFLOW_ADDR") == "" && os.Getenv("PORT") != "" {
		c.Addr = "0.0.0.0:" + os.Getenv("PORT") // Cloud Run tells the container its port this way
	}
	c.DirectURL = strings.TrimRight(env("CUEFLOW_DIRECT_URL", c.PublicURL), "/")
	c.MediaBaseURL = env("CUEFLOW_MEDIA_BASE_URL", c.DirectURL)
	c.CFAccessTeamDomain = strings.TrimSpace(os.Getenv("CUEFLOW_CF_ACCESS_TEAM_DOMAIN"))
	c.CFAccessAudience = strings.TrimSpace(os.Getenv("CUEFLOW_CF_ACCESS_AUD"))
	if (c.CFAccessTeamDomain == "") != (c.CFAccessAudience == "") {
		return nil, errors.New("Cloudflare Access login needs both CUEFLOW_CF_ACCESS_TEAM_DOMAIN and CUEFLOW_CF_ACCESS_AUD")
	}
	c.MediaStorage = env("CUEFLOW_MEDIA_STORAGE", "")
	c.Queue = env("CUEFLOW_QUEUE", "local")
	c.Events = env("CUEFLOW_EVENTS", "postgres")
	c.PubSubTopic = os.Getenv("CUEFLOW_PUBSUB_TOPIC")
	if c.PubSubDeleteTimeout, err = envDuration("CUEFLOW_PUBSUB_DELETE_TIMEOUT", DefaultPubSubDeleteTimeout); err != nil {
		return nil, err
	}
	if c.PubSubDeleteTimeout <= 0 {
		return nil, fmt.Errorf("CUEFLOW_PUBSUB_DELETE_TIMEOUT must be > 0, got %s", c.PubSubDeleteTimeout)
	}
	if c.MCPRequestTimeout, err = envDuration("CUEFLOW_MCP_REQUEST_TIMEOUT", DefaultMCPRequestTimeout); err != nil {
		return nil, err
	}
	if c.MCPRequestTimeout < MinMCPRequestTimeout {
		return nil, fmt.Errorf("CUEFLOW_MCP_REQUEST_TIMEOUT must be at least %s, got %s", MinMCPRequestTimeout, c.MCPRequestTimeout)
	}
	if c.MCPRequestTimeout >= 30*time.Minute {
		warn(fmt.Sprintf("CUEFLOW_MCP_REQUEST_TIMEOUT=%s is not under Cloud Run's 30 min request timeout, which then cuts the request first", c.MCPRequestTimeout))
	}
	if c.PubSubDeleteTimeout >= 10*time.Second {
		warn(fmt.Sprintf("CUEFLOW_PUBSUB_DELETE_TIMEOUT=%s is not under Cloud Run's 10 s shutdown grace: the instance may be killed before the delete returns", c.PubSubDeleteTimeout))
	}
	c.Tick = env("CUEFLOW_TICK", "internal")
	c.TasksQueue = os.Getenv("CUEFLOW_TASKS_QUEUE")
	c.TasksTargetURL = strings.TrimRight(env("CUEFLOW_TASKS_TARGET_URL", c.PublicURL), "/")
	c.TasksServiceAccount = os.Getenv("CUEFLOW_TASKS_SERVICE_ACCOUNT")
	c.InternalAudience = env("CUEFLOW_INTERNAL_AUDIENCE", c.TasksTargetURL)
	for _, e := range strings.Split(os.Getenv("CUEFLOW_INTERNAL_SERVICE_ACCOUNTS"), ",") {
		if e = strings.TrimSpace(e); e != "" {
			c.InternalServiceAccounts = append(c.InternalServiceAccounts, e)
		}
	}
	c.DBMaxConns = envInt("CUEFLOW_DB_MAX_CONNS", 0)
	c.MediaRetentionDays = envInt("CUEFLOW_MEDIA_RETENTION_DAYS", 0)
	for _, e := range strings.Split(os.Getenv("CUEFLOW_MEDIA_REGISTER_ACCOUNTS"), ",") {
		if e = strings.TrimSpace(e); e != "" {
			c.MediaRegisterAccounts = append(c.MediaRegisterAccounts, e)
		}
	}
	if len(c.MediaRegisterAccounts) > 0 && c.Tick != "external" {
		return nil, errors.New("CUEFLOW_MEDIA_REGISTER_ACCOUNTS only applies in cloud mode (CUEFLOW_TICK=external)")
	}
	if c.Queue != "local" && c.Queue != "cloudtasks" {
		return nil, fmt.Errorf("CUEFLOW_QUEUE must be local or cloudtasks, got %q", c.Queue)
	}
	if c.Events != "postgres" && c.Events != "pubsub" {
		return nil, fmt.Errorf("CUEFLOW_EVENTS must be postgres or pubsub, got %q", c.Events)
	}
	if c.Events == "pubsub" && c.PubSubTopic == "" {
		return nil, errors.New("CUEFLOW_EVENTS=pubsub needs CUEFLOW_PUBSUB_TOPIC (projects/PROJECT/topics/TOPIC)")
	}
	if c.Tick != "internal" && c.Tick != "external" {
		return nil, fmt.Errorf("CUEFLOW_TICK must be internal or external, got %q", c.Tick)
	}
	if c.Queue == "cloudtasks" && (c.TasksQueue == "" || c.TasksServiceAccount == "") {
		return nil, errors.New("CUEFLOW_QUEUE=cloudtasks needs CUEFLOW_TASKS_QUEUE and CUEFLOW_TASKS_SERVICE_ACCOUNT")
	}
	if c.Queue == "cloudtasks" && c.Tick != "external" {
		return nil, errors.New("CUEFLOW_QUEUE=cloudtasks needs CUEFLOW_TICK=external (the /internal routes that receive the work are only served then)")
	}
	if c.Tick == "external" && c.Queue != "cloudtasks" {
		return nil, errors.New("CUEFLOW_TICK=external needs CUEFLOW_QUEUE=cloudtasks (work is delivered by the queue)")
	}
	if c.Cloud() && len(c.InternalServiceAccounts) == 0 {
		return nil, errors.New("cloud mode needs CUEFLOW_INTERNAL_SERVICE_ACCOUNTS (who may call /internal/*)")
	}
	key, err := ParseSecretKey(os.Getenv("CUEFLOW_SECRET_KEY"))
	if err != nil {
		return nil, err
	}
	c.SecretKey = key
	for _, raw := range strings.Split(os.Getenv("CUEFLOW_SECRET_KEY_PREVIOUS"), ",") {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		k, err := ParseSecretKey(raw)
		if err != nil {
			return nil, fmt.Errorf("CUEFLOW_SECRET_KEY_PREVIOUS: %w", err)
		}
		c.PreviousSecretKeys = append(c.PreviousSecretKeys, k)
	}
	return c, nil
}

// ParseSecretKey accepts a 32-byte key encoded as base64 (std or url) or hex.
func ParseSecretKey(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("CUEFLOW_SECRET_KEY is required (32 bytes, base64 or hex); generate one with `openssl rand -base64 32`")
	}
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		hex.DecodeString,
	} {
		if b, err := dec(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("CUEFLOW_SECRET_KEY must decode to exactly 32 bytes")
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// durationSyntax is what time.ParseDuration accepts. It fails on a value that matches only when the value is
// out of range, which it reports with the same text as a typo.
var durationSyntax = regexp.MustCompile(`^[-+]?(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+$`)

// envDuration reads k as a Go duration (30s, 10m, 1h30m); blank is def. A value that does not parse is an
// error naming k, so "600" or "5min" cannot quietly become the default.
func envDuration(k string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil && durationSyntax.MatchString(v) {
		return 0, fmt.Errorf("%s=%q is out of range: a duration must be between about -2562047h and 2562047h", k, v)
	}
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a duration: give a number with a unit, e.g. 30s, 10m or 1h", k, v)
	}
	return d, nil
}

// envDurationOrWarn is envDuration for the settings that took a bad value silently before it was checked:
// such a value, or one that is not > 0, keeps def and is passed to warn, so a deployment that boots today
// still boots.
func envDurationOrWarn(k string, def time.Duration, warn func(string)) time.Duration {
	d, err := envDuration(k, def)
	if err == nil && d <= 0 {
		err = fmt.Errorf("%s=%q must be > 0", k, strings.TrimSpace(os.Getenv(k)))
	}
	if err != nil {
		warn(fmt.Sprintf("%v; using %s", err, def))
		return def
	}
	return d
}

func envBool(k string) bool {
	b, _ := strconv.ParseBool(os.Getenv(k))
	return b
}

// DefaultPubSubDeleteTimeout is CUEFLOW_PUBSUB_DELETE_TIMEOUT's default. Pub/Sub can take a few
// seconds to answer a delete it has already done, and the delete must fit in Cloud Run's 10 s after SIGTERM.
const DefaultPubSubDeleteTimeout = 8 * time.Second

// DefaultMCPRequestTimeout is CUEFLOW_MCP_REQUEST_TIMEOUT's default: longer than any built-in tool call
// takes, well under Cloud Run's 30-minute request timeout.
const DefaultMCPRequestTimeout = 10 * time.Minute

// MinMCPRequestTimeout is the shortest CUEFLOW_MCP_REQUEST_TIMEOUT: a connector call ends 5 s before its
// request does, and a tool call needs time left after that.
const MinMCPRequestTimeout = 30 * time.Second

// DefaultContextHandOffAt is CUEFLOW_CONTEXT_HANDOFF's default: hand off at 80% of the window.
const DefaultContextHandOffAt = 0.8

// envFraction reads a number strictly between 0 and 1. Anything else falls back to def and is passed
// to warn, so a typo is visible at startup instead of silently changing behaviour.
func envFraction(k string, def float64, warn func(string)) float64 {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || !(f > 0 && f < 1) {
		warn(fmt.Sprintf("%s=%q must be a number between 0 and 1 (e.g. 0.8); using %g", k, v, def))
		return def
	}
	return f
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}

// KnownModules are the modules this binary can mount, in mount order.
var KnownModules = []string{"content", "deliverables"}

// moduleNeeds lists the modules each module cannot work without: content's posts are deliverables,
// so without that module they would have no review page and nothing to publish them.
var moduleNeeds = map[string][]string{"content": {"deliverables"}}

// parseModules reads CUEFLOW_MODULES: blank or "all" means every known module; "none" means none.
func parseModules(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "all" {
		return append([]string{}, KnownModules...), nil
	}
	if raw == "none" {
		return []string{}, nil
	}
	var out []string
	for _, m := range strings.Split(raw, ",") {
		m = strings.ToLower(strings.TrimSpace(m))
		if m == "" {
			continue
		}
		known := false
		for _, k := range KnownModules {
			known = known || k == m
		}
		if !known {
			return nil, fmt.Errorf("CUEFLOW_MODULES: unknown module %q (known: %s)", m, strings.Join(KnownModules, ", "))
		}
		dup := false
		for _, o := range out {
			dup = dup || o == m
		}
		if !dup {
			out = append(out, m)
		}
	}
	for _, m := range out {
		for _, need := range moduleNeeds[m] {
			if !slices.Contains(out, need) {
				return nil, fmt.Errorf("CUEFLOW_MODULES: module %q needs %q; add it to the list", m, need)
			}
		}
	}
	return out, nil
}

// Enabled reports whether a module is mounted.
func (c *Config) Enabled(module string) bool {
	for _, m := range c.Modules {
		if m == module {
			return true
		}
	}
	return false
}
