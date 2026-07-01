package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	eventslib "github.com/Bengo-Hub/shared-events"

	"github.com/bengobox/library-service/internal/config"
	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/migrate"
	handlers "github.com/bengobox/library-service/internal/http/handlers"
	router "github.com/bengobox/library-service/internal/http/router"
	"github.com/bengobox/library-service/internal/modules/calendar"
	"github.com/bengobox/library-service/internal/modules/circulation"
	"github.com/bengobox/library-service/internal/modules/consumers"
	"github.com/bengobox/library-service/internal/modules/membership"
	"github.com/bengobox/library-service/internal/modules/rbac"
	"github.com/bengobox/library-service/internal/modules/refdata"
	"github.com/bengobox/library-service/internal/platform/cache"
	"github.com/bengobox/library-service/internal/platform/database"
	"github.com/bengobox/library-service/internal/platform/events"
	"github.com/bengobox/library-service/internal/platform/secrets"
	"github.com/bengobox/library-service/internal/platform/marketflow"
	"github.com/bengobox/library-service/internal/platform/subscriptions"
	"github.com/bengobox/library-service/internal/platform/treasury"
	"github.com/bengobox/library-service/internal/shared/logger"
)

// App holds the wired runtime for the library service.
type App struct {
	cfg             *config.Config
	log             *zap.Logger
	httpServer      *http.Server
	db              *pgxpool.Pool
	cache           *redis.Client
	events          *nats.Conn
	orm             *ent.Client
	outboxPublisher       *eventslib.OutboxPoller
	circulation           *circulation.Service
	membership            *membership.Service
	patronCategoryScheduler *membership.PatronCategoryScheduler
	paymentConsumer       *consumers.PaymentConsumer
}

// New constructs and wires the application.
// terminalJWTSecret returns the PIN/terminal JWT signing secret, falling back to the shared
// INTERNAL_SERVICE_KEY when TERMINAL_JWT_SECRET isn't set (mirrors pos-api) so desk/kiosk PIN
// login works without provisioning a dedicated secret.
func terminalJWTSecret(cfg *config.Config) string {
	if cfg.Auth.TerminalJWTSecret != "" {
		return cfg.Auth.TerminalJWTSecret
	}
	return cfg.Auth.APIKey
}

