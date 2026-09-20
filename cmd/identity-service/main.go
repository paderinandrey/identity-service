// Command identity-service runs the Identity & Access Service.
//
// Subcommands:
//
//	serve        run the HTTP server (default)
//	migrate      apply database schema migrations
//	create-user  create or update a user by email (development/bootstrap)
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"gopkg.in/yaml.v3"

	"github.com/paderinandrey/identity-service/internal/access"
	"github.com/paderinandrey/identity-service/internal/config"
	"github.com/paderinandrey/identity-service/internal/events"
	graphqlapi "github.com/paderinandrey/identity-service/internal/graphql"
	"github.com/paderinandrey/identity-service/internal/httpserver"
	"github.com/paderinandrey/identity-service/internal/identity"
	"github.com/paderinandrey/identity-service/internal/logging"
	"github.com/paderinandrey/identity-service/internal/observability"
	"github.com/paderinandrey/identity-service/internal/postgres"
	"github.com/paderinandrey/identity-service/internal/redisstore"
	"github.com/paderinandrey/identity-service/internal/samlsso"
	"github.com/paderinandrey/identity-service/internal/scim"
	"github.com/paderinandrey/identity-service/internal/session"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "identity-service:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.LogLevel)

	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	switch cmd {
	case "serve":
		return serve(ctx, cfg, logger)
	case "migrate":
		return postgres.Migrate(ctx, cfg.DatabaseURL)
	case "create-user":
		return createUser(ctx, cfg, args)
	case "grant-role":
		return changeRole(ctx, cfg, args, true)
	case "revoke-role":
		return changeRole(ctx, cfg, args, false)
	case "seed-access":
		return seedAccess(ctx, cfg, args)
	case "import-assignments":
		return importAssignments(ctx, cfg, args)
	case "replay-users":
		return replayUsers(ctx, cfg)
	default:
		return fmt.Errorf("unknown command %q (want serve, migrate, create-user, grant-role, revoke-role, seed-access, import-assignments or replay-users)", cmd)
	}
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	obs, err := observability.Init(observability.Config{
		SentryDSN:   cfg.SentryDSN,
		Environment: cfg.AppEnv,
	})
	if err != nil {
		return err
	}
	defer obs.Shutdown()
	logger = slog.New(obs.LogHandler(logger.Handler()))

	logger.Info("starting identity-service", "env", cfg.AppEnv, "addr", cfg.ListenAddr, "internal_addr", cfg.InternalListenAddr)

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	logger.Info("connected to PostgreSQL")

	redisClient, err := connectRedis(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer func() { _ = redisClient.Close() }()
	logger.Info("connected to Redis")

	store := postgres.NewStore(pool)
	sessions := session.NewManager(redisClient, session.Config{
		CookieName:    cfg.SessionCookieName,
		IdleTimeout:   cfg.SessionIdleTimeout,
		Lifetime:      cfg.SessionLifetime,
		MaxConcurrent: cfg.SessionsMaxConcurrent,
		SecureCookie:  !cfg.IsDevelopment(),
	}, logger)

	accessStore := postgres.NewAccessStore(pool)
	userCache := identity.NewUserCache(store, cfg.UserRevocationDelay, cfg.CacheMaxEntries)
	userCache.SetMetrics(obs.Cache("user"))
	permsCache := access.NewPermissionsCache(accessStore, cfg.PermissionsCacheTTL, cfg.CacheMaxEntries)
	permsCache.SetMetrics(obs.Cache("permissions"))
	users := &userSource{users: userCache, perms: permsCache}
	sessionHandlers := session.NewHandlers(sessions, users, []string{cfg.BaseURL, cfg.FrontendBaseURL})
	graphqlServer := graphqlapi.NewServer(&graphqlapi.Resolver{
		Directory: store,
		Access:    accessStore,
		Logger:    logger,
	}, sessions, users, []string{cfg.BaseURL, cfg.FrontendBaseURL}, logger)

	var samlService *samlsso.Service
	if cfg.SAMLIdPMetadataURL != "" {
		samlService, err = samlsso.New(ctx, samlsso.Config{
			BaseURL:           cfg.BaseURL,
			FrontendBaseURL:   cfg.FrontendBaseURL,
			IDPMetadataURL:    cfg.SAMLIdPMetadataURL,
			RelayStateSecret:  cfg.RelayStateSecret,
			AllowIDPInitiated: cfg.SAMLAllowIDPInitiated,
		}, store, sessions, redisstore.NewNonceStore(redisClient), logger)
		if err != nil {
			return err
		}
		samlService.SetMetrics(obs.SignIns())
		go samlService.RefreshMetadataLoop(ctx)
	} else {
		logger.Warn("SAML_IDP_METADATA_URL is not set: SSO routes are disabled")
	}

	relay := events.NewRelay(pool, cfg.RabbitMQURL, cfg.EventsExchange, logger)
	relay.SetMetrics(obs.Relay())
	go relay.Run(ctx)

	srv := httpserver.New(cfg.ListenAddr, logger, cfg.ShutdownTimeout,
		httpserver.WithWrapper(func(h http.Handler) http.Handler {
			return obs.Recover(obs.HTTPMetrics(h), logger)
		}),
		httpserver.WithReadyCheck(dependencyCheck(pool, redisClient)),
		// Public zone: what the entry proxy routes from outside.
		httpserver.WithRoutes(func(mux *http.ServeMux) {
			authMux := http.NewServeMux()
			sessionHandlers.Register(authMux)
			if samlService != nil {
				samlService.Register(authMux)
			}
			authMux.Handle("POST /graphql", graphqlServer)
			withSessions := sessions.Middleware(authMux)
			mux.Handle("/auth/", withSessions)
			mux.Handle("/graphql", withSessions)
			// SCIM is machine-authenticated: mounted outside the session
			// middleware, and only when a client token is configured.
			if cfg.SCIMToken != "" {
				scimHandlers := scim.NewHandlers(store, sessions, cfg.SCIMToken, logger)
				scimHandlers.SetMetrics(obs.SCIM())
				scimHandlers.Register(mux)
				logger.Info("SCIM provisioning enabled")
			}
		}),
		// Internal zone: reachable only from the proxy and monitoring
		// (NetworkPolicy in the chart); never routed from outside.
		httpserver.WithInternal(cfg.InternalListenAddr, func(mux *http.ServeMux) {
			internalAuth := http.NewServeMux()
			sessionHandlers.RegisterInternal(internalAuth)
			withSessions := sessions.Middleware(internalAuth)
			mux.Handle("/internal/session/", withSessions)
			// Programmatic sessions for E2E suites; never in production
			// (config guards the combination at startup).
			if cfg.E2ELoginToken != "" {
				session.RegisterE2ELogin(internalAuth, sessions, store, cfg.E2ELoginToken, logger)
				mux.Handle("/internal/e2e/", withSessions)
				logger.Info("E2E login endpoint enabled")
			}
			mux.Handle("GET /internal/metrics", obs.MetricsHandler())
		}),
	)
	if err := srv.Run(ctx); err != nil {
		return err
	}
	logger.Info("stopped")
	return nil
}

