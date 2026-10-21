// Command cueflow runs the CueFlow agent ops console: API + dashboard, the scheduler, the agent crew
// and the modules' workers (the content module's publish worker).
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
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/blob"
	"github.com/j75689/cueflow/internal/cfaccess"
	"github.com/j75689/cueflow/internal/config"
	_ "github.com/j75689/cueflow/internal/connectors/all"
	"github.com/j75689/cueflow/internal/content"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/db"
	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/events/pg"
	"github.com/j75689/cueflow/internal/events/pubsub"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/internalauth"
	"github.com/j75689/cueflow/internal/jobs"
	"github.com/j75689/cueflow/internal/jobs/cloudtasks"
	"github.com/j75689/cueflow/internal/mcpx"
	"github.com/j75689/cueflow/internal/runner"
	"github.com/j75689/cueflow/internal/scheduler"
	"github.com/j75689/cueflow/internal/secret"
	"github.com/j75689/cueflow/internal/store"
	"github.com/j75689/cueflow/internal/version"
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
		err = runner.Main(os.Args[2:], version.Version)
	case "media":
		err = mediaCmd(os.Args[2:])
	case "mcp-bridge":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: cueflow mcp-bridge URL")
			os.Exit(2)
		}
		err = mcpx.Bridge(context.Background(), os.Args[2])
	case "version", "-v", "--version":
		fmt.Println("cueflow", version.Version)
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
	pool, err := openDB(ctx, cfg, 0)
	if err != nil {
		return err
	}
	defer pool.Close()
	return migrateDB(ctx, cfg, pool)
}

// openDB opens the main pool: for a transaction-mode pooler when a session URL is set.
func openDB(ctx context.Context, cfg *config.Config, maxConns int) (*pgxpool.Pool, error) {
	return db.OpenWith(ctx, cfg.DatabaseURL, db.Options{MaxConns: maxConns, TxPooler: cfg.DatabaseSessionURL != ""})
}

