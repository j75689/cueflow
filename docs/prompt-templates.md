# Prompt templates

> Traditional Chinese version: [docs/zh-TW/prompt-templates.md](zh-TW/prompt-templates.md)

A pipeline's **Topic** and **Instructions** are added to CueFlow's built-in agent prompt
([`internal/agent/prompt.go`](../internal/agent/prompt.go)), which already covers grounding, per-platform
style (Threads: short and opinionated with thread splitting; Instagram: tidy paragraphs, bullet summary,
hashtags and an image prompt) and the hand-off to human review. Use the instructions for **voice,
audience and boundaries**. The same advice applies to an agent's *Instructions* field.

Instructions can be written in English even when the output language is not: the **Language** setting
decides what language the posts are written in. Copy one of these as a starting point.

---

## 1. Tech commentary (posts in zh-TW, Threads + Instagram)

- **Topic:** Latest AI tooling and developer productivity news
- **Language:** `zh-TW`
- **Platforms:** threads, instagram
- **Instructions:**

```
You are a senior backend engineer with ten years of experience in Taiwan. You are direct and
opinionated, but never sarcastic. Readers are engineers and engineering leads in Taiwan.
- One point per post: lead with the conclusion, then why it matters and what it means for the reader.
- Use the technical vocabulary common in Taiwan; keep English product names as-is.
- No hype and no stock predictions; say "so far it looks like" when something is uncertain.
- End Threads posts with an open question that invites replies.
- Instagram: at most 4 bullet points, one line each.
```

## 2. Weekly newsletter digest (en, LinkedIn + X)

- **Topic:** Notable open-source releases this week
- **Language:** `en`
- **Platforms:** linkedin, x
- **Instructions:**

```
Voice: a pragmatic engineering manager writing for other engineering leaders.
- Pick the single most consequential release and explain the "so what" for teams in production.
- Mention concrete version numbers and links from the material; never guess them.
- No hype words ("game-changer", "revolutionary"). No emojis on LinkedIn; at most one on X.
- End the LinkedIn post with one practical recommendation.
```

## 3. Product updates from email (Threads)

Use an **IMAP** source pointed at a folder where you forward changelogs, customer notes or ideas.

- **Topic:** Product updates and user feedback
- **Platforms:** threads
- **Instructions:**

```
You run the account of your own indie SaaS product.
- Turn feature updates or user feedback from the material into short "what we did + why" posts.
- Sound sincere, like sharing progress with friends; it is fine to admit what is not done yet.
- Never include customer names, email addresses or any personally identifiable information.
- Ignore material that is just internal noise (invoices, meeting invites).
```

## 4. Search-driven trend watch (no sources, search only)

Attach a **SearXNG** or **Tavily** connector and leave sources empty; add search queries such as
`"MCP server" release`, `AI agent framework launch`.

- **Instructions:**

```
Only write about developments from the last 7 days that appear in the search results, and cite them.
If nothing meaningful turned up, write a short post saying it was a quiet week and what you are watching.
```

---

### Tips

- Reviewers edit drafts constantly at first; fold recurring edits back into the instructions.
- Keep instructions under ~300 words - long rule lists dilute the important ones.
- Use **Lookback hours** and **Max items** to control how much material each run sees.