// userSource serves session-validation lookups from caches: validate runs
// on every ecosystem request, so a warm cache must not touch PostgreSQL.
type userSource struct {
	users *identity.UserCache
	perms *access.PermissionsCache
}

func (u *userSource) FindByID(ctx context.Context, id string) (*identity.User, error) {
	return u.users.FindByID(ctx, id)
}

func (u *userSource) IsActive(ctx context.Context, id string) (bool, error) {
	return u.users.IsActive(ctx, id)
}

func (u *userSource) Permissions(ctx context.Context, id string) ([]string, error) {
	return u.perms.EffectivePermissions(ctx, id)
}

func connectRedis(ctx context.Context, redisURL string) (*redis.Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return client, nil
}

// dependencyCheck pings PostgreSQL and Redis, caching the result briefly so
// frequent probes do not hammer the dependencies.
func dependencyCheck(pool interface {
	Ping(context.Context) error
}, redisClient *redis.Client) func(context.Context) error {
	const cacheFor = 2 * time.Second
	var (
		mu      sync.Mutex
		last    error
		checked time.Time
	)
	return func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(checked) < cacheFor {
			return last
		}
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		last = nil
		if err := pool.Ping(pingCtx); err != nil {
			last = fmt.Errorf("postgres: %w", err)
		} else if err := redisClient.Ping(pingCtx).Err(); err != nil {
			last = fmt.Errorf("redis: %w", err)
		}
		checked = time.Now()
		return last
	}
}

