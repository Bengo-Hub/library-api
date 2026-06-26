package config

import (
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
)

const namespace = ""

// Config aggregates runtime configuration for the library service.
type Config struct {
	App           AppConfig
	HTTP          HTTPConfig
	Postgres      PostgresConfig
	Redis         RedisConfig
	Events        EventsConfig
	Telemetry     TelemetryConfig
	Auth          AuthConfig
	Media         MediaConfig
	Services      ServicesConfig
	Backup        BackupConfig
	Subscriptions SubscriptionsConfig
}

// SubscriptionsConfig holds the subscriptions S2S client config used to gate
// mutations + NATS data sync by tenant entitlement. APIKey reuses the shared
// INTERNAL_SERVICE_KEY (same value every library S2S client uses).
type SubscriptionsConfig struct {
	ServiceURL     string        `envconfig:"SUBSCRIPTIONS_SERVICE_URL" default:"https://pricingapi.codevertexitsolutions.com"`
	RequestTimeout time.Duration `envconfig:"SUBSCRIPTIONS_REQUEST_TIMEOUT" default:"10s"`
	APIKey         string        `envconfig:"INTERNAL_SERVICE_KEY" default:""`
}

// BackupConfig controls the tenant-scoped backup scheduler + retention churn.
type BackupConfig struct {
	Dir             string `envconfig:"BACKUP_DIR" default:"/app/backups/library"`
	ScheduleEnabled bool   `envconfig:"BACKUP_SCHEDULE_ENABLED" default:"true"`
	ScheduleHour    int    `envconfig:"BACKUP_SCHEDULE_HOUR" default:"2"`
	RetentionDays   int    `envconfig:"BACKUP_RETENTION_DAYS" default:"4"`
}

// MediaConfig points at the per-tenant media root (cover images) and the e-book
// file store. Both live on a PVC; URLs are stored relative and resolved at read.
type MediaConfig struct {
	Root      string `envconfig:"MEDIA_ROOT" default:"./media"`
	URLBase   string `envconfig:"MEDIA_URL_BASE" default:""`
	EbookRoot string `envconfig:"EBOOK_ROOT" default:"./media/ebooks"`
}

type AppConfig struct {
	Name    string `envconfig:"APP_NAME" default:"library-service"`
	Env     string `envconfig:"APP_ENV" default:"development"`
	Region  string `envconfig:"APP_REGION" default:"africa-east-1"`
	Version string `envconfig:"APP_VERSION" default:"0.1.0"`
}

type HTTPConfig struct {
	Host           string        `envconfig:"HTTP_HOST" default:"0.0.0.0"`
	Port           int           `envconfig:"HTTP_PORT" default:"4010"`
	ReadTimeout    time.Duration `envconfig:"HTTP_READ_TIMEOUT" default:"20s"`
	WriteTimeout   time.Duration `envconfig:"HTTP_WRITE_TIMEOUT" default:"20s"`
	IdleTimeout    time.Duration `envconfig:"HTTP_IDLE_TIMEOUT" default:"90s"`
	TLSCertFile    string        `envconfig:"TLS_CERT_FILE"`
	TLSKeyFile     string        `envconfig:"TLS_KEY_FILE"`
	AllowedOrigins []string      `envconfig:"HTTP_ALLOWED_ORIGINS" default:"https://library.codevertexitsolutions.com,https://accounts.codevertexitsolutions.com"`
}

type PostgresConfig struct {
	URL                      string        `envconfig:"POSTGRES_URL" default:"postgres://postgres:postgres@localhost:5432/library?sslmode=disable"`
	MigrateURL               string        `envconfig:"POSTGRES_MIGRATE_URL" default:""`
	MaxOpenConns             int           `envconfig:"POSTGRES_MAX_OPEN_CONNS" default:"5"`
	MaxIdleConns             int           `envconfig:"POSTGRES_MAX_IDLE_CONNS" default:"3"`
	ConnMaxLifetime          time.Duration `envconfig:"POSTGRES_CONN_MAX_LIFETIME" default:"5m"`
	StatementTimeout         time.Duration `envconfig:"POSTGRES_STATEMENT_TIMEOUT" default:"30s"`
	IdleInTransactionTimeout time.Duration `envconfig:"POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT" default:"60s"`
	RunMigrations            bool          `envconfig:"POSTGRES_RUN_MIGRATIONS" default:"false"`
}

