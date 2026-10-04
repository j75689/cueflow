package connector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/guard"
)

type fakePublisher struct{ calls int }

func (f *fakePublisher) Test(context.Context) (*connector.TestResult, error) {
	return &connector.TestResult{OK: true}, nil
}

func (f *fakePublisher) Publish(context.Context, connector.PublishRequest) (*connector.PublishResult, error) {
	f.calls++
	return &connector.PublishResult{ExternalIDs: map[string]string{"threads": "1"}}, nil
}

var last *fakePublisher

func init() {
	connector.Register(connector.Spec{
		Type: "test_publisher", Kind: connector.KindPublisher, Name: "Test publisher",
		Fields: []connector.Field{
			{Key: "url", Label: "URL", Type: connector.FieldString, Required: true},
			{Key: "token", Label: "Token", Type: connector.FieldPassword, Secret: true},
			{Key: "mode", Label: "Mode", Type: connector.FieldSelect, Options: []string{"a", "b"}, Default: "a"},
			{Key: "n", Label: "N", Type: connector.FieldNumber},
		},
	}, func(cfg connector.Config, env connector.Env) (connector.Instance, error) {
		last = &fakePublisher{}
		return last, nil
	})
}

func TestGuardedPublisher(t *testing.T) {
	inst, err := connector.Build("test_publisher", connector.Config{"url": "http://x"}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	pub := inst.(connector.Publisher)
	postID := uuid.New()
	req := connector.PublishRequest{PostID: postID, Platforms: []string{"threads"}, Parts: []string{"hi"}}

	if _, err := pub.Publish(context.Background(), req); !errors.Is(err, guard.ErrPublishForbidden) {
		t.Fatalf("publish without grant: got %v", err)
	}
	if _, err := pub.Publish(guard.WithPublishGrant(context.Background(), uuid.New()), req); !errors.Is(err, guard.ErrPublishForbidden) {
		t.Fatalf("publish with grant for another post: got %v", err)
	}
	agentCtx := guard.WithAgent(guard.WithPublishGrant(context.Background(), postID))
	if _, err := pub.Publish(agentCtx, req); !errors.Is(err, guard.ErrPublishForbidden) {
		t.Fatalf("publish from agent context: got %v", err)
	}
	if last.calls != 0 {
		t.Fatalf("underlying publisher was called %d times", last.calls)
	}
	if _, err := pub.Publish(guard.WithPublishGrant(context.Background(), postID), req); err != nil {
		t.Fatalf("granted publish failed: %v", err)
	}
	if last.calls != 1 {
		t.Fatalf("calls = %d", last.calls)
	}
}

func TestSpecValidationAndSplit(t *testing.T) {
	spec, ok := connector.Lookup("test_publisher")
	if !ok {
		t.Fatal("not registered")
	}
	if err := spec.Validate(spec.WithDefaults(connector.Config{})); err == nil {
		t.Error("missing required field should fail")
	}
	if err := spec.Validate(connector.Config{"url": "x", "mode": "c"}); err == nil {
		t.Error("bad select option should fail")
	}
	if err := spec.Validate(connector.Config{"url": "x", "n": "abc"}); err == nil {
		t.Error("non-numeric number should fail")
	}
	if err := spec.Validate(connector.Config{"url": "x", "n": "3"}); err != nil {
		t.Error(err)
	}
	cfg, secrets := spec.Split(map[string]any{"url": "u", "token": "t", "unknown": 1})
	if cfg["url"] != "u" || secrets["token"] != "t" || cfg["token"] != nil || cfg["unknown"] != nil {
		t.Errorf("split: cfg=%v secrets=%v", cfg, secrets)
	}
	if got := spec.WithDefaults(connector.Config{})["mode"]; got != "a" {
		t.Errorf("default mode = %v", got)
	}
}

func TestConfigHelpers(t *testing.T) {
	c := connector.Config{
		"s": " x ", "n": 3.0, "ns": "4", "b": "true",
		"l": []any{"a", " ", "b"}, "lt": "a, b\nc", "kv": map[string]any{"threads": "id1"},
	}
	if c.String("s") != "x" || c.Int("n", 0) != 3 || c.Int("ns", 0) != 4 || !c.Bool("b") {
		t.Error("scalar helpers")
	}
	if got := c.StringList("l"); len(got) != 2 {
		t.Errorf("list: %v", got)
	}
	if got := c.StringList("lt"); len(got) != 3 {
		t.Errorf("text list: %v", got)
	}
	if c.KV("kv")["threads"] != "id1" {
		t.Error("kv")
	}
	if c.Int("missing", 7) != 7 {
		t.Error("default int")
	}
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	connector.Register(connector.Spec{Type: "test_publisher", Kind: connector.KindPublisher},
		func(connector.Config, connector.Env) (connector.Instance, error) { return nil, nil })
}

func TestDefaultNames(t *testing.T) {
	spec := connector.Spec{Name: "Thing", Fields: []connector.Field{{Key: "mode", Default: "fast"}},
		DefaultName: func(cfg connector.Config) string { return connector.WithDetail("Thing", cfg.String("mode")) }}
	if got := spec.NameFor(connector.Config{}); got != "Thing · fast" {
		t.Fatalf("defaults should apply before naming: %q", got)
	}
	if got := (connector.Spec{Name: "Plain"}).NameFor(nil); got != "Plain" {
		t.Fatalf("fallback: %q", got)
	}
	for typ, want := range map[string]string{"acp_agent": "Claude Code (ACP)", "openai": "OpenAI"} {
		if s, ok := connector.Lookup(typ); ok && s.NameFor(connector.Config{}) != want {
			t.Errorf("%s default name = %q, want %q", typ, s.NameFor(connector.Config{}), want)
		}
	}
}
