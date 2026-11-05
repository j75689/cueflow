// Package domain holds the errors every layer of CueFlow shares. The content workflow's own model
// (posts, their state machine, platform rules) lives in internal/content.
package domain

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrVersionConflict = errors.New("version conflict")
	// ErrConflict means a write would duplicate a unique key (a slug, a name) another row holds.
	ErrConflict = errors.New("conflict")
	// ErrReferenced means a delete is refused because other rows still refer to the row.
	ErrReferenced = errors.New("still referenced")
)

// TransitionError is returned when a requested status change violates a state machine.
type TransitionError struct {
	From, To string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("invalid transition %s -> %s", e.From, e.To)
}
