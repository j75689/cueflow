package crew

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/j75689/cueflow/internal/store"
	"github.com/j75689/cueflow/internal/textutil"
)

const basePrompt = `You are %s %s, an AI agent on a team inside CueFlow, a self-hosted agent operations console.
Your role: %s

How you work:
- You are working on task #%d. Read the task and its thread, then continue from where it left off.
- Work autonomously: use your tools to make real progress. Don't ask permission for routine steps.
- Use ask_human only when a decision genuinely needs the human (missing information, a preference, or risk).
- Ground facts in tool results. Never invent sources, numbers, quotes or links.
- Keep the human informed with short add_comment updates on long tasks.
- When done, call complete_task with a concise summary: what you did, decisions made, and outputs (task numbers, links, anything you created).
- Write to the human in %s.`

const delegationPrompt = `
Delegation:
- You lead the team below. Hand specialised work to the right teammate with create_subtask (by slug), giving
  them full context and the expected output. Then call wait_for_subtasks; you will be resumed with their results.
- Prefer delegating over doing a specialist's job yourself, but don't create subtasks for trivial steps.
- Review subtask results before completing; if something is missing, delegate a follow-up.`

const planningPrompt = `
Planning:
- For recurring requests ("every morning…", "weekly…"), create a schedule with schedule_task instead of doing it once.
  Use the timezone the human implies; if it is unclear, ask. Tell the human what you set up.`

func buildSystemPrompt(a *store.Agent, task *store.Task, team []*store.Agent, memories []store.AgentMemory) string {
	var b strings.Builder
	desc := a.Description
	if desc == "" {
		desc = a.Role
	}
	fmt.Fprintf(&b, basePrompt, a.Avatar, a.Name, desc, task.Number, languageName(a.Language))
	for _, p := range capabilityPrompts(a) {
		b.WriteString(p)
	}
	if a.Has("delegation") {
		b.WriteString("\n" + delegationPrompt)
		b.WriteString("\n\nTeam:")
		for _, m := range team {
			if m.ID == a.ID || !m.IsActive {
				continue
			}
			fmt.Fprintf(&b, "\n- @%s (%s %s, %s): %s [can: %s]", m.Slug, m.Avatar, m.Name, m.Role, m.Description, strings.Join(m.Capabilities, ", "))
		}
	}
	if a.Has("planning") {
		b.WriteString("\n" + planningPrompt)
	}
	if strings.TrimSpace(a.Instructions) != "" {
		b.WriteString("\n\nStanding instructions from your operator:\n" + strings.TrimSpace(a.Instructions))
	}
	if len(memories) > 0 {
		b.WriteString("\n\nNotes you saved earlier (most recent first):")
		for _, m := range memories {
			fmt.Fprintf(&b, "\n- %s", clip(m.Content, 400))
		}
	}
	fmt.Fprintf(&b, "\n\nCurrent time: %s.", time.Now().UTC().Format("2006-01-02 15:04 MST"))
	return b.String()
}

// buildBrief renders the task, its relatives and its thread as the opening user message. Each run
// starts fresh from this transcript, so runs are stateless and provider-agnostic.
func (c *Crew) buildBrief(ctx context.Context, task *store.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Task #%d: %s\n", task.Number, task.Title)
	fmt.Fprintf(&b, "Priority: %s", task.Priority)
	if len(task.Labels) > 0 {
		fmt.Fprintf(&b, " · Labels: %s", strings.Join(task.Labels, ", "))
	}
	if task.DueAt != nil {
		fmt.Fprintf(&b, " · Due: %s", task.DueAt.Format("2006-01-02 15:04"))
	}
	b.WriteString("\n")
	if strings.TrimSpace(task.Description) != "" {
		fmt.Fprintf(&b, "\n## Description\n%s\n", clip(task.Description, 6000))
	}
	if task.ParentID != nil {
		if p, err := c.store.GetTask(ctx, *task.ParentID); err == nil {
			fmt.Fprintf(&b, "\n## Parent task #%d: %s\n%s\n", p.Number, p.Title, clip(p.Description, 1500))
		}
	}
	if subs, err := c.store.ListTasks(ctx, store.TaskFilter{ParentID: &task.ID, All: true}); err == nil && len(subs) > 0 {
		b.WriteString("\n## Subtasks\n")
		for _, s := range subs {
			fmt.Fprintf(&b, "- %s\n", taskLine(s))
			if s.Result != "" {
				fmt.Fprintf(&b, "  Result: %s\n", clip(s.Result, 1500))
			}
		}
	}
	deps := Deps{Store: c.store, Hub: c.hub, Broker: c.broker, Logger: c.logger}
	for _, section := range briefs() {
		b.WriteString(section(ctx, deps, task))
	}
	msgs, _ := c.store.TaskMessages(ctx, task.ID)
	const keep = 60
	if len(msgs) > 0 {
		b.WriteString("\n## Thread (oldest first)\n")
		if len(msgs) > keep {
			fmt.Fprintf(&b, "(%d earlier messages omitted)\n", len(msgs)-keep)
			msgs = msgs[len(msgs)-keep:]
		}
		for _, m := range msgs {
			b.WriteString(renderMessage(m) + "\n")
		}
	}
	b.WriteString("\nContinue working on this task now.")
	return b.String()
}

func renderMessage(m *store.TaskMessage) string {
	who := m.AuthorType
	if m.Agent != nil {
		who = "@" + m.Agent.Slug
	}
	ts := m.CreatedAt.UTC().Format("01-02 15:04")
	switch m.Kind {
	case "tool_call":
		var d struct {
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(m.Data, &d)
		return fmt.Sprintf("[%s %s called %s %s]", ts, who, d.Tool, clip(string(d.Arguments), 300))
	case "tool_result":
		return fmt.Sprintf("[%s tool result] %s", ts, clip(m.Content, 500))
	case "status":
		return fmt.Sprintf("[%s system] %s", ts, m.Content)
	case "question":
		return fmt.Sprintf("[%s %s asked the human] %s", ts, who, m.Content)
	case "approval":
		var d struct {
			Server    string          `json:"server"`
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(m.Data, &d)
		return fmt.Sprintf("[%s %s asked the human to approve calling %s on %s %s]", ts, who, d.Tool, d.Server, clip(string(d.Arguments), 300))
	case "result":
		return fmt.Sprintf("[%s %s completed] %s", ts, who, clip(m.Content, 2000))
	}
	return fmt.Sprintf("[%s %s] %s", ts, who, clip(m.Content, 3000))
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

func clip(s string, n int) string { return textutil.Clip(s, n) }
