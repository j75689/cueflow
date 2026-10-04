// Package all registers every built-in connector. Import it for side effects.
//
// To add an integration: create internal/connectors/<name>, call connector.Register in its init(),
// and add a blank import here.
package all

import (
	_ "github.com/j75689/cueflow/internal/connectors/anthropic"
	_ "github.com/j75689/cueflow/internal/connectors/imap"
	_ "github.com/j75689/cueflow/internal/connectors/openai"
	_ "github.com/j75689/cueflow/internal/connectors/postiz"
	_ "github.com/j75689/cueflow/internal/connectors/rss"
	_ "github.com/j75689/cueflow/internal/connectors/searxng"
	_ "github.com/j75689/cueflow/internal/connectors/smtp"
	_ "github.com/j75689/cueflow/internal/connectors/tavily"
)
