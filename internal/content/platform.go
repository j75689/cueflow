package content

import (
	"fmt"
	"github.com/j75689/cueflow/internal/textutil"
	"strings"
	"unicode/utf8"
)

// Platform describes the publishing constraints CueFlow enforces as review warnings and uses to
// steer generation. Limits mirror the platforms' documented caps.
type Platform struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	MaxChars       int    `json:"max_chars"`
	SupportsThread bool   `json:"supports_thread"`
	MaxHashtags    int    `json:"max_hashtags"`
	RequiresMedia  bool   `json:"requires_media"`
	// Style is the writing brief handed to the agent for this platform.
	Style string `json:"-"`
}

// YouTubeTitleMax is how much of a YouTube post's first line is kept as the title.
const YouTubeTitleMax = 100

var platforms = []Platform{
	{
		ID: "threads", Name: "Threads", MaxChars: 500, SupportsThread: true, MaxHashtags: 1,
		Style: "Short, punchy sentences with a clear, opinionated take that invites replies. " +
			"If the idea needs more room, split it into a thread where every part stands on its own " +
			"(at most 500 characters per part). At most one topic tag. No markdown.",
	},
	{
		ID: "instagram", Name: "Instagram", MaxChars: 2200, MaxHashtags: 30, RequiresMedia: true,
		Style: "A hook line, then tidy short paragraphs and a bulleted summary of the key points. " +
			"End with 5-15 relevant popular hashtags (returned separately, not in the body). " +
			"Also provide an image_prompt describing a single eye-catching visual for the post. No markdown.",
	},
	{
		// A Short: one vertical or square video of up to 3 minutes. The first line of the text is the
		// title (YouTube cuts it at 100 characters) and the rest is the description.
		ID: "youtube", Name: "YouTube", MaxChars: 5000, MaxHashtags: 15, RequiresMedia: true,
		Style: "A YouTube Short. The first line is the video title (at most 100 characters, no < or >), " +
			"then a short description with the key points. A few relevant hashtags (returned separately). " +
			"It needs one vertical video of up to 3 minutes. No markdown.",
	},
	{
		ID: "x", Name: "X", MaxChars: 280, SupportsThread: true, MaxHashtags: 3,
		Style: "Concise and direct. Split into a thread if needed, each part at most 280 characters.",
	},
	{
		ID: "linkedin", Name: "LinkedIn", MaxChars: 3000, MaxHashtags: 5,
		Style: "Professional, insight-driven, short paragraphs, a takeaway at the end, up to 5 hashtags.",
	},
}

func Platforms() []Platform { return platforms }

func PlatformByID(id string) (Platform, bool) {
	for _, p := range platforms {
		if p.ID == id {
			return p, true
		}
	}
	return Platform{}, false
}

// ValidatePlatforms rejects unknown or empty platform lists.
func ValidatePlatforms(ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("at least one platform is required")
	}
	for _, id := range ids {
		if _, ok := PlatformByID(id); !ok {
			return fmt.Errorf("unknown platform %q", id)
		}
	}
	return nil
}

// Warnings lists constraint violations for every target platform. They do not block approval;
// the reviewer decides.
func (p *Post) ComputeWarnings() []string {
	var w []string
	parts := p.Parts()
	for _, id := range p.Platforms {
		pl, ok := PlatformByID(id)
		if !ok {
			w = append(w, fmt.Sprintf("%s: unknown platform", id))
			continue
		}
		if len(parts) > 1 && !pl.SupportsThread {
			w = append(w, fmt.Sprintf("%s: threads are not supported; parts will be joined", id))
			joined := strings.Join(parts, "\n\n") + hashtagSuffix(p.Hashtags)
			if n := utf8.RuneCountInString(joined); n > pl.MaxChars {
				w = append(w, fmt.Sprintf("%s: %d characters exceeds the %d limit", id, n, pl.MaxChars))
			}
		} else {
			for i, part := range parts {
				text := part
				if i == len(parts)-1 {
					text += hashtagSuffix(p.Hashtags)
				}
				if n := utf8.RuneCountInString(text); n > pl.MaxChars {
					label := "content"
					if len(parts) > 1 {
						label = fmt.Sprintf("part %d", i+1)
					}
					w = append(w, fmt.Sprintf("%s: %s has %d characters, limit is %d", id, label, n, pl.MaxChars))
				}
			}
		}
		if pl.MaxHashtags > 0 && len(p.Hashtags) > pl.MaxHashtags {
			w = append(w, fmt.Sprintf("%s: %d hashtags, limit is %d", id, len(p.Hashtags), pl.MaxHashtags))
		}
		if id == "youtube" {
			title, _, _ := strings.Cut(strings.TrimSpace(strings.Join(parts, "\n\n")), "\n")
			if n := utf8.RuneCountInString(strings.TrimSpace(title)); n > YouTubeTitleMax {
				w = append(w, fmt.Sprintf("youtube: the first line becomes the title and is cut at %d characters (it has %d)", YouTubeTitleMax, n))
			}
			if strings.ContainsAny(strings.Join(parts, "\n"), "<>") {
				w = append(w, "youtube: the title and description can't contain < or >")
			}
		}
		if pl.RequiresMedia && len(p.MediaURLs) == 0 {
			w = append(w, fmt.Sprintf("%s: requires at least one image or video", id))
		}
	}
	return w
}

func hashtagSuffix(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return "\n\n" + FormatHashtags(tags)
}

// FormatHashtags renders tags as "#a #b".
func FormatHashtags(tags []string) string { return textutil.FormatHashtags(tags) }
