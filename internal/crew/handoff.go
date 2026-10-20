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
)

// handOff tracks one run's hand-off.
type handOff struct {
	asked   bool // the request was sent
	turns   int  // turns since the request
	skipped bool // the first prompt was already over: a fresh run wouldn't be smaller
}

// over reports whether the run's latest prompt reached the hand-off point of a known window.
func (c *Crew) overHandOff(run *store.AgentRun) bool {
	return run.ContextWindow > 0 && float64(run.ContextTokens) >= c.ContextHandOffAt*float64(run.ContextWindow)
}

// handOffRequest is the message that asks for the note.
func handOffRequest(run *store.AgentRun) string {
	return fmt.Sprintf("Your context is %d%% full (%d of %d tokens), so this run ends now and a fresh run picks up the task "+
		"from its description, thread and files. Before it ends, write a hand-off note with write_file, name %q: what you have "+
		"done, the facts and decisions you confirmed, what is left, and which task files to read first. Keep it to what the next "+
		"run needs; don't start new work. After saving it, stop: no more tool calls.",
		run.ContextTokens*100/run.ContextWindow, run.ContextTokens, run.ContextWindow, handOffFile)
}

// handOffOutcome ends the run after the note: with it if one was written, saying so if not.
func (c *Crew) handOffOutcome(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun, written bool) *outcome {
	pct := run.ContextTokens * 100 / max(run.ContextWindow, 1)
	if !written {
		return &outcome{kind: "handoff", text: fmt.Sprintf("The agent's context reached %d%% of the model's window, and it was asked for a "+
			"hand-off note but didn't write one. Reply to continue in a fresh run (it starts from the task, its thread and its files).", pct)}
	}
	if note, ok := c.handOffNote(ctx, task); ok {
		c.say(ctx, task.ID, a, "message", "Hand-off note:\n\n"+clip(note, 4000), map[string]any{"handoff": true, "file": handOffFile})
	}
	return &outcome{kind: "handoff", text: fmt.Sprintf("The agent's context reached %d%% of the model's window, so it handed off. "+
		"Review its note (%s; edit it in the thread by replying with corrections), then reply to continue in a fresh run that starts from the note.",
		pct, handOffFile)}
}

// handOffNote is the task's own hand-off note, if it has one.
func (c *Crew) handOffNote(ctx context.Context, task *store.Task) (string, bool) {
	f, rc, err := c.store.OpenTaskFile(ctx, task.ID, handOffFile)
	if err != nil {
		return "", false
	}
	defer rc.Close()
	if f.TaskID != task.ID { // an ancestor's note is about its own work
		return "", false
	}
	b, err := io.ReadAll(io.LimitReader(rc, 4*handOffBriefChars))
	if err != nil || !utf8.Valid(b) {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// handOffBrief is the brief's section with the previous run's note.
func (c *Crew) handOffBrief(ctx context.Context, task *store.Task) string {
	note, ok := c.handOffNote(ctx, task)
	if !ok || note == "" {
		return ""
	}
	return fmt.Sprintf("\n## Hand-off from the previous run\nThe previous run handed off when its context filled up. Its note (%s), "+
		"reviewed by the human, is where to start; the thread below has anything they added.\n\n%s\n", handOffFile,
		clipNoted(note, handOffBriefChars))
}

// afterTurn decides, after a turn's tools ran, whether the run goes on, asks for the note or ends.
// It returns the message to append (the request) or the outcome to end with.
func (c *Crew) afterTurn(ctx context.Context, h *handOff, env *runEnv, run *store.AgentRun) (*llm.Message, *outcome) {
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
	case run.Turns == 1:
		// The first prompt alone is past the point (a big brief, a long note, a small window): a fresh
		// run would start just as full, so handing off would only loop. Say so and carry on.
		h.skipped = true
		c.system(ctx, env.task.ID, fmt.Sprintf("This run's first prompt already fills %d%% of the model's window (the brief, instructions and "+
			"tools), so it won't hand off: a fresh run would start as full. Trim the task's description or thread, or use a model with a "+
			"larger window, if it runs out.", run.ContextTokens*100/run.ContextWindow))
	default:
		h.asked = true
		return &llm.Message{Role: llm.RoleUser, Content: handOffRequest(run)}, nil
	}
	return nil, nil
}
