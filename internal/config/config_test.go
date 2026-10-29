package config

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// base sets the one required variable (a 32-byte key, hex) and clears everything cloud-related.
func base(t *testing.T) {
	t.Helper()
	t.Setenv("CUEFLOW_SECRET_KEY", strings.Repeat("ab", 32))
	for _, k := range []string{"CUEFLOW_QUEUE", "CUEFLOW_TICK", "CUEFLOW_TASKS_QUEUE", "CUEFLOW_TASKS_TARGET_URL",
		"CUEFLOW_TASKS_SERVICE_ACCOUNT", "CUEFLOW_INTERNAL_AUDIENCE", "CUEFLOW_INTERNAL_SERVICE_ACCOUNTS",
		"CUEFLOW_ADDR", "PORT", "CUEFLOW_DB_MAX_CONNS", "CUEFLOW_DIRECT_URL", "CUEFLOW_MEDIA_BASE_URL",
		"CUEFLOW_PUBLIC_URL", "CUEFLOW_CF_ACCESS_TEAM_DOMAIN", "CUEFLOW_CF_ACCESS_AUD", "CUEFLOW_PUBSUB_DELETE_TIMEOUT", "CUEFLOW_MCP_REQUEST_TIMEOUT",
		"CUEFLOW_DISPATCH_INTERVAL", "CUEFLOW_DISPATCH_LEASE", "CUEFLOW_PUBLISH_TIMEOUT", "CUEFLOW_CONNECTOR_HTTP_TIMEOUT",
		"CUEFLOW_MEDIA_STORAGE", "CUEFLOW_MEDIA_REGISTER_ACCOUNTS"} {
		t.Setenv(k, "")
	}
	for _, k := range SecretVars {
		if k != "CUEFLOW_SECRET_KEY" {
			t.Setenv(k, "")
		}
		t.Setenv(k+"_FILE", "")
	}
}

