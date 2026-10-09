package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestTransitionsFollowPRDFlow(t *testing.T) {
	allowed := map[[2]Status]bool{
		{StatusDraft, StatusPendingReview}:    true,
		{StatusPendingReview, StatusApproved}: true,
		{StatusPendingReview, StatusRejected}: true,
		{StatusApproved, StatusQueued}:        true,
		{StatusQueued, StatusPublished}:       true,
		{StatusQueued, StatusFailed}:          true,
		{StatusFailed, StatusQueued}:          true, // manual retry
	}
	for _, from := range AllStatuses {
		for _, to := range AllStatuses {
			want := allowed[[2]Status{from, to}]
			if got := from.CanTransitionTo(to); got != want {
				t.Errorf("%s -> %s: got %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestNoStageSkipping(t *testing.T) {
	// The PRD forbids jumping stages: in particular nothing may reach QUEUED/PUBLISHED without APPROVED.
	for _, from := range []Status{StatusDraft, StatusPendingReview, StatusRejected} {
		for _, to := range []Status{StatusQueued, StatusPublished} {
			err := CheckTransition(from, to)
			var te *TransitionError
			if !errors.As(err, &te) {
				t.Errorf("%s -> %s should be rejected, got %v", from, to, err)
			}
		}
	}
}

func TestEditableDeletable(t *testing.T) {
	for _, s := range AllStatuses {
		wantEdit := s == StatusDraft || s == StatusPendingReview
		if s.Editable() != wantEdit {
			t.Errorf("%s editable = %v", s, s.Editable())
		}
		wantDel := s == StatusDraft || s == StatusRejected
		if s.Deletable() != wantDel {
			t.Errorf("%s deletable = %v", s, s.Deletable())
		}
	}
}

func TestComputeWarnings(t *testing.T) {
	p := &Post{
		Platforms:   []string{"threads", "instagram"},
		ThreadParts: []string{"short", strings.Repeat("x", 501)},
		Hashtags:    []string{"a", "b"},
	}
	w := strings.Join(p.ComputeWarnings(), "\n")
	for _, want := range []string{
		"threads: part 2 has", // over 500
		"threads: 2 hashtags, limit is 1",
		"instagram: threads are not supported",
		"instagram: requires at least one image",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings missing %q:\n%s", want, w)
		}
	}

	ok := &Post{Platforms: []string{"threads"}, Content: "hello"}
	if got := ok.ComputeWarnings(); len(got) != 0 {
		t.Errorf("unexpected warnings: %v", got)
	}
}

func TestFormatHashtags(t *testing.T) {
	if got := FormatHashtags([]string{"#ai", " golang ", ""}); got != "#ai #golang" {
		t.Errorf("got %q", got)
	}
}

func TestValidatePlatforms(t *testing.T) {
	if err := ValidatePlatforms(nil); err == nil {
		t.Error("empty platforms should fail")
	}
	if err := ValidatePlatforms([]string{"myspace"}); err == nil {
		t.Error("unknown platform should fail")
	}
	if err := ValidatePlatforms([]string{"threads", "instagram"}); err != nil {
		t.Error(err)
	}
}

func TestYouTubeWarnings(t *testing.T) {
	p := &Post{
		Platforms: []string{"youtube"},
		Content:   strings.Repeat("標", 101) + "\n說明 <script>",
	}
	w := strings.Join(p.ComputeWarnings(), "\n")
	for _, want := range []string{
		"youtube: the first line becomes the title and is cut at 100 characters (it has 101)",
		"youtube: the title and description can't contain < or >",
		"youtube: requires at least one image or video",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings missing %q:\n%s", want, w)
		}
	}
	ok := &Post{Platforms: []string{"youtube"}, Content: "短標題\n說明", MediaURLs: []string{"/media/x.mp4"}}
	if got := ok.ComputeWarnings(); len(got) != 0 {
		t.Errorf("unexpected warnings: %v", got)
	}
}
