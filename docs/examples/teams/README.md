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
| [`social-content.json`](social-content.json) | Researcher, Writer and Editor for post drafts (the content module); the Chief a new org starts with delegates to them |