// migrateDB migrates through the session URL when there is one (the migration lock needs a session).
func migrateDB(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool) error {
	if cfg.DatabaseSessionURL != "" {
		return db.MigrateSession(ctx, cfg.DatabaseSessionURL)
	}
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
	for _, w := range cfg.Warnings {
		logger.Warn("config: " + w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := connectWithRetry(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrateDB(ctx, cfg, pool); err != nil {
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
	// "Everything running is stale" is only true when this is the one process. In the cloud, other
	// instances own live work and the clock's lease-based cleanup (FailAbandoned) does the job.
	cs := content.NewStore(st)
	broker := events.NewBroker()
	closeEvents := func() {}
	if cfg.Events == "pubsub" {
		t, err := newPubSub(ctx, cfg.PubSubTopic)
		if err != nil {
			return err
		}
		var once sync.Once
		closeEvents = func() {
			once.Do(func() {
				closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := t.Close(closeCtx); err != nil {
					logger.Warn("delete events subscription (it expires on its own)", "subscription", t.Subscription(), "err", err)
				}
			})
		}
		defer closeEvents() // a startup error after this point
		broker.Use(t, logger)
	} else {
		broker.Use(&pg.Transport{Pool: pool, ListenURL: cfg.DatabaseSessionURL}, logger)
	}
	go broker.Run(ctx)
	h := hub.New(st, logger)
	sched := scheduler.New(st, logger)

	// Modules (CUEFLOW_MODULES). Each one brings its routes, workers, cron and agent tools; a module
	// that is not mounted costs nothing at runtime and its tables stay where they are.
	var modules []api.Module
	handlers := jobs.Handlers{}
	var sweeps []func(context.Context) error
	var workers []func(context.Context)
	var queued []*jobs.Queue
	if cfg.Enabled("content") {
		// The post kind, source connectors and the agents' drafting tools.
		contentModule := &content.Module{Store: cs, Hub: h, Broker: broker, Logger: logger, MediaBaseURL: cfg.MediaBaseURL}
		contentModule.Attach()
		modules = append(modules, contentModule)
	}
	if cfg.Enabled("deliverables") {
		// Typed items any agent can produce, reviewed by a human, then acted on.
		ds := deliverable.NewStore(st)
		deliverer := deliverable.NewWorker(ds, h, broker, logger, cfg.DispatchEvery, cfg.DispatchLease, cfg.PublishTimeout)
		deliverableModule := &deliverable.Module{Store: ds, Hub: h, Broker: broker, Dispatcher: deliverer, Logger: logger}
		deliverableModule.Attach()
		modules = append(modules, deliverableModule)
		handlers[deliverable.KindDeliver] = func(ctx context.Context, _ uuid.UUID) error { return deliverer.Handle(ctx) }
		sweeps = append(sweeps, deliverer.Sweep)
		workers = append(workers, deliverer.Run)
		queued = append(queued, &deliverer.Queue)
	}

	team := crew.New(st, h, broker, logger, cfg.AgentAllowPrivateURLs)
	team.Concurrency = cfg.AgentConcurrency
	team.ContextHandOffAt = cfg.ContextHandOffAt
	team.MCPBaseURL = localBaseURL(cfg.Addr)
	team.PublicURL = cfg.PublicURL
	team.ValidateSchedule = scheduler.ValidateSchedule
	team.OnSchedulesChanged = func(ctx context.Context) { _ = sched.Reload(ctx) }
	sched.OnTaskSpawned = func(ctx context.Context, t *store.Task) {
		broker.Publish(t.OrgID, "task.updated", map[string]any{"id": t.ID, "status": t.Status})
		team.Wake()
	}
	if !cfg.Cloud() {
		if err := st.FailStaleAgentRuns(ctx); err != nil {
			logger.Warn("clean stale agent runs", "err", err)
		}
		if err := st.CancelOrphanRunnerJobs(ctx); err != nil {
			logger.Warn("clean stale runner jobs", "err", err)
		}
	}
	if org, err := st.DefaultOrg(ctx); err != nil {
		logger.Warn("seed default agents", "err", err)
	} else if n, err := crew.SeedDefaults(ctx, st, org.ID); err != nil {
		logger.Warn("seed default agents", "err", err)
	} else if n > 0 {
		logger.Info("created the default agent team", "agents", n)
	}
	apiSrv := &api.Server{
		Store: st, Hub: h, Broker: broker, Scheduler: sched, Crew: team,
		Logger: logger, AdminToken: cfg.AdminToken, MCPToken: cfg.MCPToken, PublicURL: cfg.PublicURL, DirectURL: cfg.DirectURL,
		Modules: modules,
	}
	if cfg.CFAccessTeamDomain != "" {
		apiSrv.Access = cfaccess.New(ctx, cfg.CFAccessTeamDomain, cfg.CFAccessAudience)
		logger.Info("Cloudflare Access login enabled", "team", cfg.CFAccessTeamDomain)
	}
	if cfg.Queue == "cloudtasks" {
		q, err := cloudtasks.New(cloudtasks.Config{Queue: cfg.TasksQueue, TargetURL: cfg.TasksTargetURL,
			ServiceAccount: cfg.TasksServiceAccount, Audience: cfg.InternalAudience})
		if err != nil {
			return fmt.Errorf("cloud tasks: %w", err)
		}
		team.Queue, sched.Queue = q, q
		for _, wq := range queued {
			*wq = q
		}
	}
	if cfg.Tick == "external" {
		// The clock and the queue live outside: Cloud Scheduler calls /internal/tick, Cloud Tasks
		// calls /internal/work/*. Nothing is polled or scheduled in this process.
		sched.External = true
		apiSrv.InternalAuth = internalauth.Middleware(internalauth.NewGoogleVerifier(cfg.InternalAudience, cfg.InternalServiceAccounts))
		handlers[jobs.KindCrew] = func(ctx context.Context, _ uuid.UUID) error { return team.HandleCrew(ctx) }
		handlers[jobs.KindSchedule] = sched.HandleSchedule
		handlers[jobs.KindApproval] = team.RunApproval
		apiSrv.Handlers = handlers
		sched.MediaRetention = time.Duration(cfg.MediaRetentionDays) * 24 * time.Hour
		if len(cfg.MediaRegisterAccounts) > 0 {
			apiSrv.MediaRegisterAuth = internalauth.Middleware(internalauth.NewGoogleVerifier(cfg.InternalAudience, cfg.MediaRegisterAccounts))
		}
		apiSrv.Tick = func(ctx context.Context) error {
			errs := []error{sched.Tick(ctx), team.Sweep(ctx)}
			for _, sweep := range sweeps {
				errs = append(errs, sweep(ctx))
			}
			return errors.Join(errs...)
		}
		logger.Info("external clock: waiting for /internal/tick and /internal/work deliveries", "queue", cfg.Queue)
	} else {
		if !cfg.DisableScheduler {
			if err := sched.Start(ctx); err != nil {
				return err
			}
		}
		for _, run := range workers {
			go run(ctx)
		}
		go team.Run(ctx)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		logger.Info("CueFlow listening", "addr", "http://"+cfg.Addr, "version", version.Version, "auth", cfg.AdminToken != "", "mcp", cfg.MCPToken != "", "modules", cfg.Modules)
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
	// Before the HTTP drain, which waits for open dashboard streams: Cloud Run kills the instance 10 s
	// after SIGTERM, and a subscription left behind lingers until it expires a day later.
	closeEvents()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	team.Wait()
	return nil
}

// newPubSub creates this instance's events subscription; PUBSUB_EMULATOR_HOST points it at the
// emulator, as Google's own clients do.
func newPubSub(ctx context.Context, topic string) (*pubsub.Transport, error) {
	cfg := pubsub.Config{Topic: topic}
	if h := os.Getenv("PUBSUB_EMULATOR_HOST"); h != "" {
		cfg.Endpoint, cfg.Client = "http://"+h, http.DefaultClient
	}
	return pubsub.New(ctx, cfg)
}

// connectWithRetry waits for PostgreSQL (it may still be starting under docker compose).
func connectWithRetry(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*pgxpool.Pool, error) {
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		pool, err := openDB(ctx, cfg, cfg.DBMaxConns)
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
	pool, err := openDB(ctx, cfg, 0)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrateDB(ctx, cfg, pool); err != nil {
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

// mediaCmd handles `cueflow media migrate --to LOCATION` (file:///dir, gcs://bucket, or db) and
// `cueflow media prune --older-than 90d [--dry-run]`.
func mediaCmd(args []string) error {
	const usage = "usage: cueflow media migrate --to file:///dir|gcs://bucket|db\n       cueflow media prune --older-than 90d [--dry-run]"
	if len(args) == 0 {
		return errors.New(usage)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := openDB(ctx, cfg, 0)
	if err != nil {
		return err
	}
	defer pool.Close()
	box, err := secret.New(cfg.SecretKey, cfg.PreviousSecretKeys...)
	if err != nil {
		return err
	}
	st := store.New(pool, box)
	cur, err := blob.Parse(cfg.MediaStorage)
	if err != nil {
		return err
	}
	st.SetBlobs(cur) // rows already moved elsewhere stay readable only through the configured backend

	switch args[0] {
	case "migrate":
		if len(args) < 3 || args[1] != "--to" {
			return errors.New(usage)
		}
		dst, err := blob.Parse(args[2])
		if err != nil {
			return err
		}
		if dst != nil && cur != nil && dst.Kind() == cur.Kind() {
			st.SetBlobs(dst)
		}
		n, err := st.MigrateMedia(ctx, dst)
		fmt.Printf("moved %d media item(s)\n", n)
		return err
	case "prune":
		fs := flag.NewFlagSet("prune", flag.ContinueOnError)
		olderThan := fs.String("older-than", "", "delete media older than this, e.g. 90d, 36h")
		dry := fs.Bool("dry-run", false, "only count what would be deleted")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		age, err := parseAge(*olderThan)
		if err != nil {
			return err
		}
		n, err := st.PruneMedia(ctx, age, *dry)
		verb := "deleted"
		if *dry {
			verb = "would delete"
		}
		fmt.Printf("%s %d media item(s) older than %s that no unfinished post uses\n", verb, n, *olderThan)
		return err
	}
	return errors.New(usage)
}

// parseAge reads "90d", "12h" or any time.ParseDuration value.
func parseAge(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("--older-than %q: want a positive number of days, e.g. 90d", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--older-than is required, e.g. 90d or 36h (got %q)", s)
	}
	return d, nil
}
