// Package content is the social content module: the "post" deliverable kind (a post for Threads,
// Instagram, X... with the platform rules as its validator and publishing through a publisher
// connector as its action), the material source connectors collect, and the agents' tools for
// drafting posts and fetching material. It is mounted on the core as a module (see Module).
//
// Posts used to have a table and a worker of their own; they are deliverables now. The old
// posts table is kept as an archive and is not read by this code.
package content

import "errors"

// Post is the shape the platform rules are written against; the post kind's payload is viewed as
// one for validation.
type Post struct {
	Content     string
	ThreadParts []string
	Hashtags    []string
	Platforms   []string
	MediaURLs   []string
	Accounts    []string
	Topic       string
	LinkURL     string
}

// Parts returns the ordered list of posts to publish: thread parts when present, else content.
func (p *Post) Parts() []string {
	if len(p.ThreadParts) > 0 {
		return p.ThreadParts
	}
	return []string{p.Content}
}

var errNoPlatform = errors.New("at least one platform is required")
