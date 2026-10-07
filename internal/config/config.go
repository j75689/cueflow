// Package config loads CueFlow runtime settings from environment variables.
package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Addr is the listen address. Defaults to 127.0.0.1:3000 so nothing is exposed publicly
	// unless the operator opts in.
	Addr        string
	DatabaseURL string
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
	// be reachable from the publishing service (e.g. http://cueflow:3000 for Postiz in compose).
	MediaBaseURL string
	// MediaStorage is where new media bytes go: empty (the database) or file:///data/media.
	MediaStorage string
	// DigestCron, when set, sends a "pending review" summary via every active notifier.
	DigestCron       string
	DispatchEvery    time.Duration
	DispatchLease    time.Duration
	PublishTimeout   time.Duration
	LogLevel         string
	DisableScheduler bool
	// AgentConcurrency is how many agent tasks may run at once.
	AgentConcurrency int
	// AgentAllowPrivateURLs lets agents' read_url reach private/LAN addresses (off by default).
	AgentAllowPrivateURLs bool

	// Queue is how background work is requested: "local" (in-process loops, the default) or
	// "cloudtasks" (each request becomes an HTTP delivery to /internal/work/{kind}/{id}).
	Queue string
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
	// MediaRegisterAccounts may call /internal/media/register (the Reels renderer's service account).
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
		AdminToken:            os.Getenv("CUEFLOW_ADMIN_TOKEN"),
		MCPToken:              os.Getenv("CUEFLOW_MCP_TOKEN"),
		PublicURL:             env("CUEFLOW_PUBLIC_URL", "http://localhost:3000"),
		DigestCron:            os.Getenv("CUEFLOW_DIGEST_CRON"),
		LogLevel:              env("CUEFLOW_LOG_LEVEL", "info"),
		DispatchEvery:         envDuration("CUEFLOW_DISPATCH_INTERVAL", 3*time.Second),
		DispatchLease:         envDuration("CUEFLOW_DISPATCH_LEASE", 10*time.Minute),
		PublishTimeout:        envDuration("CUEFLOW_PUBLISH_TIMEOUT", 5*time.Minute),
		DisableScheduler:      envBool("CUEFLOW_DISABLE_SCHEDULER"),
		AgentConcurrency:      envInt("CUEFLOW_AGENT_CONCURRENCY", 3),
		AgentAllowPrivateURLs: envBool("CUEFLOW_AGENT_ALLOW_PRIVATE_URLS"),
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

func envDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envBool(k string) bool {
	b, _ := strconv.ParseBool(os.Getenv(k))
	return b
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}
