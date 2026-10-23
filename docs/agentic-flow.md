# Agentic Flow Design

> Traditional Chinese version: [docs/zh-TW/agentic-flow.md](zh-TW/agentic-flow.md)

> How CueFlow's agent team understands requests, divides work, executes and collaborates with humans.
> For the overall system see [architecture.md](architecture.md); for the APIs see the Agents / Tasks / Schedules
> sections of [api.md](api.md).

## 1. Goals and positioning

You tell CueFlow what you want in one sentence (like talking to a chat bot), and a team of specialised agents gets it done: they understand the request, break it down, hand parts to teammates, gather material, write drafts, set up recurring work, and report back. The whole process is laid out on a board, and you can step in at any time.

The design has three hard lines:

1. **Agents cannot publish.** Drafts always go to the review queue and only a human can approve them. This is the PRD's human-in-the-loop principle and the foundation of the security model.
2. **Everything is visible and can be taken over.** Every thought, tool call and result an agent produces is written to the task thread; humans can reply, reassign, drag cards and stop runs.
3. **Bounded.** Turns, subtasks, drafts, delegation depth and schedule frequency are all capped, preventing runaway loops and costs.

## 2. Core concepts

```mermaid
flowchart LR
    Human(("Human")) -->|"Command / Board / replies"| Task["Task<br/>a card on the board"]
    Task -->|"assigned to"| Agent["Agent (role)<br/>persona + capabilities + LLM + memory"]
    Agent -->|"Run"| Tools["Tools<br/>granted by capabilities"]
    Tools -->|"create_subtask"| Sub["Subtask"] -->|"assigned to"| Mate["Teammate agent"]
    Tools -->|"create_post_draft"| Draft["Post deliverable → review"]
    Tools -->|"schedule_task"| Sched["Schedule"] -->|"creates on a timer"| Task
    Agent -->|"thread"| Thread["Thread<br/>messages / tool calls / questions / results"]
    Thread --> Human
```

| Concept | Description |
|---|---|
| **Agent (role)** | Name, slug (for `@mentions` and delegation), avatar, role type, description (its introduction to teammates), instructions (persona and standing orders), capabilities, LLM / search / source connectors, language, platforms, turn limit |
| **Capability** | A permission group of tools. An agent only sees the tools within its capabilities |
| **Task** | A card on the board: title, description, status, priority, labels, assignee, parent task, result |
| **Thread** | The message stream under a task: human and agent messages, tool calls and results, system status, questions, the final result |
| **Run** | One working session of an agent on a task; a task can have several runs (e.g. resumed after waiting for subtasks) |
| **Memory** | Notes an agent keeps across tasks (preferences, facts, decisions); the latest 30 are included in every run |
| **Schedule** | A cron that creates a task for an agent |

## 3. Default team

On first start, when no agents exist, the system creates a single agent, the Chief (`internal/crew/defaults.go`). It can be edited in the UI, and you add the rest of the team yourself: by hand, or by importing a **team template**, a JSON file like a Grafana dashboard (Agents → *Import team*; *Export team* writes one from an org). The repository ships [`templates/social-content.json`](../templates/social-content.json), the researcher, writer and editor below, and [`templates/developer-team.json`](../templates/developer-team.json), a software team (product manager, tech lead, architect, engineer, reviewer, QA, tech writer) described in [templates/README.md](../templates/README.md):

| Role | slug | Capabilities | Responsibility |
|---|---|---|---|
| 🧭 Chief (created for you) | `chief` | planning, delegation, memory, research, sources, post_deliverables, deliverables | The front desk: understands requests, breaks them down, delegates to the agents you add (or does the work itself when none fits), sets up schedules, reports back; turn limit 16 |
| 🔎 Researcher (example template) | `researcher` | research, sources, memory | Gathers and verifies information, produces summaries with sources |
| ✍️ Writer (example template) | `writer` | post_deliverables, imaging, research, sources, memory | Writes drafts in each platform's style |
| 🧐 Editor (example template) | `editor` | post_deliverables, memory | Checks pending drafts for facts, tone and platform rules, and revises them (cannot approve) |

