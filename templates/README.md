# Team templates

A team template is a JSON file that describes a set of agents (name, slug, role, capabilities,
instructions, platforms, language, turn limit), the way a Grafana dashboard is a JSON file: nothing
in CueFlow is a template, you keep them as files and import them where you need them.

- **Import**: Agents → *Import team* (paste or pick a file), or `POST /api/v1/agents/template` with
  the file as the body. Roles whose slug the org already has are skipped, so importing twice, or over
  a hand-made team, only adds what is missing. Every agent it creates is an ordinary agent you edit
  afterwards; connectors are picked per install, not stored in the file.
- **Export**: Agents → *Export team*, or `GET /api/v1/agents/template`: the current org's agents as a
  template.
- Capabilities, roles and platforms are checked on import against what this server has, so a
  template that names a capability of a module that is not mounted fails with a clear error.

| File | Team |
|---|---|
| [`social-content.json`](social-content.json) | Researcher, Writer and Editor for post drafts (the [content module](../docs/modules.md#content)); the Chief a new org starts with delegates to them |
| [`developer-team.json`](developer-team.json) | Tech Lead, Architect, Engineer, Reviewer, QA and Tech Writer for software work; see below |

## Developer team

The roles follow the engineering division of [agency-agents](https://github.com/msitarzewski/agency-agents),
cut down to what runs on a board with a human merging: a change goes design → pull request → review →
verification → docs. Ask the Chief for a change (or assign the task to `@tech-lead` directly) and the Tech Lead
runs the stages as subtasks, one at a time, and reports the pull request back. Nothing merges: the pull
request is the deliverable, and the design, review and verification reports land under Deliverables as
documents.

What each role needs:

- **Engineer, Reviewer, QA, Tech Writer** touch code, so give them a *Runtime* that allows the shell: a
  [runner](../docs/external-agents.md#run-agents-on-your-own-machine-runners) started with `--allow-shell`, or
  the [runner container](../deploy/runner/README.md), which also gives the agent its own GitHub token and Claude
  login. On the built-in LLM loop (no runtime) they only read public pages and write documents: the Engineer
  then delivers the change as a diff in a document, and the Reviewer and QA review what they can read.
- **Tech Lead and Architect** work on the built-in loop; the Architect reads code better with a runner too.
- A repository to work in: name it in the task, or tell the Tech Lead once and it remembers.
- Protect the default branch of that repository (require a pull request). The agents never merge, but the
  token they hold decides what they *could* do.

Two engineers working in parallel: import the file, then duplicate the Engineer in the dashboard with another
slug (`frontend`, `backend`) and a line in its instructions about its half; the Tech Lead sees both in its team
list.
