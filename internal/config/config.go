// Package config loads CueFlow runtime settings from environment variables.
package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	// Addr is the listen address. Defaults to 127.0.0.1:3000 so nothing is exposed publicly
	// unless the operator opts in.
	Addr        string
	DatabaseURL string
	// SecretKey is the 32-byte AES-256-GCM key used to encrypt connector secrets.
	SecretKey  []byte
	AdminToken string
	// PublicURL is used to build links in notification emails.
	PublicURL string
	// DigestCron, when set, sends a "pending review" summary via every active notifier.
	DigestCron       string
	DispatchEvery    time.Duration
	DispatchLease    time.Duration
	PublishTimeout   time.Duration
	LogLevel         string
	DisableScheduler bool
}

func Load() (*Config, error) {
	c := &Config{
		Addr:             env("CUEFLOW_ADDR", "127.0.0.1:3000"),
		DatabaseURL:      env("DATABASE_URL", "postgres://cueflow:cueflow@localhost:5432/cueflow?sslmode=disable"),
		AdminToken:       os.Getenv("CUEFLOW_ADMIN_TOKEN"),
		PublicURL:        env("CUEFLOW_PUBLIC_URL", "http://localhost:3000"),
		DigestCron:       os.Getenv("CUEFLOW_DIGEST_CRON"),
		LogLevel:         env("CUEFLOW_LOG_LEVEL", "info"),
		DispatchEvery:    envDuration("CUEFLOW_DISPATCH_INTERVAL", 3*time.Second),
		DispatchLease:    envDuration("CUEFLOW_DISPATCH_LEASE", 5*time.Minute),
		PublishTimeout:   envDuration("CUEFLOW_PUBLISH_TIMEOUT", 2*time.Minute),
		DisableScheduler: envBool("CUEFLOW_DISABLE_SCHEDULER"),
	}
	key, err := ParseSecretKey(os.Getenv("CUEFLOW_SECRET_KEY"))
	if err != nil {
		return nil, err
	}
	c.SecretKey = key
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