The default names and instructions are in English; the language agents write in follows each agent's `language` setting (default `en`).

## 4. Capabilities and tools

| Capability | Tool | Description |
|---|---|---|
| (all agents) | `add_comment` | Post a progress update to the thread |
| | `ask_human` | Ask a question and pause until a human replies (ends the run) |
| | `complete_task` | Report the result and finish (ends the run) |
| | `write_file` | Save text as a file of this task (creates or replaces it); files stay out of the conversation until read |
| | `read_file` | Read a file of this task or of a task above it, up to 30,000 characters from a character offset; says the file's length so the agent can page |
| | `list_files` | List the files this task can read: its own and those of the tasks above it (at most 100, then a count) |
| research | `web_search` | Search via SearXNG / Tavily |
| | `read_url` | Read a public web page (internal networks blocked, see §10) |
| sources | `search_materials` | Search material already collected from RSS / mail (the agent's Sources, or all when none are picked) |
| | `fetch_sources` | Pull the latest items from source connectors now |
| post_deliverables ("Drafting") | `create_post_draft` | Create a `post` deliverable for one platform (optional Threads `topic`, `link` card, `publish_at` time and `media_urls`, e.g. a video a tool rendered); it goes straight to `PENDING_REVIEW` |
| | `list_posts` | List post deliverables and their review status |
| | `revise_draft` | Revise a draft still in `DRAFT` / `PENDING_REVIEW` (audited as the agent) |
| imaging | `generate_image` | Generate an image with the first active image connector and attach it to an editable draft (default prompt: the draft's `image_prompt`). At most 2 per run, since each image costs money |
| deliverables | `create_deliverable` | File a deliverable of any registered kind (`document` by default) for human review; it goes straight to `PENDING_REVIEW` |
| | `list_deliverables` | List the org's deliverables and their status |
| | `get_deliverable` | Read one in full: title, status, review note and content, in pages of up to 30,000 characters by `offset` |
| | `revise_deliverable` | Revise one still in `DRAFT` / `PENDING_REVIEW` (audited as the agent) |
| delegation | `create_subtask` | Create a subtask assigned to a teammate (starts immediately) |
| | `list_agents` | List teammates available for delegation |
| | `wait_for_subtasks` | Pause until all subtasks finish, then be resumed (ends the run) |
| planning | `list_tasks` / `get_task` | Look at tasks on the board |
| | `create_task` | Create a standalone task (not a subtask) |
| | `schedule_task` / `list_schedules` / `pause_schedule` | Manage recurring tasks |
| memory | `remember` / `search_memory` | Save / search long-term memory |

**Tool permissions have three layers of defense:**

1. **Declaration**: the tool list sent to the LLM contains only tools within the agent's capabilities.
2. **Execution**: if the model calls a tool outside its list (e.g. it is tricked into calling a publisher's `create_post`), the runner returns an error result "Tool is not available to you" and records it in the thread.
3. **Publishing**: even if someone wired a publisher into an agent by mistake, agent runs carry the `guard.WithAgent` marker and `guardedPublisher` always refuses.

## 5. Task state machine

```mermaid
stateDiagram-v2
    [*] --> BACKLOG: created (unassigned)
    [*] --> TODO: created (assigned to an agent)
    BACKLOG --> TODO: assigned and dragged to To do
    TODO --> IN_PROGRESS: claimed by the runner (system only)
    IN_PROGRESS --> REVIEW: complete_task / plain-text reply (human-created task)
    IN_PROGRESS --> DONE: complete_task (agent-created subtask)
    IN_PROGRESS --> BLOCKED: wait_for_subtasks
    IN_PROGRESS --> WAITING_HUMAN: ask_human / turns exhausted / run failed / stopped
    IN_PROGRESS --> TODO: a human commented during the run → rerun after it ends
    BLOCKED --> TODO: all subtasks finished (automatic)
    WAITING_HUMAN --> TODO: human replies / presses Run
    REVIEW --> TODO: human replies asking for changes
    REVIEW --> DONE: human confirms
    DONE --> TODO: human replies with a follow-up
    TODO --> CANCELED
    BACKLOG --> CANCELED
```

| Status | Board column | Meaning |
|---|---|---|
| `BACKLOG` | Backlog | An idea, not started |
| `TODO` | To do | Ready; claimed automatically if assigned to an agent |
| `IN_PROGRESS` | In progress | An agent is working (set by the system only; humans cannot drag cards here) |
| `BLOCKED` | Waiting | Waiting for subtasks |
| `WAITING_HUMAN` | Waiting (needs you) | The agent asked a question, the run failed, or turns ran out |
| `REVIEW` | Review | The agent finished; a human checks the result |
| `DONE` / `CANCELED` | Done / hidden | Finished |

Unlike the deliverable state machine, task status is permissive for humans: cards can be dragged freely on the board (except into In progress). The strict part is when an agent finishes: the new status is applied only if the task is **still `IN_PROGRESS`** (`SetTaskStatusIf`). If a human drags the card away during a run, the agent's result is written to the thread but the card is not pulled back.

The board's **List** view (Board → *List*, or `/board/?view=list&status=<column>` with `backlog`, `todo`, `progress`, `waiting`, `review`, `done` or `canceled`) shows one column at a time as rows, with chips to switch between them; the search box and agent chips filter it as they do the board. Unlike the board, Done and Canceled also show cards finished more than 14 days ago, so old ones can be moved or deleted in bulk. The list shows 100 cards a page (*Previous* / *Next*, `&page=N` in the URL), and the search box also matches the description. Done lists the most recently finished first; Canceled lists newest first by when the card last went to Review or Done, or by its last update (a new thread message counts) if it never did. Select cards (or *Select all*, which ticks the cards on the page) and pick **Move to…** (Backlog, To do, Review, Done or Canceled) or **Delete**; each card is changed as if it were moved or deleted on its own (`POST /api/v1/tasks/bulk`, see [api.md](api.md#tasks-kanban-board)). The same rule applies: running cards can't be moved until they are stopped.

## 6. Lifecycle of a run

```mermaid
sequenceDiagram
    participant C as Crew Runner
    participant DB as PostgreSQL
    participant L as LLM Connector
    participant T as Tools
    participant B as Event Broker → UI

    C->>DB: ClaimTodo: TODO → IN_PROGRESS, lease = now+2m (SKIP LOCKED, by priority)
    C->>DB: StartAgentRun
    C->>C: start heartbeat (extend lease every 40s)
    C->>DB: load agent, teammates, memories, task, parent, subtasks, drafts, thread
    C->>C: build system prompt + task brief
    loop until a terminal tool / plain-text reply / turns exhausted
        C->>L: Chat(system, messages, tools)
        L-->>C: text + tool calls
        C->>DB: write text to the thread (kind=message)
        loop each tool call
            C->>DB: tool_call
            C->>T: execute (permission check)
            T-->>C: result or error
            C->>DB: tool_result (2,000 chars shown, up to 32,000 fed back to the model)
        end
        C->>B: task.message
    end
    C->>DB: FinishAgentRun (turns, tool calls, tokens)
    C->>DB: SetTaskStatusIf(IN_PROGRESS → new status)
    C->>B: task.updated
    C->>C: if a subtask finished → check whether the parent can resume
```

### 6.1 Stateless runs

Every run **rebuilds its context from scratch** instead of replaying the previous run's raw LLM messages:

- **System prompt**: general working rules + role description + delegation rules and team roster (with the delegation capability) + scheduling rules (with planning) + the role's instructions + the latest 30 memories + the current time.
- **Task brief (the first user message)**: task title, priority, labels, due date, description, parent task, all subtasks and their results, the previous run's hand-off note if it handed off (§6.3), the task's files (names and first lines, not contents), drafts already created, and the latest 60 thread messages (tool results compressed to 500 characters).

Benefits:
- Provider-agnostic: no need to persist each LLM vendor's native message format; a task can switch LLM or agent midway and carry on.
- Human replies in the thread, teammates' subtask results and system messages naturally become context for the next run.
- Within a single run the native format is preserved (e.g. Anthropic thinking blocks are passed back unchanged), so the tool loop stays faithful.

### 6.2 How runs end

| End reason | Written to the thread | New status | Run record |
|---|---|---|---|
| `complete_task` | `result` | Human-created task → `REVIEW`; agent-created subtask → `DONE` | SUCCEEDED |
| Plain-text reply (no tool call) | `result` | Same as above (treated as done; suits Q&A tasks) | SUCCEEDED |
| `ask_human` | `question` | `WAITING_HUMAN` | SUCCEEDED |
| `wait_for_subtasks` | system message | `BLOCKED` (straight back to `TODO` if the subtasks have in fact all finished) | SUCCEEDED |
| Turns exhausted | system message (reply to continue or raise the limit) | `WAITING_HUMAN` | FAILED |
| Context reached the hand-off point (§6.3) | the hand-off note (marked) and a system message | `WAITING_HUMAN` | SUCCEEDED |
| Context window exceeded | system message (what to shrink, or reply to continue in a fresh run) | `WAITING_HUMAN` | FAILED |
| LLM / configuration error | system message (with the error) | `WAITING_HUMAN` | FAILED |
| Stopped by a human (Stop, moving or reassigning the card, on any instance) | "Stopped." | `WAITING_HUMAN` | CANCELED |
| Built-in run used its cloud delivery's time (below) | system message | `TODO`, continuing in a fresh run (up to 5 in a row, then `WAITING_HUMAN`) | SUCCEEDED (`continued`) |
| Context ended any other way (the server stopped, a delivery's request was cut off) | system message (interrupted; reply to continue) | `WAITING_HUMAN` | FAILED |
| Process restart, lease expired | system message (explaining the interruption) | `WAITING_HUMAN` | FAILED |

**Cloud deliveries** (`CUEFLOW_QUEUE=cloudtasks`): a run works inside one Cloud Tasks delivery, an HTTP
request that Cloud Tasks and Cloud Run cut off after 30 minutes. A delivery claims work only in its first
20 minutes. A built-in run starts no new turn after 25 minutes and continues in a fresh run that the next
delivery picks up (it starts from the brief and thread, like any run). A run on a runner is handed over at
28 minutes: the runner's agent keeps working, and the next delivery keeps watching the same job, so a long
QA or build pass is never killed by the request limit. An ACP agent on the CueFlow host itself can't be
handed over; such a run past 30 minutes is interrupted.

**A human comments during a run**: the runner does not interrupt the in-flight LLM call. When the run ends it checks whether any human message arrived after the run started. If so, it does not apply the completion status; it moves the task back to `TODO` and reruns immediately so the new comment is handled (except when the run was stopped or failed, or a context overflow on its first turn, which a rerun would only repeat).

**Alerts**: when a run leaves the task waiting for a human (`REVIEW` or `WAITING_HUMAN`: ready for review, a
question, an approval, a hand-off that stopped, a failed run or one cut off by a restart), CueFlow sends one
message through the notifier connector the org picked (**Manage orgs → Alerts**; Telegram or email). It
names the org, task, agent and reason, with a link to the task (built from `CUEFLOW_PUBLIC_URL`). A run
stopped by a human, a task blocked on subtasks, an agent-created subtask that ends `DONE` and a run that a
mid-run comment re-queues send none. Sending is best effort (15 s timeout, errors logged); disabling the
notifier connector mutes it.

### 6.3 Hand-off when the context fills up

A run's history is append-only (nothing sent to the model is cut), so a long run grows until it nears the model's
window. When the latest prompt reaches the hand-off point, 80% of a known window by default
(`CUEFLOW_CONTEXT_HANDOFF`):

1. The crew adds one message at the end of the conversation asking the agent to write a hand-off note with
   `write_file` as `handoff.md` (what it did, the facts and decisions it confirmed, what is left, which task files to
   read first) and stop. Earlier turns are sent as they were.
2. Once the note is saved, the run ends: the thread gets a pointer to it (marked *hand-off*, opening the file) and
   the task waits for the human. Only a note written after the request counts. An agent that answers in text instead has written the note in that text; one that keeps calling
   tools gets one more turn, then the run ends saying no note was written. The request still comes when the point is
   reached on the run's last allowed turn: the note's turns go past the limit.
3. The human reviews it and replies, with corrections or just to continue. The fresh run's brief starts with a
   *Hand-off from the previous run* section quoting the note (`handoff.md` holds all of it), followed by the thread
   as usual. Nothing of the old conversation is replayed. The note is quoted only in the run right after the
   hand-off, not in every later brief.

**Without waiting.** An agent with **Auto hand-off** on (`auto_handoff`, off by default; built-in runtime only) skips
step 3 when the note was saved: the task goes straight back to `TODO` and the fresh run starts from the note, which is
still posted to the thread and quoted in its brief. At most 3 hand-offs in a row continue this way; the next waits for
the human, saying so. The count is read from the task's recorded runs: it covers the latest hand-offs that went on
by themselves, so a hand-off that waited (whatever moved the task on afterwards was a human: a reply, dragging the
card, Run now), a run that ended any other way, or a human message starts it again. A hand-off without a note, where
the agent ignored the request, always waits. Agents on an external runtime can't have the setting.

A run hands off only once it has grown by a tenth of the window since its first prompt. If it reaches the point
sooner (a large brief or note, a small window), a fresh run would start almost as full and hand off again, so the
run says so once and carries on instead. Without a known window there is no
hand-off; the run ends with a context-overflow message if it runs out.

## 7. Delegation and resumption

```mermaid
sequenceDiagram
    actor H as Human
    participant Chief as 🧭 Chief (#1)
    participant R as 🔎 Researcher (#2)
    participant W as ✍️ Writer (#3)

    H->>Chief: "Write a Threads post about MCP"
    Note over Chief: Run 1
    Chief->>R: create_subtask #2 "Research the latest MCP news"
    Chief->>Chief: wait_for_subtasks → BLOCKED
    Note over R: #2 claimed automatically
    R->>R: search_materials / web_search / read_url
    R-->>Chief: complete_task → #2 DONE (agent-created subtasks need no human check)
    Note over Chief: all subtasks finished → #1 back to TODO<br/>subtask results posted to the thread
    Note over Chief: Run 2
    Chief->>W: create_subtask #3 "Draft a Threads post (with the research)"
    Chief->>Chief: wait_for_subtasks → BLOCKED
    W->>W: create_post_draft → draft goes to the review queue
    W-->>Chief: complete_task → #3 DONE
    Note over Chief: Run 3
    Chief-->>H: complete_task → #1 REVIEW "Draft submitted, approve it under Deliverables"
```

Rules:

- A subtask is `TODO` and assigned the moment it is created, and the runner claims it immediately; up to 3 tasks run in parallel (`CUEFLOW_AGENT_CONCURRENCY`).
- After the parent calls `wait_for_subtasks` it is `BLOCKED`. Whenever any subtask enters `DONE` / `REVIEW` / `CANCELED` (whether an agent finished it or a human dragged it on the board), the system checks whether all subtasks are finished; if so, it posts a summary of the subtask results to the parent's thread and moves the parent back to `TODO`.
- If a subtask is stuck in `WAITING_HUMAN` (e.g. the researcher has a question), the parent stays `BLOCKED` until a human deals with it.
- Delegation is at most 3 levels deep (root task → subtask → sub-subtask); a sub-subtask cannot delegate further.

## 8. Human-agent collaboration

| Human action | System behavior |
|---|---|
| Send a request in Command | Creates a `TODO` task assigned to the chosen agent (Chief by default) and opens the task page |
| Add a card on the board and assign it | Starts in `TODO` if assigned to an agent, otherwise `BACKLOG` |
| Drag a card to To do | Starts automatically if an agent is assigned |
| Move or delete selected cards in the List view | Moving assigned cards to To do asks first and says how many agents will start; Delete asks first and says how many subtasks go with the cards and how many running agents are stopped. Cards that could not be changed stay selected, with the reason |
| Comment in the thread | If the task has an agent and is not in `BACKLOG` / `CANCELED` → moves to `TODO` and the agent continues after reading the comment; if a run is in progress, it reruns automatically when the run ends |
| Start a comment with `@slug` | First hands the task to that agent (a running run is stopped), then behaves as above |
| Press Stop or drag a running card away | Cancels the run's context, aborting the LLM call; the task stays in `WAITING_HUMAN` or the column you dropped it in |
| Reassign to another agent | A running run is stopped |
| Review drafts | Agent drafts are like any other draft: they reach the publisher only after approval on the Deliverables page |

## 9. Schedules

- Created by humans on the Schedules page, or by agents with the planning capability via `schedule_task` (marked `created_by: agent`; the UI shows "created by agent").
- Each firing creates a new task: the title gets the date, a `scheduled` label is added, it is assigned to the configured agent, and the runner is woken immediately.
- Validation: a valid 5-field cron or a descriptor such as `@daily`; consecutive firings must be ≥ 5 minutes apart (checked over the next 5 firings), so an agent cannot create an every-minute schedule. On a validation error the error is returned to the agent so it can correct itself.
- `Run now` creates one task immediately.

### Publishing defaults

A schedule (or a task) can name a publisher and accounts (`publisher_connector_id`,
`publish_accounts`). Tasks created by the schedule copy them, subtasks inherit them from their parent,
and every draft an agent writes for such a task starts with that publisher and the accounts that are on
the draft's platform. When accounts are picked, the brief tells the agent which platforms they cover
and `create_post_draft` refuses drafts for any other platform (e.g. no Instagram draft when only a
Threads account is picked). Drafts still wait for a human on the Deliverables page; the defaults only save picking the
target each time.

## 10. Guardrails and limits

| Item | Limit / behavior | Where |
|---|---|---|
| Turns per run | The role's `max_turns` (default 12, Chief 16, max 40) | `store/agents.go` |
| How full a run's context may get | 80% of the model's window by default (`CUEFLOW_CONTEXT_HANDOFF`); past it the run writes a hand-off note and waits for the human (§6.3), getting up to 2 turns past its turn limit for the note. The task page shows each run's latest prompt as "Context 182K / 1M (18%)", coloured from there, and without a percentage when the window is unknown | `config`, `crew/handoff.go` |
| The hand-off note in the next brief | 12,000 characters (`handoff.md` has the rest) | `crew/handoff.go` |
| Automatic hand-offs in a row (Auto hand-off on) | 3; the next waits for the human. A hand-off that waited, a run that ends otherwise, or a human message resets the count | `crew/handoff.go` |
| Subtasks + standalone tasks created per run | 8 | `crew/tools.go` |
| Drafts created per run | 10 | `crew/tools.go` |
| Delegation depth | 3 levels | `crew/tools.go` |
| A single tool result fed back to the model | Over 8,000 characters (`saveResultOver`), a text result of a tool that returns outside content (MCP tools, approved calls included; `read_url`; `search_materials`) is saved whole as a task file `results/<tool>-<n>.txt` (first line: the tool and its arguments; an existing file is never overwritten), and the model gets the file name, the total length and the first 2,000 characters, with the offset to read on from. Tools that report CueFlow's own state (tasks, agents, memories, subtask results, files) stay inline, as do errors, non-text results, results over 8 MB and a result whose save fails. Inline results are cut at 32,000 characters with a note saying how many were cut (2,000 when shown in the thread) | `crew/files.go`, `crew/tools.go` |
| A run's history | Append-only, on every connector: nothing already sent to the model is cut or rewritten (Claude's thinking blocks are bound to it), and there is no budget below the model's own window. Claude connectors cache it (Prompt caching, on by default; if the endpoint rejects `cache_control`, the connector retries without it and logs a warning); other connectors re-send the whole history each turn, cached only if the provider does so on its own. A run that outgrows the window stops with a message saying so; a reply starts a fresh run from the task and its thread | `crew/crew.go`, `connectors/anthropic` |
| Thread messages in the task brief | Latest 60 | `crew/prompt.go` |
| Files in the task brief | Up to 30 (name, size, which task, start of the first line; never the contents), then a count; `list_files` shows up to 100 | `crew/files.go` |
| One `read_file` call | 30,000 characters | `crew/files.go` |
| One task file | 8 MB | `store/task_files.go` |
| Memories included | Latest 30, 2,000 characters each | `crew/crew.go`, `store/agents.go` |
| Schedule frequency | ≥ 5 minutes | `scheduler.ValidateSchedule` |
| Parallel tasks | 3 (`CUEFLOW_AGENT_CONCURRENCY`) | `config` |
| One model call | 10 minutes; every LLM connector streams the answer, so a long one isn't cut off by the timeout other connector requests have (`CUEFLOW_CONNECTOR_HTTP_TIMEOUT`, default 90s), nor by a proxy's idle timeout. Claude connectors wait up to 2 minutes for the answer to start and try again when a stream breaks off or reports an overload; an OpenAI-compatible server that doesn't stream is read the old way, within the 10 minutes | `llm/llm.go`, `connectors/anthropic`, `connectors/openai` |
| Run lease | 2 minutes, renewed every 40 seconds; expired → `WAITING_HUMAN`. Each claim mints the id of the run that works it (`tasks.run_id`), and a run renews and finishes the task only under its own claim: a run that stalled past its lease while the task was claimed again stops at its next tool call or heartbeat, whichever comes first (a tool call is refused before it runs), and leaves the task to the new run | `crew/crew.go`, `store.Claim` |
| `read_url` | http(s) only; blocks loopback / private / link-local / CGNAT / multicast at dial time; at most 5 redirects; 2 MB, 8,000 characters, 30 seconds; bypasses the system proxy (`CUEFLOW_AGENT_ALLOW_PRIVATE_URLS=true` relaxes this) | `crew/fetch.go` |
| Publishing | Impossible for any agent, see §4 | `guard`, `connector` |

## 11. Prompt design

The system prompt is assembled by code in a fixed order:

1. **Identity and working rules**: who you are, you are working on task #N, make progress autonomously, ask humans only when necessary, you cannot publish, facts need sources, report progress on long tasks, finish with a `complete_task` summary, communicate in the configured language.
2. **Delegation rules and team roster** (with the delegation capability): each teammate's slug, role, description and capabilities; guidance to "hand work to the specialist, but don't open subtasks for trivial steps" and to check subtask results before completing.
3. **Scheduling rules** (with the planning capability): use a schedule instead of doing recurring requests once, and infer a suitable timezone.
4. **Role instructions**: the persona and standing orders the user edits in the UI.
5. **Memories** and the **current time**.

All differences between roles come from "capabilities (which tools they see)" plus "instructions (how they work)", so adding a role (e.g. a data analyst or a customer-reply agent) needs no code changes.

## 11a. External agents (ACP runtime)

An agent can delegate its runs to an external coding agent instead of CueFlow's built-in LLM loop. The
agent's **Runtime** points at an *External agent (ACP)* connector (`acp_agent`, kind `runtime`) that describes
how to launch it: presets for Claude Code (`claude-agent-acp`), Codex (`codex-acp`), Gemini CLI
(`gemini --acp`), OpenCode (`opencode acp`), Goose (`goose acp`), or a custom command; credentials (API key,
Claude Code OAuth token from `claude setup-token`) are stored encrypted on the connector and passed as
environment variables.

```mermaid
sequenceDiagram
    participant C as Crew runner
    participant A as External agent (ACP, stdio)
    participant M as CueFlow MCP endpoint (/mcp/run/{token})
    C->>C: register a one-time MCP endpoint scoped to this task and agent
    C->>A: spawn in an empty temp dir (own process group)
    C->>A: initialize (no client fs / terminal)
    C->>A: session/new {cwd, mcpServers: [cueflow → HTTP, or stdio via `cueflow mcp-bridge`]}
    C->>A: session/prompt (operating rules + agent prompt + task brief)
    A-->>C: session/update (message chunks, tool calls) → written to the task thread
    A->>C: session/request_permission → allow CueFlow tools, think, web fetch, refuse the rest
    A->>M: tools/call create_post_draft, add_comment, complete_task ...
    M->>M: same tool code and checks as the built-in loop
    C->>A: session/cancel once a terminal tool ran (or on timeout / Stop)
    C->>C: unregister the endpoint, kill the process group, remove the temp dir
```

- **Same tools, same guarantees.** The MCP endpoint serves exactly the tools of the agent's capabilities,
  executed by the same code as the built-in loop (thread logging, limits, drafts to review). There is still
  no publishing tool.
- **Permission policy.** CueFlow's MCP tools, `think` and (optionally) `fetch` are allowed; `execute`, `edit`,
  `read`, `delete`, `move`, `search` and anything else are refused and noted in the thread. The client also
  declares no file system or terminal capability.
- **Stateless runs.** Each run is a fresh ACP session seeded with the task brief, exactly like the built-in
  loop; no session IDs to manage.
- **Outcome mapping.** A terminal tool decides the outcome; otherwise `end_turn` → completed with the agent's
  last message, `max_turn_requests`/`max_tokens` → turn limit, `refusal` → failed, timeout → failed.
- **Tokens.** Usage is recorded when the agent reports it in the prompt result.

## 11b. External tools (MCP servers)

An operator can add an **MCP server** connector (kind `tools`: Streamable HTTP, SSE or stdio) and
attach it to agents (`tool_connector_ids`). Its tools appear to those agents as `<server>__<tool>`,
next to the built-in tools, for both the built-in loop and ACP runtimes.

Each tool has a permission, set on the connector page:

| Permission | Effect |
|---|---|
| `allow` | the agent calls it directly |
| `ask` | the call is recorded (`tool_approvals`), shown in the task thread as an approval card, and the run ends with the task in `WAITING_HUMAN`. **Approve & run** executes the call server-side and posts the result to the thread; **Reject** posts the refusal (with an optional note). Once no approval is pending, the task goes back to `TODO` and the agent resumes with the outcome in its brief |
| `deny` | the agent never sees it |

Each connector has a call timeout (`timeout_minutes`, default 2); raise it for slow tools such as video
rendering. Tools without a stored permission use the default: `allow` when the server marks them read-only (or the
name reads like a lookup: get, list, search...), otherwise `ask`. Tools that look like they publish,
message people, delete data or move money are flagged *risky*; allowing one asks for confirmation. A
server that adds tools later gets the defaults for them, so no new tool runs unattended unless it is
read-only. Permissions are the operator's call: an allowed tool runs outside CueFlow's review.

## 12. End-to-end example

In Command, the user types: "Every morning at 8, summarize AI news and draft a Threads post".

1. Task #1 is created, assigned to Chief, status `TODO`.
2. **Chief, run 1**: recognises a recurring request. If the time were unclear it would `ask_human`; here 8 a.m. is given, so it calls `schedule_task` directly (`0 8 * * *`, `Asia/Taipei`, assigned to Writer, with a description asking it to gather the previous day's AI news and write a draft), uses `remember` to note "the user wants AI news drafts every day at 8", and finally `complete_task` reports what it set up. #1 → `REVIEW`.
3. The next day at 08:00, the schedule creates task #2 "Summarize AI news and draft a Threads post · 2026-10-05", assigned to Writer, which starts automatically.
4. **Writer, run**: `fetch_sources` → `search_materials` → `web_search` / `read_url` to verify when needed → `create_post_draft` (Threads; anything over 500 characters is split into a thread automatically) → `complete_task` reports the draft ID. #2 → `REVIEW`.
5. You see the draft on the Deliverables page, edit and approve it → the deliverables worker hands it to the publisher.
6. To adjust the style, comment on #2 "make it punchier and end with a question"; Writer reruns and uses `revise_draft` on the not-yet-approved draft. Or use `@editor please check this` to hand the task to Editor.

## 13. Known limitations and future work

- **Cost control**: currently controlled by turn and count caps; there is no token- or money-based budget yet. `agent_runs` already records token usage and can serve as the basis for a daily budget.
- **Long-task compaction**: context is truncated to "the latest 60 messages", so very long tasks may lose early details; agents could write stage summaries instead.
- **Structured output**: subtask results are free text; JSON-schema output could be added for data pipelines.
- **External MCP tools**: `internal/mcpx` already classifies tools (blocking those with side effects); agents could later use read-only third-party MCP tools.
- **Single instance**: cron is not suited to multi-instance deployment (see architecture.md §8).