func New(ctx context.Context) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	log, err := logger.New(cfg.App.Env)
	if err != nil {
		return nil, fmt.Errorf("logger init: %w", err)
	}

	dbPool, err := database.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return nil, fmt.Errorf("postgres init: %w", err)
	}
	redisClient := cache.NewClient(cfg.Redis)

	natsConn, err := events.Connect(cfg.Events)
	if err != nil {
		log.Warn("event bus connection failed", zap.Error(err))
	}
	if natsConn != nil {
		if streamErr := events.EnsureStream(ctx, natsConn, cfg.Events); streamErr != nil {
			log.Warn("failed to ensure library stream", zap.Error(streamErr))
		}
	}

	healthHandler := handlers.NewHealthHandler(log, dbPool, redisClient, natsConn)

	sqlDB, err := sql.Open("pgx", cfg.Postgres.URL)
	if err != nil {
		return nil, fmt.Errorf("ent driver init: %w", err)
	}
	sqlDB.SetMaxIdleConns(cfg.Postgres.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.Postgres.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(cfg.Postgres.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	drv := entsql.OpenDB(dialect.Postgres, sqlDB)
	ormClient := ent.NewClient(ent.Driver(drv))

	if cfg.Postgres.RunMigrations {
		if err := ormClient.Schema.Create(ctx, schema.WithDir(migrate.Dir)); err != nil {
			return nil, fmt.Errorf("ent schema create: %w", err)
		}
		log.Info("versioned migrations applied (POSTGRES_RUN_MIGRATIONS=true)")
	}

	// Transactional outbox poller.
	var outboxPublisher *eventslib.OutboxPoller
	if natsConn != nil && cfg.Events.OutboxEnabled {
		outboxRepo := eventslib.NewSQLOutboxRepository(sqlDB)
		outboxNatsPublisher := eventslib.NewNATSAdapter(natsConn, log)
		outboxPublisher = eventslib.NewOutboxPoller(outboxRepo, outboxNatsPublisher, log, eventslib.PollerConfig{
			BatchSize:  cfg.Events.OutboxBatchSize,
			PollPeriod: cfg.Events.OutboxPollPeriod,
		})
		outboxPublisher.Start(ctx)
		log.Info("outbox background publisher started")
	}

	// RBAC (seed global roles once).
	rbacService := rbac.NewService(ormClient, log)
	if err := rbacService.SeedGlobalRoles(ctx); err != nil {
		log.Warn("seed global roles failed", zap.Error(err))
	}
	// Seed the shared global default collections (idempotent, nil-tenant).
	if err := refdata.SeedGlobalCollections(ctx, ormClient, log); err != nil {
		log.Warn("seed global collections failed", zap.Error(err))
	}
	// Seed shared global member tiers + loan policies (idempotent, nil-tenant).
	if err := refdata.SeedGlobalTiersPolicies(ctx, ormClient, log); err != nil {
		log.Warn("seed global tiers/policies failed", zap.Error(err))
	}
	// Seed standard authorized values (LOC, CCODE, NOT_LOAN, etc.) under the global tenant.
	if err := refdata.SeedAuthorizedValues(ctx, ormClient, refdata.GlobalTenantID, log); err != nil {
		log.Warn("seed authorized values failed", zap.Error(err))
	}
	// Seed demo desk PINs for the sandbox tenant (idempotent; no-op for other tenants).
	if err := refdata.SeedDemoStaff(ctx, ormClient, log); err != nil {
		log.Warn("seed demo staff failed", zap.Error(err))
	}
	// Top demo physical titles up to 5 copies each (idempotent; no-op for other tenants).
	if err := refdata.SeedDemoCopies(ctx, ormClient, log); err != nil {
		log.Warn("seed demo copies failed", zap.Error(err))
	}
	// Seed demo patrons (incl. member-role staff) so the Members list isn't empty (idempotent).
	if err := refdata.SeedDemoMembers(ctx, ormClient, log); err != nil {
		log.Warn("seed demo members failed", zap.Error(err))
	}
	// Push the library role catalogue to the auth registry (idempotent; best-effort) so
	// auth-ui can assign service-level library roles. Runs off the request path.
	go func() {
		if err := rbacService.PushRolesToAuthRegistry(context.Background(), cfg.Auth.ServiceURL, cfg.Auth.APIKey); err != nil {
			log.Warn("auth role-registry push failed", zap.Error(err))
		}
	}()

	// S2S clients.
	treasuryClient := treasury.NewClient(cfg.Services.TreasuryURL, cfg.Auth.APIKey, 0)
	marketflowClient := marketflow.NewClient(cfg.Services.MarketflowURL, cfg.Auth.APIKey, log)
	subsClient := subscriptions.NewClient(subscriptions.Config{
		ServiceURL:     cfg.Subscriptions.ServiceURL,
		APIKey:         cfg.Subscriptions.APIKey,
		RequestTimeout: cfg.Subscriptions.RequestTimeout,
	})

	// Domain services + handlers.
	calendarCalc := calendar.NewCalculator(ormClient, redisClient, log)
	circulationSvc := circulation.NewService(ormClient, redisClient, log)
	membershipSvc := membership.NewService(ormClient, log)
	secretStore := secrets.NewStore(ormClient, log)
	deps := router.Deps{
		Log:            log,
		Health:         healthHandler,
		Auth:           handlers.NewAuthHandler(rbacService, log),
		Catalog:        handlers.NewCatalogHandler(ormClient, secretStore, cfg.Media.Root, log),
		Branch:         handlers.NewBranchHandler(ormClient, log),
		Member:         handlers.NewMemberHandler(ormClient, marketflowClient, log),
		Circulation:    handlers.NewCirculationHandler(ormClient, circulationSvc, log),
		Hold:           handlers.NewHoldHandler(ormClient, log),
		Fine:           handlers.NewFineHandler(ormClient, treasuryClient, log),
		Ebook:          handlers.NewEbookHandler(ormClient, treasuryClient, cfg.Media.EbookRoot, log),
		Reports:        handlers.NewReportsHandler(ormClient, log),
		RBACHandler:    handlers.NewRBACHandler(rbacService, log),
		Membership:     handlers.NewMembershipHandler(ormClient, membershipSvc, treasuryClient, log),
		Sequence:       handlers.NewSequenceHandler(ormClient, log),
		PINAuth:          handlers.NewPINAuthHandler(ormClient, rbacService, subsClient, terminalJWTSecret(cfg), log),
		PlatformConfig:   handlers.NewPlatformConfigHandler(secretStore, log),
		CirculationRules: handlers.NewCirculationRuleHandler(ormClient, circulationSvc, log),
		Holiday:          handlers.NewHolidayHandler(ormClient, calendarCalc, log),
		AuthorizedValues: handlers.NewAuthorizedValueHandler(ormClient, log),
		Acquisition:      handlers.NewAcquisitionHandler(ormClient, treasuryClient, log),
		RBAC:           rbacService,
		AllowedOrigins: cfg.HTTP.AllowedOrigins,
		MediaRoot:      cfg.Media.Root,
	}

	// auth-service JWT validator (JWKS) + optional S2S API key.
	authConfig := authclient.DefaultConfig(cfg.Auth.JWKSUrl, cfg.Auth.Issuer, cfg.Auth.Audience)
	authConfig.CacheTTL = cfg.Auth.JWKSCacheTTL
	authConfig.RefreshInterval = cfg.Auth.JWKSRefreshInterval
	validator, err := authclient.NewValidator(authConfig)
	if err != nil {
		return nil, fmt.Errorf("auth validator init: %w", err)
	}
	if cfg.Auth.EnableAPIKeyAuth {
		apiKeyValidator := authclient.NewAPIKeyValidator(cfg.Auth.ServiceURL, nil)
		deps.AuthMiddleware = authclient.NewAuthMiddlewareWithAPIKey(validator, apiKeyValidator)
	} else {
		deps.AuthMiddleware = authclient.NewAuthMiddleware(validator)
	}

	chiRouter := router.New(deps)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.HTTP.Host, cfg.HTTP.Port),
		Handler:           chiRouter,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	return &App{
		cfg:             cfg,
		log:             log,
		httpServer:      httpServer,
		db:              dbPool,
		cache:           redisClient,
		events:          natsConn,
		orm:                     ormClient,
		outboxPublisher:         outboxPublisher,
		circulation:             circulationSvc,
		membership:              membershipSvc,
		patronCategoryScheduler: membership.NewPatronCategoryScheduler(ormClient, log),
		paymentConsumer:         consumers.NewPaymentConsumer(ormClient, log),
	}, nil
}

