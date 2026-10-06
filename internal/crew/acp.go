package crew

import (
	"context"
	"fmt"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/j75689/cueflow/internal/acpdrive"
	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/store"
)

// mcpServerName is how CueFlow's tool server appears to external agents (tools become
// mcp__cueflow__<tool> in Claude Code).
const mcpServerName = acpdrive.ServerName

const acpPrompt = `You are working inside CueFlow as the agent described below. CueFlow is a self-hosted social media
operations console. Your tools for this task are provided by the MCP server named "cueflow"
(e.g. cueflow.create_post_draft, cueflow.add_comment, cueflow.ask_human, cueflow.complete_task).

Rules for this environment:
- Do all task work through the "cueflow" tools. Shell commands, file edits and local file access are
  not available and will be refused; do not try them.
- Web fetch/search tools may be available for research.
- You cannot publish to social media. Drafts go to a human review queue.
- Finish by calling cueflow.complete_task with a concise summary, or cueflow.ask_human if you need a decision.

`

type runtimeOptions interface {
	AuthMethod() string
	AllowWeb() bool
}

// crewSink writes an external agent's activity into the task thread.
type crewSink struct {
	c     *Crew
	task  *store.Task
	agent *store.Agent
}

func (s crewSink) Say(ctx context.Context, kind, content string, data any) {
	s.c.say(ctx, s.task.ID, s.agent, kind, content, data)
}

func (s crewSink) System(ctx context.Context, text string) { s.c.system(ctx, s.task.ID, text) }

// acpLoop runs one task through an external ACP agent. It mirrors loop(): same tools (served over
// MCP), same thread logging, same outcomes.
func (c *Crew) acpLoop(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun) *outcome {
	inst, conn, err := c.hub.Instance(ctx, *a.RuntimeConnectorID, false)
	if err != nil {
		return &outcome{kind: "error", text: "runtime connector: " + err.Error()}
	}
	defer hub.Close(inst)
	rt, ok := inst.(connector.Runtime)
	if !ok {
		return &outcome{kind: "error", text: fmt.Sprintf("connector %q is not an agent runtime", conn.Name)}
	}
	if pool, ok := inst.(runnerPool); ok {
		return c.runnerLoop(ctx, task, a, run, pool)
	}
	launch := rt.Launch()
	allowWeb := true
	authMethod := ""
	if o, ok := inst.(runtimeOptions); ok {
		allowWeb, authMethod = o.AllowWeb(), o.AuthMethod()
	}
	if launch.Timeout <= 0 {
		launch.Timeout = 20 * time.Minute
	}
	if c.MCPBaseURL == "" {
		return &outcome{kind: "error", text: "MCP base URL is not configured"}
	}

	env := &runEnv{c: c, task: task, agent: a}
	token, mrun, unregister := c.registerMCPRun(ctx, env)
	defer unregister()
	defer func() {
		if env.searcher != nil {
			hub.Close(env.searcher)
		}
	}()

	team, _ := c.store.ListAgents(ctx, task.OrgID)
	var memories []store.AgentMemory
	if a.Has("memory") {
		memories, _ = c.store.Memories(ctx, a.ID, "", 30)
	}
	prompt := acpPrompt + buildSystemPrompt(a, task, team, memories) + "\n\n---\n\n" + c.buildBrief(ctx, task)

	run.ConnectorID, run.Runtime = a.RuntimeConnectorID, "acp"
	hint := ""
	if h, ok := inst.(interface{ CredentialHint() string }); ok {
		hint = h.CredentialHint()
	}
	res := acpdrive.Run(ctx, acpdrive.Params{
		Launch: launch, AuthMethod: authMethod, AllowWeb: allowWeb,
		MCPURL: strings.TrimRight(c.MCPBaseURL, "/") + MCPPathPrefix + token,
		Model:  launch.Model, Prompt: prompt, Ended: mrun.done, Sink: crewSink{c: c, task: task, agent: a}, CredentialHint: hint,
	})
	return c.acpOutcome(ctx, run, env, res, launch.Timeout)
}

// acpOutcome records usage and maps an ACP run's result onto a task outcome. A terminal CueFlow
// tool call (complete_task, ask_human...) always wins.
func (c *Crew) acpOutcome(ctx context.Context, run *store.AgentRun, env *runEnv, res acpdrive.Result, timeout time.Duration) *outcome {
	if res.AgentName != "" {
		run.Model = res.AgentName
	}
	run.Turns = max(run.Turns, 1)
	run.ToolCalls += res.ToolCalls
	if u := res.Usage; u != nil {
		run.InputTokens += int64(u.InputTokens)
		run.OutputTokens += int64(u.OutputTokens)
	}
	if env.outcome != nil {
		return env.outcome
	}
	switch {
	case res.Canceled || ctx.Err() != nil:
		return &outcome{kind: "canceled"}
	case res.TimedOut:
		return &outcome{kind: "error", text: fmt.Sprintf("the external agent did not finish within %s", timeout)}
	case res.Err != nil:
		return &outcome{kind: "error", text: res.Err.Error()}
	}
	switch res.StopReason {
	case acp.StopReasonEndTurn:
		text := strings.TrimSpace(res.FinalText)
		if text == "" {
			text = "(finished without a summary)"
		}
		return &outcome{kind: "complete", text: text}
	case acp.StopReasonMaxTurnRequests, acp.StopReasonMaxTokens:
		return &outcome{kind: "limit"}
	case acp.StopReasonRefusal:
		return &outcome{kind: "error", text: "the external agent refused the request"}
	case acp.StopReasonCancelled:
		return &outcome{kind: "canceled"}
	}
	return &outcome{kind: "error", text: fmt.Sprintf("unexpected stop reason %q", res.StopReason)}
}
