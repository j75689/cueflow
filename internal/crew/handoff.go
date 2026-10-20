package crew

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// A run whose context passes Crew.ContextHandOffAt of the model's window hands off: it is asked to
// write what a fresh run needs into handOffFile and then ends, waiting for the human. Their reply
// starts a fresh run whose brief carries the note; nothing of the old conversation is replayed, so
// no thinking block is bound to edited turns (the history stays append-only: the request is a new
// message at the end).

const (
	// handOffFile is the task file the note goes to; a later hand-off replaces it.
	handOffFile = "handoff.md"
	// handOffBriefChars caps the note quoted in the next brief; the file has all of it.
	handOffBriefChars = 12000
	// handOffTurns is how many turns past the request a run gets to write the note, beyond its turn
	// limit if need be: one to write it, one more if it did something else first.
	handOffTurns = 2
	// handOffGrowth is how much of the window a run must have added since its first prompt before it
	// hands off. A fresh run starts at about its first prompt, so handing off sooner would only make
	// the next run hand off again.
	handOffGrowth = 0.1
)

// handOff tracks one run's hand-off.
type handOff struct {
	first   int64 // the run's first prompt
	asked   bool  // the request was sent
	turns   int   // turns since the request
	skipped bool  // past the point without having grown much: a fresh run wouldn't be smaller
}

// overHandOff reports whether the run's latest prompt reached the hand-off point of a known window.
func (c *Crew) overHandOff(run *store.AgentRun) bool {
	return run.ContextWindow > 0 && float64(run.ContextTokens) >= c.ContextHandOffAt*float64(run.ContextWindow)
}

// contextPct is how full the run's context is, in percent (0 when the window is unknown).
func contextPct(run *store.AgentRun) int64 {
	if run.ContextWindow <= 0 {
		return 0
	}
	return run.ContextTokens * 100 / run.ContextWindow
}

// handOffRequest is the message that asks for the note.
func handOffRequest(run *store.AgentRun) string {
	return fmt.Sprintf("Your context is %d%% full (%d of %d tokens), so this run ends now and a fresh run picks up the task "+
		"from its description, thread and files. Before it ends, write a hand-off note with write_file, name %q: what you have "+
		"done, the facts and decisions you confirmed, what is left, and which task files to read first. Keep it to what the next "+
		"run needs; don't start new work. After saving it, stop: no more tool calls.",
		contextPct(run), run.ContextTokens, run.ContextWindow, handOffFile)
}

// handOffOutcome ends the run after the note. The thread gets a short pointer to the file (the next
// brief quotes the note itself, once); if no note was saved, the outcome says so.
func (c *Crew) handOffOutcome(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun, written bool) *outcome {
	pct := contextPct(run)
	if written {
		if _, size, ok := c.handOffNote(ctx, task); ok {
			c.say(ctx, task.ID, a, "message", fmt.Sprintf("Hand-off note saved as %s (%s).", handOffFile, humanBytes(size)),
				map[string]any{"handoff": true, "file": handOffFile})
			return &outcome{kind: "handoff", noted: true, text: fmt.Sprintf("The agent's context reached %d%% of the model's window, so it handed off. "+
				"Review its note (%s; reply with corrections if anything is off), then reply to continue in a fresh run that starts from the note.",
				pct, handOffFile)}
		}
	}
	return &outcome{kind: "handoff", text: fmt.Sprintf("The agent's context reached %d%% of the model's window, and it was asked for a "+
		"hand-off note but none was saved. Reply to continue in a fresh run (it starts from the task, its thread and its files).", pct)}
}

// handOffNote is the start of the task's own hand-off note (enough to quote) and the file's size, if
// it has one that is text.
func (c *Crew) handOffNote(ctx context.Context, task *store.Task) (string, int64, bool) {
	f, rc, err := c.store.OpenTaskFile(ctx, task.ID, handOffFile)
	if err != nil {
		return "", 0, false
	}
	defer rc.Close()
	if f.TaskID != task.ID { // an ancestor's note is about its own work
		return "", 0, false
	}
	b, err := io.ReadAll(io.LimitReader(rc, int64(4*handOffBriefChars+utf8.UTFMax)))
	if err != nil {
		return "", 0, false
	}
	// The limit may cut the last character: drop its bytes, then the rest must be text.
	for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	if !utf8.Valid(b) {
		return "", 0, false
	}
	return strings.TrimSpace(string(b)), f.SizeBytes, true
}

// handOffBrief is the brief's section with the previous run's note: only when that run handed off
// (it wrote the note), so a note isn't quoted again in every brief after the task moved on.
func (c *Crew) handOffBrief(ctx context.Context, task *store.Task) string {
	if !c.lastRunHandedOff(ctx, task) {
		return ""
	}
	note, _, ok := c.handOffNote(ctx, task)
	if !ok || note == "" {
		return ""
	}
	return fmt.Sprintf("\n## Hand-off from the previous run\nThe previous run handed off when its context filled up. Its note (%s), "+
		"reviewed by the human, is where to start; the thread below has anything they added.\n\n%s\n", handOffFile,
		clipNoted(note, handOffBriefChars))
}