type RedisConfig struct {
	Addr        string        `envconfig:"REDIS_ADDR" default:"localhost:6380"`
	Username    string        `envconfig:"REDIS_USERNAME"`
	Password    string        `envconfig:"REDIS_PASSWORD"`
	DB          int           `envconfig:"REDIS_DB" default:"0"`
	TLSRequired bool          `envconfig:"REDIS_TLS_REQUIRED" default:"false"`
	DialTimeout time.Duration `envconfig:"REDIS_DIAL_TIMEOUT" default:"5s"`
}

type EventsConfig struct {
	Bus              string        `envconfig:"EVENT_BUS" default:"nats"`
	NATSURL          string        `envconfig:"EVENTS_NATS_URL" default:"nats://localhost:4222"`
	StreamName       string        `envconfig:"NATS_STREAM" default:"library"`
	DeliverGroup     string        `envconfig:"NATS_DELIVER_GROUP" default:"library-workers"`
	DeadLetterJet    string        `envconfig:"NATS_DLQ_STREAM" default:"library-dlq"`
	OutboxEnabled    bool          `envconfig:"OUTBOX_ENABLED" default:"true"`
	OutboxBatchSize  int           `envconfig:"OUTBOX_BATCH_SIZE" default:"100"`
	OutboxPollPeriod time.Duration `envconfig:"OUTBOX_POLL_PERIOD" default:"5s"`
}

type TelemetryConfig struct {
	OTLPEndpoint string `envconfig:"OTLP_ENDPOINT"`
	MetricsURL   string `envconfig:"METRICS_ENDPOINT"`
	TracingURL   string `envconfig:"TRACING_ENDPOINT"`
}

type ServicesConfig struct {
	// TreasuryURL is the treasury-api base — fines, e-book sales and membership fees
	// are charged via treasury payment intents (S2S).
	TreasuryURL string `envconfig:"TREASURY_SERVICE_URL" default:"https://booksapi.codevertexitsolutions.com"`
	// NotificationsURL is the notifications-api base — REST fallback when an outbox
	// event path is not used.
	NotificationsURL string `envconfig:"NOTIFICATIONS_SERVICE_URL" default:"https://notificationsapi.codevertexitsolutions.com"`
	// MarketflowURL is the marketflow (CRM) base — optional member profile enrich.
	MarketflowURL string `envconfig:"MARKETFLOW_SERVICE_URL" default:"https://marketflowapi.codevertexitsolutions.com"`
}

type AuthConfig struct {
	ServiceURL          string        `envconfig:"AUTH_SERVICE_URL" default:"https://sso.codevertexitsolutions.com"`
	Issuer              string        `envconfig:"AUTH_ISSUER" default:"https://sso.codevertexitsolutions.com"`
	Audience            string        `envconfig:"AUTH_AUDIENCE" default:"codevertex"`
	JWKSUrl             string        `envconfig:"AUTH_JWKS_URL" default:"https://sso.codevertexitsolutions.com/api/v1/.well-known/jwks.json"`
	JWKSCacheTTL        time.Duration `envconfig:"AUTH_JWKS_CACHE_TTL" default:"3600s"`
	JWKSRefreshInterval time.Duration `envconfig:"AUTH_JWKS_REFRESH_INTERVAL" default:"300s"`
	EnableAPIKeyAuth    bool          `envconfig:"AUTH_ENABLE_API_KEY_AUTH" default:"true"`
	APIKey              string        `envconfig:"INTERNAL_SERVICE_KEY" default:""`
	// TerminalJWTSecret signs short-lived terminal JWTs issued after a desk/kiosk PIN login
	// (supplements SSO). Leave empty to disable PIN auth.
	TerminalJWTSecret string `envconfig:"TERMINAL_JWT_SECRET" default:""`
}

// Load gathers configuration from environment variables and optional .env files.
func Load() (*Config, error) {
	_ = godotenv.Load()

	var cfg Config
	if err := envconfig.Process(namespace, &cfg); err != nil {
		return nil, fmt.Errorf("config: failed to load environment variables: %w", err)
	}

	return &cfg, nil
}