func TestLocalIsTheDefault(t *testing.T) {
	base(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Queue != "local" || c.Tick != "internal" || c.Cloud() {
		t.Fatalf("defaults must stay local: %+v", c)
	}
}

func TestCloudSettingsAreValidatedTogether(t *testing.T) {
	cloud := map[string]string{
		"CUEFLOW_QUEUE": "cloudtasks", "CUEFLOW_TICK": "external",
		"CUEFLOW_TASKS_QUEUE":               "projects/p/locations/asia-northeast1/queues/q",
		"CUEFLOW_TASKS_SERVICE_ACCOUNT":     "tasks@p.iam.gserviceaccount.com",
		"CUEFLOW_TASKS_TARGET_URL":          "https://cueflow-abc.a.run.app/",
		"CUEFLOW_INTERNAL_SERVICE_ACCOUNTS": "tasks@p.iam.gserviceaccount.com, sched@p.iam.gserviceaccount.com",
	}
	set := func(t *testing.T, drop string) {
		base(t)
		for k, v := range cloud {
			if k != drop {
				t.Setenv(k, v)
			}
		}
	}

	set(t, "")
	c, err := Load()
	if err != nil {
		t.Fatalf("a complete cloud configuration must load: %v", err)
	}
	if !c.Cloud() || c.TasksTargetURL != "https://cueflow-abc.a.run.app" || !slices.Equal(c.InternalAudiences, []string{c.TasksTargetURL, c.DirectURL}) ||
		len(c.InternalServiceAccounts) != 2 {
		t.Fatalf("cloud config: %+v", c)
	}

	for _, drop := range []string{"CUEFLOW_TASKS_QUEUE", "CUEFLOW_TASKS_SERVICE_ACCOUNT", "CUEFLOW_INTERNAL_SERVICE_ACCOUNTS", "CUEFLOW_TICK"} {
		set(t, drop)
		if _, err := Load(); err == nil {
			t.Errorf("cloud mode without %s must be refused", drop)
		}
	}

	base(t)
	t.Setenv("CUEFLOW_TICK", "external") // an outside clock with no queue to deliver the work
	if _, err := Load(); err == nil {
		t.Error("an external clock needs the queue")
	}
	base(t)
	t.Setenv("CUEFLOW_QUEUE", "pubsub")
	if _, err := Load(); err == nil {
		t.Error("an unknown queue must be refused")
	}
}

func TestCloudRunPortSetsTheListenAddress(t *testing.T) {
	base(t)
	t.Setenv("PORT", "8080")
	c, err := Load()
	if err != nil || c.Addr != "0.0.0.0:8080" {
		t.Fatalf("PORT: %v %v", c, err)
	}
	t.Setenv("CUEFLOW_ADDR", "127.0.0.1:9999")
	c, err = Load()
	if err != nil || c.Addr != "127.0.0.1:9999" {
		t.Fatalf("an explicit address wins over PORT: %v %v", c, err)
	}
}

func TestCloudflareAccessNeedsBothSettings(t *testing.T) {
	base(t)
	t.Setenv("CUEFLOW_CF_ACCESS_TEAM_DOMAIN", "acme.cloudflareaccess.com")
	if _, err := Load(); err == nil {
		t.Error("a team domain without an audience must be refused")
	}
	base(t)
	t.Setenv("CUEFLOW_CF_ACCESS_AUD", "aud")
	if _, err := Load(); err == nil {
		t.Error("an audience without a team domain must be refused")
	}
	base(t)
	t.Setenv("CUEFLOW_CF_ACCESS_TEAM_DOMAIN", "acme.cloudflareaccess.com")
	t.Setenv("CUEFLOW_CF_ACCESS_AUD", "aud")
	c, err := Load()
	if err != nil || c.CFAccessTeamDomain == "" || c.CFAccessAudience != "aud" {
		t.Fatalf("both set: %+v %v", c, err)
	}
}

func TestDirectURLDefaultsToPublicAndFeedsMedia(t *testing.T) {
	base(t)
	t.Setenv("CUEFLOW_PUBLIC_URL", "https://people.example/")
	c, err := Load()
	if err != nil || c.DirectURL != "https://people.example" || c.MediaBaseURL != "https://people.example" {
		t.Fatalf("defaults: direct=%q media=%q %v", c.DirectURL, c.MediaBaseURL, err)
	}
	t.Setenv("CUEFLOW_DIRECT_URL", "https://direct.example/")
	c, err = Load()
	if err != nil || c.DirectURL != "https://direct.example" || c.MediaBaseURL != "https://direct.example" {
		t.Fatalf("direct URL: direct=%q media=%q %v", c.DirectURL, c.MediaBaseURL, err)
	}
	t.Setenv("CUEFLOW_MEDIA_BASE_URL", "https://media.example")
	if c, _ := Load(); c.MediaBaseURL != "https://media.example" {
		t.Errorf("an explicit media URL wins: %q", c.MediaBaseURL)
	}
}

// CUEFLOW_CONTEXT_HANDOFF takes a fraction between 0 and 1; anything else keeps the default and
// leaves a warning for startup.
func TestContextHandOff(t *testing.T) {
	for v, want := range map[string]float64{"": 0.8, "0.6": 0.6, "0.95": 0.95, "0": 0.8, "1": 0.8, "80": 0.8, "-0.2": 0.8, "abc": 0.8} {
		base(t)
		t.Setenv("CUEFLOW_CONTEXT_HANDOFF", v)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		valid := v == "" || v == "0.6" || v == "0.95"
		if c.ContextHandOffAt != want || valid != (len(c.Warnings) == 0) {
			t.Errorf("%q: %v, warnings %v", v, c.ContextHandOffAt, c.Warnings)
		}
	}
}

// The reasons envDuration gives, as they follow the quoted value.
const (
	notADuration = " is not a duration: give a number with a unit, e.g. 30s, 10m or 1h"
	outOfRange   = " is out of range: a duration must be between about -2562047h and 2562047h"
)

func TestPubSubDeleteTimeout(t *testing.T) {
	for _, tc := range []struct {
		v          string
		want       time.Duration
		warn       bool
		err        bool
		unparsable bool // the error must also quote the value
		overflow   bool // the error must say the value is out of range
	}{
		{v: "", want: 8 * time.Second},
		{v: "5s", want: 5 * time.Second},
		{v: "9.5s", want: 9500 * time.Millisecond},
		{v: "10s", want: 10 * time.Second, warn: true},
		{v: "1m", want: time.Minute, warn: true},
		{v: "0", err: true},
		{v: "-2s", err: true},
		{v: "8", err: true, unparsable: true},
		{v: "5sec", err: true, unparsable: true},
		{v: "99999999999h", err: true, overflow: true},
		{v: "-99999999999h", err: true, overflow: true},
		// Shaped like a duration but not one: they keep the typo hint, not the out-of-range one.
		{v: "1hh", err: true, unparsable: true},
		{v: ".s", err: true, unparsable: true},
		{v: "+-1s", err: true, unparsable: true},
		{v: "1e3s", err: true, unparsable: true},
		{v: "1h-1m", err: true, unparsable: true},
	} {
		base(t)
		t.Setenv("CUEFLOW_PUBSUB_DELETE_TIMEOUT", tc.v)
		c, err := Load()
		if tc.err {
			if err == nil || !strings.Contains(err.Error(), "CUEFLOW_PUBSUB_DELETE_TIMEOUT") ||
				tc.unparsable && !strings.Contains(err.Error(), strconv.Quote(tc.v)+notADuration) ||
				tc.overflow && !strings.Contains(err.Error(), strconv.Quote(tc.v)+outOfRange) {
				t.Errorf("%q: want an error naming the setting, got %v", tc.v, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.v, err)
		}
		if c.PubSubDeleteTimeout != tc.want || tc.warn != (len(c.Warnings) > 0) {
			t.Errorf("%q: %v, warnings %v", tc.v, c.PubSubDeleteTimeout, c.Warnings)
		}
	}
}

func TestMCPRequestTimeout(t *testing.T) {
	for _, tc := range []struct {
		v          string
		want       time.Duration
		warn       bool
		err        bool
		unparsable bool // the error must also quote the value
		overflow   bool // the error must say the value is out of range
	}{
		{v: "", want: 10 * time.Minute},
		{v: "5m", want: 5 * time.Minute},
		{v: "29m", want: 29 * time.Minute},
		{v: "30m", want: 30 * time.Minute, warn: true},
		{v: "1h", want: time.Hour, warn: true},
		{v: "30s", want: 30 * time.Second},
		{v: "29s", err: true},
		{v: "0", err: true},
		{v: "-1m", err: true},
		{v: "600", err: true, unparsable: true},
		{v: "5min", err: true, unparsable: true},
		{v: "99999999999h", err: true, overflow: true},
		{v: "-99999999999h", err: true, overflow: true},
		// Shaped like a duration but not one: they keep the typo hint, not the out-of-range one.
		{v: "1hh", err: true, unparsable: true},
		{v: ".s", err: true, unparsable: true},
		{v: "+-1s", err: true, unparsable: true},
		{v: "1e3s", err: true, unparsable: true},
		{v: "1h-1m", err: true, unparsable: true},
		{v: " 5m ", want: 5 * time.Minute},
	} {
		base(t)
		t.Setenv("CUEFLOW_MCP_REQUEST_TIMEOUT", tc.v)
		c, err := Load()
		if tc.err {
			if err == nil || !strings.Contains(err.Error(), "CUEFLOW_MCP_REQUEST_TIMEOUT") ||
				tc.unparsable && !strings.Contains(err.Error(), strconv.Quote(tc.v)+notADuration) ||
				tc.overflow && !strings.Contains(err.Error(), strconv.Quote(tc.v)+outOfRange) {
				t.Errorf("%q: want an error naming the setting, got %v", tc.v, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.v, err)
		}
		if c.MCPRequestTimeout != tc.want || tc.warn != (len(c.Warnings) > 0) {
			t.Errorf("%q: %v, warnings %v", tc.v, c.MCPRequestTimeout, c.Warnings)
		}
	}
}

// The duration settings from before CUEFLOW_PUBSUB_DELETE_TIMEOUT took a bad value silently, so they
// keep their default and warn instead of failing: a deployment that booted still boots. A value that is
// not > 0 is bad too: an interval of 0 panicked the deliverables worker, and a lease or timeout of 0 means nothing.
func TestOlderDurationsWarnInsteadOfFailing(t *testing.T) {
	for _, tc := range []struct {
		k   string
		def time.Duration
		get func(*Config) time.Duration
	}{
		{"CUEFLOW_DISPATCH_INTERVAL", 3 * time.Second, func(c *Config) time.Duration { return c.DispatchEvery }},
		{"CUEFLOW_DISPATCH_LEASE", 10 * time.Minute, func(c *Config) time.Duration { return c.DispatchLease }},
		{"CUEFLOW_PUBLISH_TIMEOUT", 5 * time.Minute, func(c *Config) time.Duration { return c.PublishTimeout }},
	} {
		base(t)
		t.Setenv(tc.k, "5min")
		c, err := Load()
		if err != nil {
			t.Fatalf("%s: %v", tc.k, err)
		}
		if got := tc.get(c); got != tc.def || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], tc.k+`="5min"`) ||
			!strings.Contains(c.Warnings[0], "using "+tc.def.String()) {
			t.Errorf("%s=5min: %v, warnings %v", tc.k, got, c.Warnings)
		}
		t.Setenv(tc.k, "7m")
		if c, err = Load(); err != nil || tc.get(c) != 7*time.Minute || len(c.Warnings) != 0 {
			t.Errorf("%s=7m: %v %v", tc.k, c, err)
		}
		for v, msg := range map[string]string{"0": " must be > 0", "0s": " must be > 0", "-1s": " must be > 0",
			"-5m": " must be > 0", "99999999999h": outOfRange, "-99999999999h": outOfRange,
			"1hh": notADuration, ".s": notADuration, "+-1s": notADuration, "1e3s": notADuration, "1h-1m": notADuration} {
			t.Setenv(tc.k, v)
			if c, err = Load(); err != nil {
				t.Fatalf("%s=%s: %v", tc.k, v, err)
			}
			if got := tc.get(c); got != tc.def || len(c.Warnings) != 1 ||
				!strings.Contains(c.Warnings[0], tc.k+"="+strconv.Quote(v)+msg) ||
				!strings.Contains(c.Warnings[0], "using "+tc.def.String()) {
				t.Errorf("%s=%s: %v, warnings %v", tc.k, v, got, c.Warnings)
			}
		}
	}
}

// A value that is > 0 but below the floor keeps the default too: an interval of 1ns busy-loops the
// deliverables worker, and a lease of a few ms marks publishes that are still running FAILED.
func TestOlderDurationsHaveAFloor(t *testing.T) {
	for _, tc := range []struct {
		k, v string
		want time.Duration
		warn string // "" means no warning
	}{
		{"CUEFLOW_DISPATCH_INTERVAL", "1ns", 3 * time.Second, `CUEFLOW_DISPATCH_INTERVAL="1ns" must be >= 100ms; using 3s`},
		{"CUEFLOW_DISPATCH_INTERVAL", "99ms", 3 * time.Second, `CUEFLOW_DISPATCH_INTERVAL="99ms" must be >= 100ms; using 3s`},
		{"CUEFLOW_DISPATCH_INTERVAL", "100ms", 100 * time.Millisecond, ""},
		{"CUEFLOW_DISPATCH_LEASE", "500ms", 10 * time.Minute, `CUEFLOW_DISPATCH_LEASE="500ms" must be >= 1s; using 10m0s`},
		{"CUEFLOW_DISPATCH_LEASE", "999ms", 10 * time.Minute, `CUEFLOW_DISPATCH_LEASE="999ms" must be >= 1s; using 10m0s`},
		{"CUEFLOW_PUBLISH_TIMEOUT", "500ms", 5 * time.Minute, `CUEFLOW_PUBLISH_TIMEOUT="500ms" must be >= 1s; using 5m0s`},
		{"CUEFLOW_PUBLISH_TIMEOUT", "999ms", 5 * time.Minute, `CUEFLOW_PUBLISH_TIMEOUT="999ms" must be >= 1s; using 5m0s`},
		{"CUEFLOW_PUBLISH_TIMEOUT", "1s", time.Second, ""},
	} {
		base(t)
		t.Setenv(tc.k, tc.v)
		c, err := Load()
		if err != nil {
			t.Fatalf("%s=%s: %v", tc.k, tc.v, err)
		}
		got := map[string]time.Duration{"CUEFLOW_DISPATCH_INTERVAL": c.DispatchEvery,
			"CUEFLOW_DISPATCH_LEASE": c.DispatchLease, "CUEFLOW_PUBLISH_TIMEOUT": c.PublishTimeout}[tc.k]
		var want []string
		if tc.warn != "" {
			want = []string{tc.warn}
		}
		if got != tc.want || strings.Join(c.Warnings, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s=%s: %v, warnings %q", tc.k, tc.v, got, c.Warnings)
		}
	}
}

// The publish timeout should stay below the lease, or the sweeper can fail a publish that is still
// running. Both values are kept: only a warning says so. The check runs on the resolved values, so a
// below-floor value that falls back to its default is checked too.
func TestPublishTimeoutBelowLease(t *testing.T) {
	slow := func(timeout, lease string) string {
		return "CUEFLOW_PUBLISH_TIMEOUT=" + timeout + " is not below CUEFLOW_DISPATCH_LEASE=" + lease +
			": the lease can run out while a publish is still going, and the sweeper then marks it FAILED, so a retry may post it twice"
	}
	for _, tc := range []struct {
		lease, timeout string
		wantL, wantT   time.Duration
		warns          []string
	}{
		{"", "", 10 * time.Minute, 5 * time.Minute, nil},
		{"2m", "", 2 * time.Minute, 5 * time.Minute, []string{slow("5m0s", "2m0s")}},
		{"5m", "5m", 5 * time.Minute, 5 * time.Minute, []string{slow("5m0s", "5m0s")}},
		{"1s", "1s", time.Second, time.Second, []string{slow("1s", "1s")}},
		{"5m", "4m59s", 5 * time.Minute, 4*time.Minute + 59*time.Second, nil},
		{"", "10m", 10 * time.Minute, 10 * time.Minute, []string{slow("10m0s", "10m0s")}},
		{"2s", "1s", 2 * time.Second, time.Second, nil},
		// A fallback can cause the warning: the timeout's default is above this lease ...
		{"2m", "500ms", 2 * time.Minute, 5 * time.Minute,
			[]string{`CUEFLOW_PUBLISH_TIMEOUT="500ms" must be >= 1s; using 5m0s`, slow("5m0s", "2m0s")}},
		// ... and the lease's default is not above this timeout.
		{"500ms", "10m", 10 * time.Minute, 10 * time.Minute,
			[]string{`CUEFLOW_DISPATCH_LEASE="500ms" must be >= 1s; using 10m0s`, slow("10m0s", "10m0s")}},
	} {
		base(t)
		t.Setenv("CUEFLOW_DISPATCH_LEASE", tc.lease)
		t.Setenv("CUEFLOW_PUBLISH_TIMEOUT", tc.timeout)
		c, err := Load()
		if err != nil {
			t.Fatalf("lease %q timeout %q: %v", tc.lease, tc.timeout, err)
		}
		if c.DispatchLease != tc.wantL || c.PublishTimeout != tc.wantT {
			t.Errorf("lease %q timeout %q: got %v, %v", tc.lease, tc.timeout, c.DispatchLease, c.PublishTimeout)
		}
		if !slices.Equal(c.Warnings, tc.warns) {
			t.Errorf("lease %q timeout %q: warnings %q, want %q", tc.lease, tc.timeout, c.Warnings, tc.warns)
		}
	}
}

// CUEFLOW_CONNECTOR_HTTP_TIMEOUT is new, so a bad value stops startup: no floor, no warnings.
func TestConnectorHTTPTimeout(t *testing.T) {
	for _, tc := range []struct {
		v          string
		want       time.Duration
		err        bool
		unparsable bool // the error must also quote the value
		overflow   bool // the error must say the value is out of range
	}{
		{v: "", want: 90 * time.Second},
		{v: "3m", want: 3 * time.Minute},
		{v: "1ms", want: time.Millisecond},
		{v: " 2m ", want: 2 * time.Minute},
		{v: "0", err: true},
		{v: "-1s", err: true},
		{v: "90", err: true, unparsable: true},
		{v: "90sec", err: true, unparsable: true},
		{v: "99999999999h", err: true, overflow: true},
		{v: "-99999999999h", err: true, overflow: true},
	} {
		base(t)
		t.Setenv("CUEFLOW_CONNECTOR_HTTP_TIMEOUT", tc.v)
		c, err := Load()
		if tc.err {
			if err == nil || !strings.Contains(err.Error(), "CUEFLOW_CONNECTOR_HTTP_TIMEOUT") ||
				tc.unparsable && !strings.Contains(err.Error(), strconv.Quote(tc.v)+notADuration) ||
				tc.overflow && !strings.Contains(err.Error(), strconv.Quote(tc.v)+outOfRange) {
				t.Errorf("%q: want an error naming the setting, got %v", tc.v, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.v, err)
		}
		if c.ConnectorHTTPTimeout != tc.want || len(c.Warnings) > 0 {
			t.Errorf("%q: %v, warnings %v", tc.v, c.ConnectorHTTPTimeout, c.Warnings)
		}
	}
}

// Unset durations take their defaults without a word: only a bad value warns.
func TestUnsetDurationsDoNotWarn(t *testing.T) {
	base(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DispatchEvery != 3*time.Second || c.DispatchLease != 10*time.Minute || c.PublishTimeout != 5*time.Minute ||
		c.ConnectorHTTPTimeout != 90*time.Second || len(c.Warnings) != 0 {
		t.Errorf("defaults: %v %v %v %v, warnings %q", c.DispatchEvery, c.DispatchLease, c.PublishTimeout, c.ConnectorHTTPTimeout, c.Warnings)
	}
}

// Media register works in any mode, but only over GCS media storage: it adopts files written to the bucket.
func TestMediaRegisterNeedsGCS(t *testing.T) {
	for _, tick := range []string{"", "external"} {
		base(t)
		t.Setenv("CUEFLOW_TICK", tick)
		t.Setenv("CUEFLOW_MEDIA_REGISTER_ACCOUNTS", "render@p.iam.gserviceaccount.com, lessons@p.iam.gserviceaccount.com")
		for _, storage := range []string{"", "file:///data/media", "/data/gcs://bucket"} {
			t.Setenv("CUEFLOW_MEDIA_STORAGE", storage)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "CUEFLOW_MEDIA_REGISTER_ACCOUNTS") || !strings.Contains(err.Error(), "CUEFLOW_MEDIA_STORAGE") {
				t.Errorf("tick %q, storage %q: %v", tick, storage, err)
			}
		}
	}
	for _, storage := range []string{"gcs://bucket/media", "GCS://bucket"} {
		base(t)
		t.Setenv("CUEFLOW_MEDIA_STORAGE", storage)
		t.Setenv("CUEFLOW_MEDIA_REGISTER_ACCOUNTS", "render@p.iam.gserviceaccount.com, lessons@p.iam.gserviceaccount.com")
		c, err := Load()
		if err != nil {
			t.Fatalf("single mode with GCS media %q: %v", storage, err)
		}
		if c.Cloud() || len(c.MediaRegisterAccounts) != 2 {
			t.Fatalf("%+v", c)
		}
	}
}

func TestInternalAudiences(t *testing.T) {
	base(t)
	t.Setenv("CUEFLOW_PUBLIC_URL", "https://cueflow.example.com")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.InternalAudiences, []string{"https://cueflow.example.com"}) {
		t.Fatalf("default, one URL: %v", c.InternalAudiences)
	}
	t.Setenv("CUEFLOW_DIRECT_URL", "https://direct.example.com/")
	t.Setenv("CUEFLOW_TASKS_TARGET_URL", "https://cueflow-abc.a.run.app")
	if c, err = Load(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.InternalAudiences, []string{"https://cueflow-abc.a.run.app", "https://direct.example.com"}) {
		t.Fatalf("default: the tasks target first, then the direct URL: %v", c.InternalAudiences)
	}
	t.Setenv("CUEFLOW_DIRECT_URL", "")
	t.Setenv("CUEFLOW_TASKS_TARGET_URL", "")
	t.Setenv("CUEFLOW_INTERNAL_AUDIENCE", " https://cueflow.example.com , http://10.0.0.2:3000,")
	if c, err = Load(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.InternalAudiences, []string{"https://cueflow.example.com", "http://10.0.0.2:3000"}) || slices.Contains(c.InternalAudiences, "https://direct.example.com") {
		t.Fatalf("list: %v", c.InternalAudiences)
	}
	t.Setenv("CUEFLOW_INTERNAL_AUDIENCE", ",")
	t.Setenv("CUEFLOW_MEDIA_STORAGE", "gcs://bucket")
	t.Setenv("CUEFLOW_MEDIA_REGISTER_ACCOUNTS", "render@p.iam.gserviceaccount.com")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CUEFLOW_INTERNAL_AUDIENCE") {
		t.Fatalf("an audience list with no entry: %v", err)
	}
}
