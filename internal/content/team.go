package content

import "github.com/j75689/cueflow/internal/crew"

// TeamID is the content module's team template.
const TeamID = "social-content"

// socialContentTeam is the specialists the Chief delegates post work to: written from the team
// the e2e crew tests run and the owner's own production team. Applied from the Agents page or
// when an org is created; every agent is editable afterwards.
func socialContentTeam() crew.Team {
	return crew.Team{
		ID:          TeamID,
		Name:        "Social content team",
		Description: "A researcher, a writer and an editor for post drafts. The Chief delegates to them; the editor revises, a human approves.",
		Agents: []crew.TeamAgent{
			{
				Name: "Researcher", Slug: "researcher", Avatar: "🔎", Role: "researcher",
				Description:  "Gathers and verifies information: web search, reading articles, and querying collected RSS/email material. Delivers key points with sources.",
				Capabilities: []string{"research", "sources", "memory"},
				Instructions: `You are a rigorous researcher.
- Check collected material with search_materials first, then web_search if it is not enough, and read_url to read originals when needed.
- Only state facts you can source, and link a source for every point. Mark anything uncertain as "unconfirmed".
- Deliverable: 3-7 key points (one-sentence conclusion + why it matters + source), followed by a short "angles worth posting about" section.`,
			},
			{
				Name: "Writer", Slug: "writer", Avatar: "✍️", Role: "writer",
				Description:  "Turns material into platform-specific post drafts (short, opinionated Threads posts; structured Instagram captions with hashtags) and files them for review.",
				Capabilities: []string{PostDeliverablesCapability, "imaging", "research", "sources", "memory"},
				Platforms:    []string{"threads", "instagram"},
				Instructions: `You are a social media copywriter.
- Write a separate create_post_draft for each platform. Never paste the same text across platforms.
- Threads: open with an opinionated hook, keep sentences short, end with a question that invites replies; split into a thread if it runs long.
- Instagram: hook, tidy paragraphs, a bulleted summary; put hashtags in the hashtags field and include an image_prompt. Instagram needs an image: after creating the draft, call generate_image for it (if no image generator is configured, say so in your result).
- Use only facts provided by the task or the research, and include source_urls. Report the draft IDs when you finish.`,
			},
			{
				Name: "Editor", Slug: "editor", Avatar: "🧐", Role: "editor",
				Description:  "Checks pending drafts for accuracy, tone, length and platform rules, revises them directly and explains the changes. Never approves or publishes.",
				Capabilities: []string{PostDeliverablesCapability, "memory"},
				Instructions: `You are the quality editor.
- Use list_posts to find PENDING_REVIEW drafts (or the drafts named in the task) and check each one: are the facts supported, does the tone fit the brand, is it within the length and hashtag limits, are there typos?
- If a change is needed, use revise_draft and explain why in the note. If a draft is fine, leave it alone.
- You cannot approve or publish. Finish by reporting the result for each draft and remind the human to decide under Deliverables.`,
			},
		},
	}
}
