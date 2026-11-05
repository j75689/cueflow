package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/j75689/cueflow/internal/config"
	"github.com/j75689/cueflow/internal/content"
	contentpg "github.com/j75689/cueflow/internal/content/pg"
	contentsqlite "github.com/j75689/cueflow/internal/content/sqlite"
	"github.com/j75689/cueflow/internal/deliverable"
	deliverablepg "github.com/j75689/cueflow/internal/deliverable/pg"
	deliverablesqlite "github.com/j75689/cueflow/internal/deliverable/sqlite"
	"github.com/j75689/cueflow/internal/secret"
	"github.com/j75689/cueflow/internal/store"
	storepg "github.com/j75689/cueflow/internal/store/pg"
	storesqlite "github.com/j75689/cueflow/internal/store/sqlite"
)

// openBackend opens the database DATABASE_URL names (PostgreSQL, or a SQLite file in single mode),
// migrates it, and returns the core store with the deliverable and content stores over it. Media an
// unfinished deliverable uses can't be deleted (whether or not the module is mounted: its rows stay).
// close closes the database.
func openBackend(ctx context.Context, cfg *config.Config) (store.Backend, deliverable.Store, content.Store, func(), error) {
	box, err := secret.New(cfg.SecretKey, cfg.PreviousSecretKeys...)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if cfg.SQLite() {
		st, err := storesqlite.Open(ctx, cfg.DatabaseURL, box)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if err := storesqlite.Migrate(ctx, st); err != nil {
			st.Close()
			return nil, nil, nil, nil, fmt.Errorf("migrate: %w", err)
		}
		ds := deliverablesqlite.New(st)
		st.SetMediaUse(ds)
		return st, ds, contentsqlite.New(st), st.Close, nil
	}
	pool, err := openDB(ctx, cfg, cfg.DBMaxConns)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if err := migrateDB(ctx, cfg, pool); err != nil {
		pool.Close()
		return nil, nil, nil, nil, fmt.Errorf("migrate: %w", err)
	}
	st := storepg.New(pool, box)
	ds := deliverablepg.New(st)
	st.SetMediaUse(ds)
	return st, ds, contentpg.New(st), st.Close, nil
}

// openDB opens the main pool: for a transaction-mode pooler when a session URL is set.
func openDB(ctx context.Context, cfg *config.Config, maxConns int) (*pgxpool.Pool, error) {
	return storepg.OpenWith(ctx, cfg.DatabaseURL, storepg.Options{MaxConns: maxConns, TxPooler: cfg.DatabaseSessionURL != ""})
}

// migrateDB migrates through the session URL when there is one (the migration lock needs a session).
func migrateDB(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool) error {
	if cfg.DatabaseSessionURL != "" {
		return storepg.MigrateSession(ctx, cfg.DatabaseSessionURL)
	}
	return storepg.Migrate(ctx, pool)
}

// waitForDB waits for PostgreSQL (it may still be starting under docker compose). A SQLite file needs
// no wait.
func waitForDB(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	if cfg.SQLite() {
		return nil
	}
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		pool, err := openDB(ctx, cfg, 1)
		if err == nil {
			pool.Close()
			return nil
		}
		lastErr = err
		logger.Warn("waiting for database", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("database unavailable: %w", lastErr)
}
