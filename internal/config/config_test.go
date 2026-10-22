package config

import (
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
		"CUEFLOW_PUBLIC_URL", "CUEFLOW_CF_ACCESS_TEAM_DOMAIN", "CUEFLOW_CF_ACCESS_AUD", "CUEFLOW_PUBSUB_DELETE_TIMEOUT", "CUEFLOW_MCP_REQUEST_TIMEOUT"} {
		t.Setenv(k, "")
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
	if !c.Cloud() || c.TasksTargetURL != "https://cueflow-abc.a.run.app" || c.InternalAudience != c.TasksTargetURL ||
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

func TestPubSubDeleteTimeout(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want time.Duration
		warn bool
		err  bool
	}{
		{v: "", want: 8 * time.Second},
		{v: "5s", want: 5 * time.Second},
		{v: "9.5s", want: 9500 * time.Millisecond},
		{v: "10s", want: 10 * time.Second, warn: true},
		{v: "1m", want: time.Minute, warn: true},
		{v: "0", err: true},
		{v: "-2s", err: true},
	} {
		base(t)
		t.Setenv("CUEFLOW_PUBSUB_DELETE_TIMEOUT", tc.v)
		c, err := Load()
		if tc.err {
			if err == nil || !strings.Contains(err.Error(), "CUEFLOW_PUBSUB_DELETE_TIMEOUT") {
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
		v    string
		want time.Duration
		warn bool
		err  bool
	}{
		{v: "", want: 10 * time.Minute},
		{v: "5m", want: 5 * time.Minute},
		{v: "29m", want: 29 * time.Minute},
		{v: "30m", want: 30 * time.Minute, warn: true},
		{v: "1h", want: time.Hour, warn: true},
		{v: "0", err: true},
		{v: "-1m", err: true},
	} {
		base(t)
		t.Setenv("CUEFLOW_MCP_REQUEST_TIMEOUT", tc.v)
		c, err := Load()
		if tc.err {
			if err == nil || !strings.Contains(err.Error(), "CUEFLOW_MCP_REQUEST_TIMEOUT") {
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
