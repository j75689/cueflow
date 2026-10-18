package content

import (
	"strings"

	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/store"
	"reflect"
	"testing"
)

func TestCleanMediaURLs(t *testing.T) {
	got, err := cleanMediaURLs([]string{
		" /media/e3e8c5e8-88bf-4917-b0a8-c6ff2ecac11b.mp4 ", "", "https://cdn.example.com/a.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/media/e3e8c5e8-88bf-4917-b0a8-c6ff2ecac11b.mp4", "https://cdn.example.com/a.png"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	for _, bad := range []string{"http://example.com/a.png", "/etc/passwd", "/media/not-an-id", "file:///x"} {
		if _, err := cleanMediaURLs([]string{bad}); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}

	if got, err := cleanMediaURLs(nil); err != nil || len(got) != 0 {
		t.Fatalf("an empty list is allowed (it removes the media): %v %v", got, err)
	}
}

// An agent that drafts posts is told the publishing rules; one without the capability is not.
func TestDraftingPromptLines(t *testing.T) {
	register()
	task := &store.Task{Number: 7}
	writer := &store.Agent{Name: "Writer", Role: "writer", Capabilities: []string{PostDeliverablesCapability}, Platforms: []string{"threads"}}
	got := crew.SystemPrompt(writer, task, nil, nil)
	for _, s := range []string{"cannot publish to social media", "human review queue", "draft IDs", "write drafts for: threads"} {
		if !strings.Contains(got, s) {
			t.Errorf("a drafting agent must be told %q:\n%s", s, got)
		}
	}
	coder := &store.Agent{Name: "Dev", Role: "custom", Capabilities: []string{"planning"}}
	if got := crew.SystemPrompt(coder, task, nil, nil); strings.Contains(got, "review queue") || strings.Contains(got, "write drafts for") {
		t.Errorf("an agent without drafting must not see the content rules:\n%s", got)
	}
}

// An agent saved with the old "drafting" id still gets the drafting tools and prompt.
func TestDraftingAliasKeepsOldAgentsWorking(t *testing.T) {
	register()
	old := &store.Agent{Name: "Old", Role: "writer", Capabilities: []string{"drafting", "imaging"}, Platforms: []string{"threads"}}
	if !old.Has(PostDeliverablesCapability) || old.Has("imaging") && false {
		t.Fatalf("alias: %v", old.Capabilities)
	}
	if got := crew.SystemPrompt(old, &store.Task{Number: 1}, nil, nil); !strings.Contains(got, "write drafts for: threads") {
		t.Fatalf("prompt for an aliased capability:\n%s", got)
	}
}
