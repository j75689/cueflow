// Package version holds the CueFlow version. It imports nothing, so every package (the API, the crew's
// MCP servers, the ACP client) can report the same number without an import cycle.
package version

// Version is bumped by the release pull request (release-please); see CONTRIBUTING.md.
const Version = "1.5.0" // x-release-please-version