// Run starts background workers + the HTTP server and blocks until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	// Overdue scheduler (idempotent; safe on every replica).
	a.circulation.StartOverdueScheduler(ctx, time.Hour)

	// Hold expiry scheduler — expires READY holds past their pickup deadline, frees copies.
	a.circulation.StartHoldExpiryScheduler(ctx, time.Hour)

	// Membership-fee dunning scheduler (daily; auto-issues fees near expiry).
	a.membership.StartScheduler(ctx, 24*time.Hour)

	// Patron category auto-transition scheduler (daily; expires cards + graduates patrons by age).
	a.patronCategoryScheduler.Start(ctx, 24*time.Hour)

	// Treasury payment reconcile consumer.
	if a.events != nil && a.paymentConsumer != nil {
		if js, err := a.events.JetStream(); err == nil {
			if err := a.paymentConsumer.Start(ctx, js); err != nil {
				a.log.Warn("payment consumer not started", zap.Error(err))
			} else {
				a.log.Info("treasury payment reconcile consumer started")
			}
		}
	}

	errCh := make(chan error, 1)
	if a.cfg.HTTP.TLSCertFile != "" && a.cfg.HTTP.TLSKeyFile != "" {
		a.log.Info("library service starting with HTTPS", zap.String("addr", a.httpServer.Addr))
		go func() { errCh <- a.httpServer.ListenAndServeTLS(a.cfg.HTTP.TLSCertFile, a.cfg.HTTP.TLSKeyFile) }()
	} else {
		a.log.Info("library service starting with HTTP", zap.String("addr", a.httpServer.Addr))
		go func() { errCh <- a.httpServer.ListenAndServe() }()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server error: %w", err)
	}
}

// Close releases resources in reverse dependency order.
func (a *App) Close() {
	if a.outboxPublisher != nil {
		a.outboxPublisher.Stop()
	}
	if a.events != nil {
		_ = a.events.Drain()
		a.events.Close()
	}
	if a.cache != nil {
		_ = a.cache.Close()
	}
	if a.db != nil {
		a.db.Close()
	}
	if a.orm != nil {
		_ = a.orm.Close()
	}
	_ = a.log.Sync()
}
