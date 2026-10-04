package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/j75689/cueflow/internal/domain"
)

const systemPrompt = `You are the content agent inside CueFlow, a self-hosted social media operations console.
Your job: turn the provided source material into high-quality social media drafts. Every draft you
produce goes to a human reviewer, who edits, approves or rejects it. You cannot publish anything and
have no tools that can; never claim that something was posted.

How to work:
- Read the material, pick the one or two most interesting, timely angles for the topic, and write
  original commentary - not a summary of headlines. Have a point of view.
- Ground every factual claim in the material or in web_search results. Never invent numbers, quotes,
  names, dates or links. If the material is thin, write a shorter post rather than padding it.
- Write in the requested language. Follow each platform's style brief exactly; platforms have
  different voices, so do not copy the same text across them.
- Plain text only: no markdown, no bold/italics markers, no headings. Line breaks are fine.
- Put hashtags only in the "hashtags" field, without '#', never inside the content.
- When you are done, call submit_drafts exactly once with one draft per requested platform.
  Do not reply with drafts as plain text.`

// Material is one piece of source content handed to the agent.
type Material struct {
	Ref         string // short reference the model cites, e.g. "S3"
	Source      string
	Title       string
	URL         string
	Author      string
	Content     string
	PublishedAt *time.Time
}

func buildUserPrompt(in Input, maxContent int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Today is %s.\n\n", time.Now().UTC().Format("2006-01-02"))
	if in.Topic != "" {
		fmt.Fprintf(&b, "Topic / focus: %s\n", in.Topic)
	}
	fmt.Fprintf(&b, "Language: %s\n", languageName(in.Language))
	if in.Instructions != "" {
		fmt.Fprintf(&b, "\nOperator instructions (voice, audience, do's and don'ts):\n%s\n", strings.TrimSpace(in.Instructions))
	}

	b.WriteString("\nPlatforms to write for (one draft each):\n")
	for _, id := range in.Platforms {
		p, ok := domain.PlatformByID(id)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "- %s (platform id %q, max %d characters per post", p.Name, p.ID, p.MaxChars)
		if p.SupportsThread {
			b.WriteString(", threads allowed via thread_parts")
		}
		fmt.Fprintf(&b, "): %s\n", p.Style)
	}

	if len(in.Materials) == 0 {
		b.WriteString("\nNo new source material was collected for this run.")
		if in.CanSearch {
			b.WriteString(" Use web_search to find recent, relevant developments for the topic.")
		}
		b.WriteString("\n")
	} else {
		b.WriteString("\nSource material (cite by ref in source_refs):\n")
		for _, m := range in.Materials {
			fmt.Fprintf(&b, "\n[%s] %s", m.Ref, oneLine(m.Title))
			if m.Source != "" {
				fmt.Fprintf(&b, " — %s", m.Source)
			}
			b.WriteString("\n")
			if m.URL != "" {
				fmt.Fprintf(&b, "URL: %s\n", m.URL)
			}
			if m.PublishedAt != nil {
				fmt.Fprintf(&b, "Published: %s\n", m.PublishedAt.Format("2006-01-02"))
			}
			if c := clip(m.Content, maxContent); c != "" {
				fmt.Fprintf(&b, "%s\n", c)
			}
		}
	}
	if in.CanSearch && len(in.SearchQueries) > 0 {
		fmt.Fprintf(&b, "\nSuggested web_search queries (optional): %s\n", strings.Join(in.SearchQueries, "; "))
	}
	b.WriteString("\nWhen ready, call submit_drafts.")
	return b.String()
}

var languageNames = map[string]string{
	"zh-TW": "Traditional Chinese (Taiwan)",
	"zh-CN": "Simplified Chinese",
	"en":    "English",
	"ja":    "Japanese",
	"ko":    "Korean",
}

func languageName(code string) string {
	if n, ok := languageNames[code]; ok {
		return n
	}
	if code == "" {
		return languageNames["en"]
	}
	return code
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if n > 0 && len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
