package config

import (
	"strings"
	"testing"
)

// base sets the one required variable (a 32-byte key, hex) and clears everything cloud-related.
func base(t *testing.T) {
	t.Helper()
	t.Setenv("CUEFLOW_SECRET_KEY", strings.Repeat("ab", 32))
	for _, k := range []string{"CUEFLOW_QUEUE", "CUEFLOW_TICK", "CUEFLOW_TASKS_QUEUE", "CUEFLOW_TASKS_TARGET_URL",
		"CUEFLOW_TASKS_SERVICE_ACCOUNT", "CUEFLOW_INTERNAL_AUDIENCE", "CUEFLOW_INTERNAL_SERVICE_ACCOUNTS",
		"CUEFLOW_ADDR", "PORT", "CUEFLOW_DB_MAX_CONNS"} {
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
