package sqlite

import (
	"fmt"

	"github.com/j75689/cueflow/internal/content"
	"github.com/j75689/cueflow/internal/store"
	storesqlite "github.com/j75689/cueflow/internal/store/sqlite"
)

// Store runs the content workflow's queries on the core store's database. Methods of the core
// store are reachable through the embedded pointer.
type Store struct {
	*storesqlite.Store
}

var _ content.Store = (*Store)(nil)

func New(s *storesqlite.Store) *Store { return &Store{Store: s} }

// init serves content.NewStore for the SQLite store and hands any other backend to the constructor
// registered before it.
func init() {
	next := content.NewStore
	content.NewStore = func(s store.Backend) content.Store {
		if core, ok := s.(*storesqlite.Store); ok {
			return New(core)
		}
		if next == nil {
			panic(fmt.Sprintf("no content store for %T", s))
		}
		return next(s)
	}
}
