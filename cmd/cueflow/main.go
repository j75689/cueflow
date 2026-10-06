// Command cueflow runs the CueFlow agent ops console: API + dashboard, scheduler and publish worker.
//
//	cueflow            start the server (default)
//	cueflow migrate    apply database migrations and exit
//	cueflow genkey     print a new random CUEFLOW_SECRET_KEY
//	cueflow healthcheck exit 0 if the local server answers /api/v1/health (for container healthchecks)
//	cueflow mcp-bridge URL  serve the MCP server at URL on stdio (for agents that only launch stdio MCP servers)
//	cueflow rotate-key [--dry-run]  re-encrypt connector secrets with CUEFLOW_SECRET_KEY
//	                    (reading with it or CUEFLOW_SECRET_KEY_PREVIOUS)
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/blob"
	"github.com/j75689/cueflow/internal/config"
	_ "github.com/j75689/cueflow/internal/connectors/all"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/db"
	"github.com/j75689/cueflow/internal/dispatch"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/mcpx"
	"github.com/j75689/cueflow/internal/pipeline"
	"github.com/j75689/cueflow/internal/runner"
	"github.com/j75689/cueflow/internal/scheduler"
	"github.com/j75689/cueflow/internal/secret"
	"github.com/j75689/cueflow/internal/store"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "migrate":
		err = migrate()
	case "genkey":
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		fmt.Println(base64.StdEncoding.EncodeToString(b))
	case "healthcheck":
		err = healthcheck()
	case "rotate-key":
		err = rotateKey(len(os.Args) > 2 && os.Args[2] == "--dry-run")
	case "runner":
		err = runner.Main(os.Args[2:], api.Version)
	case "media":
		err = mediaCmd(os.Args[2:])
	case "mcp-bridge":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: cueflow mcp-bridge URL")
			os.Exit(2)
		}
		err = mcpx.Bridge(context.Background(), os.Args[2])
	case "version", "-v", "--version":
		fmt.Println("cueflow", api.Version)
	default:
		fmt.Fprintf(os.Stderr, "usage: cueflow [serve|migrate|genkey|healthcheck|rotate-key|media|runner|mcp-bridge|version]\n")
		os.Exit(2)
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func migrate() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return db.Migrate(ctx, pool)
}

// dropEmptyCredentialEnv unsets credential variables that are present but empty. docker compose
// passes `VAR: ${VAR:-}` through as an empty string, and some SDKs treat a set-but-empty variable
// as a choice (an empty ANTHROPIC_API_KEY would shadow an `ant auth login` profile).
func dropEmptyCredentialEnv() {
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if v == "" && (strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "AWS_") || strings.HasPrefix(k, "GOOGLE_")) {
			_ = os.Unsetenv(k)
		}
	}
}