func createUser(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("create-user", flag.ContinueOnError)
	email := fs.String("email", "", "user email (required)")
	name := fs.String("name", "", "display name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return fmt.Errorf("create-user: --email is required")
	}

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	user, err := postgres.NewStore(pool).UpsertByEmail(ctx, *email, *name)
	if err != nil {
		return err
	}
	fmt.Printf("user %s <%s> id=%s active=%t\n", user.Name, user.Email, user.ID, user.Active)
	return nil
}

// cliActor identifies bootstrap CLI operations in the audit journal.
const cliActor = "cli"

func changeRole(ctx context.Context, cfg config.Config, args []string, grant bool) error {
	name := "revoke-role"
	if grant {
		name = "grant-role"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	email := fs.String("email", "", "user email (required)")
	roleRef := fs.String("role", "", "role as app/role (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" || *roleRef == "" {
		return fmt.Errorf("%s: --email and --role are required", name)
	}
	app, role, err := access.SplitRoleRef(*roleRef)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	user, err := postgres.NewStore(pool).FindActiveByEmail(ctx, identity.NormalizeEmail(*email))
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	accessStore := postgres.NewAccessStore(pool)
	if grant {
		err = accessStore.GrantRole(ctx, cliActor, user.ID, app, role)
	} else {
		err = accessStore.RevokeRole(ctx, cliActor, user.ID, app, role)
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s/%s for %s\n", name, app, role, user.Email)
	return nil
}

// importAssignments grants roles to many users from a file: the cutover
// path for loading another system's assignments. Idempotent and
// restartable; one entry's failure is reported and does not stop the rest.
func importAssignments(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("import-assignments", flag.ContinueOnError)
	file := fs.String("file", "", "YAML file with assignments (required)")
	dryRun := fs.Bool("dry-run", false, "resolve users and roles, report, write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("import-assignments: --file is required")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var imp access.ImportFile
	if err := yaml.Unmarshal(raw, &imp); err != nil {
		return fmt.Errorf("import-assignments: parse %s: %w", *file, err)
	}
	if err := imp.Validate(); err != nil {
		return fmt.Errorf("import-assignments: %w", err)
	}

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := postgres.NewStore(pool)
	accessStore := postgres.NewAccessStore(pool)

	ok, failed := 0, 0
	for _, entry := range imp.Assignments {
		var user *identity.User
		if entry.ID != "" {
			user, err = store.FindByID(ctx, entry.ID)
		} else {
			user, err = store.FindActiveByEmail(ctx, identity.NormalizeEmail(entry.Email))
		}
		if err != nil {
			fmt.Printf("FAIL %s: %v\n", entry.Key(), err)
			failed++
			continue
		}
		granted, err := accessStore.GrantRoles(ctx, cliActor, user.ID, entry.Roles, *dryRun)
		if err != nil {
			fmt.Printf("FAIL %s: %v\n", entry.Key(), err)
			failed++
			continue
		}
		fmt.Printf("ok %s: granted %d, already %d\n", entry.Key(), granted, len(entry.Roles)-granted)
		ok++
	}
	suffix := ""
	if *dryRun {
		suffix = " (dry-run, nothing written)"
	}
	fmt.Printf("import-assignments: %d ok, %d failed%s\n", ok, failed, suffix)
	if failed > 0 {
		return fmt.Errorf("import-assignments: %d entry(ies) failed; fix the file and rerun — applied entries are idempotent", failed)
	}
	return nil
}

func seedAccess(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("seed-access", flag.ContinueOnError)
	file := fs.String("file", "", "YAML file with applications, permissions and roles (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("seed-access: --file is required")
	}

	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var seedCfg access.SeedConfig
	if err := yaml.Unmarshal(raw, &seedCfg); err != nil {
		return fmt.Errorf("seed-access: parse %s: %w", *file, err)
	}

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := postgres.NewAccessStore(pool).Seed(ctx, cliActor, seedCfg); err != nil {
		return err
	}
	fmt.Printf("seed-access: %d application(s) reconciled from %s\n", len(seedCfg.Applications), *file)
	return nil
}

func replayUsers(ctx context.Context, cfg config.Config) error {
	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	n, err := postgres.NewStore(pool).EnqueueSnapshots(ctx, 500)
	if err != nil {
		return err
	}
	fmt.Printf("replay-users: %d snapshot event(s) enqueued\n", n)
	return nil
}