// lastRunHandedOff reports whether the task's latest finished run wrote its hand-off note.
func (c *Crew) lastRunHandedOff(ctx context.Context, task *store.Task) bool {
	runs, err := c.store.ListAgentRuns(ctx, task.ID)
	if err != nil {
		return false
	}
	files, _, err := c.store.ListTaskFiles(ctx, task.ID, 1000)
	if err != nil {
		return false
	}
	for _, r := range runs { // newest first
		if r.FinishedAt == nil {
			continue // this run, or one still going
		}
		for _, f := range files {
			if f.Name == handOffFile && f.TaskID == task.ID {
				return !f.UpdatedAt.Before(r.StartedAt) && !f.UpdatedAt.After(*r.FinishedAt)
			}
		}
		return false
	}
	return false
}

// afterTurn decides, after a turn's tools ran, whether the run goes on, asks for the note or ends.
// It returns the message to append (the request) or the outcome to end with.
func (c *Crew) afterTurn(ctx context.Context, h *handOff, env *runEnv, run *store.AgentRun) (*llm.Message, *outcome) {
	if run.Turns == 1 {
		h.first = run.ContextTokens
	}
	switch {
	case h.asked:
		h.turns++
		if env.wroteHandOff {
			return nil, c.handOffOutcome(ctx, env.task, env.agent, run, true)
		}
		if h.turns >= handOffTurns {
			return nil, c.handOffOutcome(ctx, env.task, env.agent, run, false)
		}
	case h.skipped || !c.overHandOff(run):
	case float64(run.ContextTokens-h.first) < handOffGrowth*float64(run.ContextWindow):
		// Past the point, but this run's first prompt (the brief, instructions and tools) was already
		// nearly as big: a fresh run would start about as full and hand off again. Say so and carry on.
		h.skipped = true
		c.system(ctx, env.task.ID, fmt.Sprintf("This run's context is %d%% full, but its first prompt alone was %d%% of the model's window, "+
			"so it won't hand off: a fresh run would start almost as full. Trim the task's description or thread, or use a model with a "+
			"larger window, if it runs out.", contextPct(run), h.first*100/run.ContextWindow))
	default:
		h.asked = true
		env.wroteHandOff = false // only a note written after the request counts
		return &llm.Message{Role: llm.RoleUser, Content: handOffRequest(run)}, nil
	}
	return nil, nil
}

// maxAutoHandOffs caps the hand-offs in a row that continue without a human; the next waits for one.
const maxAutoHandOffs = 3

// autoHandOff reports whether a hand-off continues in a fresh run on its own, and the one message for
// the thread either way. Only a hand-off with a saved note by an agent with AutoHandOff (which the
// store clears for external runtimes), and only up to maxAutoHandOffs in a row. The streak is read
// from the task's recorded runs: it counts the latest runs that continued on their own
// ("handoff_auto"). A hand-off that waited ("handoff") ends it, since whatever moved the task on
// after that was a human; so does any other outcome, or a human message since a run started.
func (c *Crew) autoHandOff(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun, out *outcome) (bool, string) {
	if a == nil || !a.AutoHandOff || !out.noted || run == nil {
		return false, out.text
	}
	runs, err := c.store.ListAgentRuns(ctx, task.ID)
	if err != nil {
		c.logger.Warn("count automatic hand-offs; waiting for the human", "task", task.ID, "err", err)
		return false, out.text
	}
	lastHuman, spoke, err := c.store.LastHumanMessageAt(ctx, task.ID)
	if err != nil {
		c.logger.Warn("count automatic hand-offs; waiting for the human", "task", task.ID, "err", err)
		return false, out.text
	}
	streak := 1              // this run
	for _, r := range runs { // newest first
		if r.ID == run.ID || r.FinishedAt == nil {
			continue
		}
		if r.Outcome != outcomeAutoHandOff || spoke && lastHuman.After(r.StartedAt) {
			break
		}
		streak++
	}
	if streak > maxAutoHandOffs {
		return false, out.text + fmt.Sprintf(" This is the %d%s hand-off in a row, so it waits for you this time.", streak, ordinal(streak))
	}
	return true, fmt.Sprintf("The agent's context reached %d%% of the model's window, so it handed off and continues in a fresh run "+
		"that starts from %s (automatic hand-off %d of %d in a row; the agent's Auto hand-off setting). Comment any time to steer it.",
		contextPct(run), handOffFile, streak, maxAutoHandOffs)
}

// outcomeAutoHandOff is the outcome recorded for a hand-off that continued without waiting.
const outcomeAutoHandOff = "handoff_auto"

func ordinal(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 13:
		return "th"
	case n%10 == 1:
		return "st"
	case n%10 == 2:
		return "nd"
	case n%10 == 3:
		return "rd"
	}
	return "th"
}