func serve() error {
	dropEmptyCredentialEnv()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := connectWithRetry(ctx, cfg.DatabaseURL, logger)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	box, err := secret.New(cfg.SecretKey, cfg.PreviousSecretKeys...)
	if err != nil {
		return err
	}
	if box.HasPrevious() {
		logger.Info("secret key rotation in progress: previous keys accepted for reading; run `cueflow rotate-key`, then remove CUEFLOW_SECRET_KEY_PREVIOUS")
	}
	st := store.New(pool, box)
	blobs, err := blob.Parse(cfg.MediaStorage)
	if err != nil {
		return err
	}
	st.SetBlobs(blobs)
	if err := st.FailStaleRuns(ctx); err != nil {
		logger.Warn("clean stale runs", "err", err)
	}
	broker := events.NewBroker()
	broker.Attach(pool, logger)
	go broker.Run(ctx)
	h := hub.New(st, logger)
	runner := pipeline.NewRunner(ctx, st, h, broker, logger, cfg.PublicURL)
	worker := dispatch.NewWorker(st, h, broker, logger, cfg.DispatchEvery, cfg.DispatchLease, cfg.PublishTimeout)
	worker.MediaBaseURL = cfg.MediaBaseURL
	sched := scheduler.New(st, runner, h, logger, cfg.DigestCron, cfg.PublicURL)

	team := crew.New(st, h, broker, logger, cfg.AgentAllowPrivateURLs)
	team.Concurrency = cfg.AgentConcurrency
	team.MCPBaseURL = localBaseURL(cfg.Addr)
	team.ValidateSchedule = scheduler.ValidateSchedule
	team.OnSchedulesChanged = func(ctx context.Context) { _ = sched.Reload(ctx) }
	sched.OnTaskSpawned = func(ctx context.Context, t *store.Task) {
		broker.Publish("task.updated", map[string]any{"id": t.ID, "status": t.Status})
		team.Wake()
	}
	if err := st.FailStaleAgentRuns(ctx); err != nil {
		logger.Warn("clean stale agent runs", "err", err)
	}
	if err := st.CancelOrphanRunnerJobs(ctx); err != nil {
		logger.Warn("clean stale runner jobs", "err", err)
	}
	if org, err := st.DefaultOrg(ctx); err != nil {
		logger.Warn("seed default agents", "err", err)
	} else if n, err := crew.SeedDefaults(ctx, st, org.ID); err != nil {
		logger.Warn("seed default agents", "err", err)
	} else if n > 0 {
		logger.Info("created the default agent team", "agents", n)
	}
	if !cfg.DisableScheduler {
		if err := sched.Start(ctx); err != nil {
			return err
		}
	}
	go worker.Run(ctx)
	go team.Run(ctx)

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: (&api.Server{
			Store: st, Hub: h, Broker: broker, Runner: runner, Scheduler: sched, Dispatcher: worker, Crew: team,
			Logger: logger, AdminToken: cfg.AdminToken, MCPToken: cfg.MCPToken, PublicURL: cfg.PublicURL,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		logger.Info("CueFlow listening", "addr", "http://"+cfg.Addr, "version", api.Version, "auth", cfg.AdminToken != "", "mcp", cfg.MCPToken != "")
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	runner.Wait()
	team.Wait()
	return nil
}

// connectWithRetry waits for PostgreSQL (it may still be starting under docker compose).
func connectWithRetry(ctx context.Context, url string, logger *slog.Logger) (*pgxpool.Pool, error) {
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		pool, err := db.Open(ctx, url)
		if err == nil {
			return pool, nil
		}
		lastErr = err
		logger.Warn("waiting for database", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, fmt.Errorf("database unavailable: %w", lastErr)
}

func healthcheck() error {
	addr := os.Getenv("CUEFLOW_ADDR")
	if addr == "" {
		addr = "127.0.0.1:3000"
	}
	if host, port, err := net.SplitHostPort(addr); err == nil && (host == "" || host == "0.0.0.0" || host == "::") {
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	c := &http.Client{Timeout: 4 * time.Second}
	resp, err := c.Get("http://" + addr + "/api/v1/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health: HTTP %d", resp.StatusCode)
	}
	return nil
}

// localBaseURL turns a listen address into a URL processes on this host can use
// (0.0.0.0:3000 -> http://127.0.0.1:3000).
func localBaseURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// rotateKey re-encrypts every connector's secrets with the primary key. Procedure:
//  1. set CUEFLOW_SECRET_KEY to a new key and CUEFLOW_SECRET_KEY_PREVIOUS to the old one, restart
//  2. run `cueflow rotate-key` (optionally `--dry-run` first)
//  3. remove CUEFLOW_SECRET_KEY_PREVIOUS and restart
func rotateKey(dryRun bool) error {
	dropEmptyCredentialEnv()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	box, err := secret.New(cfg.SecretKey, cfg.PreviousSecretKeys...)
	if err != nil {
		return err
	}
	rep, err := store.New(pool, box).ReencryptConnectorSecrets(ctx, dryRun)
	verb := "re-encrypted"
	if dryRun {
		verb = "would re-encrypt"
	}
	if rep != nil {
		fmt.Printf("connectors with secrets: %d, already on the current key: %d, %s: %d\n",
			rep.Total, rep.AlreadyPrimary, verb, rep.Reencrypted)
	}
	if err != nil {
		return err
	}
	if !dryRun && box.HasPrevious() {
		fmt.Println("done: remove CUEFLOW_SECRET_KEY_PREVIOUS and restart CueFlow")
	}
	return nil
}

// mediaCmd handles `cueflow media migrate --to LOCATION` (LOCATION: file:///dir, or db).
func mediaCmd(args []string) error {
	if len(args) < 3 || args[0] != "migrate" || args[1] != "--to" {
		return errors.New("usage: cueflow media migrate --to file:///dir|db")
	}
	dst, err := blob.Parse(args[2])
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	box, err := secret.New(cfg.SecretKey, cfg.PreviousSecretKeys...)
	if err != nil {
		return err
	}
	st := store.New(pool, box)
	// Rows already moved elsewhere stay readable only through the configured backend.
	cur, err := blob.Parse(cfg.MediaStorage)
	if err != nil {
		return err
	}
	st.SetBlobs(cur)
	if dst != nil && cur != nil && dst.Kind() == cur.Kind() {
		st.SetBlobs(dst)
	}
	n, err := st.MigrateMedia(ctx, dst)
	fmt.Printf("moved %d media item(s)\n", n)
	return err
}
