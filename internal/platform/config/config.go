// Package config parses and validates process configuration. A service that
// cannot be configured correctly must not start at all, so every problem is
// collected and reported at once rather than surfacing one restart at a time.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// LookupFunc resolves an environment variable. Injecting it keeps the loader
// testable in parallel, which reading os.Getenv directly would not allow.
type LookupFunc func(key string) (string, bool)

// OSLookup reads the process environment.
func OSLookup(key string) (string, bool) { return os.LookupEnv(key) }

// Environment names the deployment mode. It drives defaults that must be safe in
// production even when an operator forgets to set them.
type Environment string

const (
	// EnvDevelopment relaxes CORS and enables human readable logs.
	EnvDevelopment Environment = "development"
	// EnvProduction requires explicit origins and structured logs.
	EnvProduction Environment = "production"
)

// Config is the fully resolved configuration of either binary.
type Config struct {
	Env         Environment
	ServiceName string
	LogLevel    slog.Level
	HTTP        HTTPConfig
	Worker      WorkerConfig
	Postgres    PostgresConfig
	NATS        NATSConfig
	Outbox      OutboxConfig
	Consumer    ConsumerConfig
	Idempotency IdempotencyConfig
	Telemetry   TelemetryConfig
	Profiling   ProfilingConfig
}

// ProfilingConfig decides whether the runtime profiles are served. The address
// is not configurable: the listener binds loopback, because a profile endpoint
// is an information leak and a denial of service in one url.
type ProfilingConfig struct {
	Enabled bool
	Port    int
}

// HTTPConfig configures the API server. Every timeout is explicit because the
// zero value of http.Server means "wait forever", which is never what is wanted.
type HTTPConfig struct {
	Port               int
	ReadHeaderTimeout  time.Duration
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
	ShutdownTimeout    time.Duration
	MaxBodyBytes       int64
	CORSAllowedOrigins []string
}

// WorkerConfig configures the worker's own operational listener. The worker owns
// the outbox and consumer metrics, so it has to expose them itself.
type WorkerConfig struct {
	MetricsPort     int
	ShutdownTimeout time.Duration
}

// PostgresConfig configures the connection pool.
type PostgresConfig struct {
	URL string
	// MigrationURL is the connection used once at startup to apply migrations.
	// It is separate because applying a migration needs rights the request path
	// must not have: the application role can read and write rows and nothing
	// else. When it is unset the application url is used, which is what a local
	// developer wants and what a production deployment should override.
	MigrationURL    string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

// NATSConfig configures the broker client and the JetStream stream it owns.
type NATSConfig struct {
	URL            string
	StreamName     string
	SubjectPrefix  string
	ConnectTimeout time.Duration
	ReconnectWait  time.Duration
	MaxReconnects  int
	PublishTimeout time.Duration
}

// OutboxConfig configures the publisher. Workers and BatchSize together bound the
// number of rows that can be in flight, which is what makes backpressure real.
type OutboxConfig struct {
	Workers      int
	BatchSize    int
	PollInterval time.Duration
	BackoffBase  time.Duration
	BackoffCap   time.Duration
	MaxAttempts  int
	DrainTimeout time.Duration
	// ClaimLease is how long a claimed row stays invisible to other publisher
	// instances. It has to outlast a publish, or a second instance will claim a
	// row that is still being published by the first.
	ClaimLease time.Duration
}

// ConsumerConfig configures the event consumer and its retry policy.
type ConsumerConfig struct {
	Name         string
	MaxAttempts  int
	RetryBase    time.Duration
	RetryCap     time.Duration
	AckWait      time.Duration
	FetchBatch   int
	DrainTimeout time.Duration
}

// IdempotencyConfig configures key retention and the sweep that enforces it.
type IdempotencyConfig struct {
	TTL           time.Duration
	SweepInterval time.Duration
	MaxKeyLength  int
}

// TelemetryConfig configures tracing. The stdout exporter keeps the development
// environment to five containers instead of nine.
type TelemetryConfig struct {
	TracingEnabled bool
	Exporter       string
	OTLPEndpoint   string
	SampleRatio    float64
}

// MigrationDSN returns the connection string migrations should use.
func (c Config) MigrationDSN() string {
	if strings.TrimSpace(c.Postgres.MigrationURL) != "" {
		return c.Postgres.MigrationURL
	}
	return c.Postgres.URL
}

// IsDevelopment reports whether relaxed development behaviour applies.
func (c Config) IsDevelopment() bool { return c.Env == EnvDevelopment }

// maxPoolSize bounds the pool so the value fits the width pgx uses and so a typo
// cannot ask PostgreSQL for more connections than it will ever grant.
const maxPoolSize = 1000

// ErrInvalidConfig is the sentinel every configuration failure unwraps to.
var ErrInvalidConfig = errors.New("invalid configuration")

// collector accumulates problems so that one restart reveals all of them.
// Values are never recorded, only variable names and the reason, because a
// connection string carries a password.
type collector struct {
	problems []string
}

func (c *collector) add(key, reason string) {
	c.problems = append(c.problems, fmt.Sprintf("%s %s", key, reason))
}

func (c *collector) err() error {
	if len(c.problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(c.problems, "; "))
}

// Load resolves configuration from the given lookup, applying defaults and
// validating every value. It returns every problem it finds, not the first.
func Load(lookup LookupFunc) (Config, error) {
	var c collector
	cfg := Config{}

	cfg.Env = environment(lookup, &c)
	cfg.ServiceName = stringValue(lookup, "SERVICE_NAME", "fulcrum")
	cfg.LogLevel = logLevel(lookup, &c)

	cfg.Postgres = PostgresConfig{
		URL:             requiredString(lookup, "DATABASE_URL", &c),
		MigrationURL:    stringValue(lookup, "MIGRATION_DATABASE_URL", ""),
		MaxConns:        poolSize(lookup, "POSTGRES_MAX_CONNS", 10, &c),
		MinConns:        poolSize(lookup, "POSTGRES_MIN_CONNS", 2, &c),
		MaxConnLifetime: duration(lookup, "POSTGRES_MAX_CONN_LIFETIME", time.Hour, &c),
		MaxConnIdleTime: duration(lookup, "POSTGRES_MAX_CONN_IDLE_TIME", 30*time.Minute, &c),
		ConnectTimeout:  duration(lookup, "POSTGRES_CONNECT_TIMEOUT", 5*time.Second, &c),
	}

	cfg.NATS = NATSConfig{
		URL:            requiredString(lookup, "NATS_URL", &c),
		StreamName:     stringValue(lookup, "NATS_STREAM", "FULCRUM"),
		SubjectPrefix:  stringValue(lookup, "NATS_SUBJECT_PREFIX", "fulcrum.events"),
		ConnectTimeout: duration(lookup, "NATS_CONNECT_TIMEOUT", 5*time.Second, &c),
		ReconnectWait:  duration(lookup, "NATS_RECONNECT_WAIT", 2*time.Second, &c),
		MaxReconnects:  intValue(lookup, "NATS_MAX_RECONNECTS", -1, &c),
		PublishTimeout: duration(lookup, "NATS_PUBLISH_TIMEOUT", 5*time.Second, &c),
	}

	cfg.HTTP = HTTPConfig{
		Port:               port(lookup, "HTTP_PORT", 8080, &c),
		ReadHeaderTimeout:  duration(lookup, "HTTP_READ_HEADER_TIMEOUT", 5*time.Second, &c),
		ReadTimeout:        duration(lookup, "HTTP_READ_TIMEOUT", 15*time.Second, &c),
		WriteTimeout:       duration(lookup, "HTTP_WRITE_TIMEOUT", 30*time.Second, &c),
		IdleTimeout:        duration(lookup, "HTTP_IDLE_TIMEOUT", 60*time.Second, &c),
		ShutdownTimeout:    duration(lookup, "HTTP_SHUTDOWN_TIMEOUT", 15*time.Second, &c),
		MaxBodyBytes:       int64(intValue(lookup, "HTTP_MAX_BODY_BYTES", 65536, &c)),
		CORSAllowedOrigins: corsOrigins(lookup, cfg.Env, &c),
	}

	cfg.Profiling = ProfilingConfig{
		Enabled: boolValue(lookup, "PPROF_ENABLED", false, &c),
		Port:    port(lookup, "PPROF_PORT", 6060, &c),
	}

	cfg.Worker = WorkerConfig{
		MetricsPort:     port(lookup, "WORKER_METRICS_PORT", 8081, &c),
		ShutdownTimeout: duration(lookup, "WORKER_SHUTDOWN_TIMEOUT", 20*time.Second, &c),
	}

	cfg.Outbox = OutboxConfig{
		Workers:      positiveInt(lookup, "OUTBOX_WORKERS", 4, &c),
		BatchSize:    positiveInt(lookup, "OUTBOX_BATCH_SIZE", 100, &c),
		PollInterval: duration(lookup, "OUTBOX_POLL_INTERVAL", 500*time.Millisecond, &c),
		BackoffBase:  duration(lookup, "OUTBOX_BACKOFF_BASE", 200*time.Millisecond, &c),
		BackoffCap:   duration(lookup, "OUTBOX_BACKOFF_CAP", 30*time.Second, &c),
		MaxAttempts:  positiveInt(lookup, "OUTBOX_MAX_ATTEMPTS", 10, &c),
		DrainTimeout: duration(lookup, "OUTBOX_DRAIN_TIMEOUT", 15*time.Second, &c),
		ClaimLease:   duration(lookup, "OUTBOX_CLAIM_LEASE", 30*time.Second, &c),
	}

	cfg.Consumer = ConsumerConfig{
		Name:         stringValue(lookup, "CONSUMER_NAME", "order-projector"),
		MaxAttempts:  positiveInt(lookup, "CONSUMER_MAX_ATTEMPTS", 5, &c),
		RetryBase:    duration(lookup, "CONSUMER_RETRY_BASE", 100*time.Millisecond, &c),
		RetryCap:     duration(lookup, "CONSUMER_RETRY_CAP", 30*time.Second, &c),
		AckWait:      duration(lookup, "CONSUMER_ACK_WAIT", 30*time.Second, &c),
		FetchBatch:   positiveInt(lookup, "CONSUMER_FETCH_BATCH", 25, &c),
		DrainTimeout: duration(lookup, "CONSUMER_DRAIN_TIMEOUT", 15*time.Second, &c),
	}

	cfg.Idempotency = IdempotencyConfig{
		TTL:           duration(lookup, "IDEMPOTENCY_TTL", 24*time.Hour, &c),
		SweepInterval: duration(lookup, "IDEMPOTENCY_SWEEP_INTERVAL", 10*time.Minute, &c),
		MaxKeyLength:  positiveInt(lookup, "IDEMPOTENCY_MAX_KEY_LENGTH", 255, &c),
	}

	cfg.Telemetry = TelemetryConfig{
		TracingEnabled: boolValue(lookup, "OTEL_TRACING_ENABLED", true, &c),
		Exporter:       stringValue(lookup, "OTEL_EXPORTER", "stdout"),
		OTLPEndpoint:   stringValue(lookup, "OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		SampleRatio:    floatValue(lookup, "OTEL_SAMPLE_RATIO", 1.0, &c),
	}

	crossValidate(cfg, &c)

	if err := c.err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadFromEnv is the production entry point.
func LoadFromEnv() (Config, error) { return Load(OSLookup) }

func crossValidate(cfg Config, c *collector) {
	if cfg.Consumer.RetryCap > 0 && cfg.Consumer.RetryBase > cfg.Consumer.RetryCap {
		c.add("CONSUMER_RETRY_CAP", "must not be shorter than CONSUMER_RETRY_BASE")
	}
	if cfg.Outbox.BackoffCap > 0 && cfg.Outbox.BackoffBase > cfg.Outbox.BackoffCap {
		c.add("OUTBOX_BACKOFF_CAP", "must not be shorter than OUTBOX_BACKOFF_BASE")
	}
	if cfg.Postgres.MinConns > cfg.Postgres.MaxConns {
		c.add("POSTGRES_MIN_CONNS", "must not exceed POSTGRES_MAX_CONNS")
	}
	if cfg.Telemetry.Exporter != "stdout" && cfg.Telemetry.Exporter != "otlp" && cfg.Telemetry.Exporter != "none" {
		c.add("OTEL_EXPORTER", "must be one of stdout, otlp, none")
	}
	if cfg.Telemetry.Exporter == "otlp" && strings.TrimSpace(cfg.Telemetry.OTLPEndpoint) == "" {
		c.add("OTEL_EXPORTER_OTLP_ENDPOINT", "is required when OTEL_EXPORTER is otlp")
	}
	if cfg.Telemetry.SampleRatio < 0 || cfg.Telemetry.SampleRatio > 1 {
		c.add("OTEL_SAMPLE_RATIO", "must be between 0 and 1")
	}
	if cfg.HTTP.Port == cfg.Worker.MetricsPort {
		c.add("WORKER_METRICS_PORT", "must differ from HTTP_PORT")
	}
	if cfg.Profiling.Enabled && (cfg.Profiling.Port == cfg.HTTP.Port || cfg.Profiling.Port == cfg.Worker.MetricsPort) {
		c.add("PPROF_PORT", "must differ from HTTP_PORT and WORKER_METRICS_PORT")
	}
	// A lease shorter than a publish would let a second instance claim a row
	// that is still in flight, which is how an event gets published twice.
	if cfg.Outbox.ClaimLease > 0 && cfg.NATS.PublishTimeout >= cfg.Outbox.ClaimLease {
		c.add("OUTBOX_CLAIM_LEASE", "must be longer than NATS_PUBLISH_TIMEOUT")
	}
}

func environment(lookup LookupFunc, c *collector) Environment {
	raw := strings.TrimSpace(stringValue(lookup, "FULCRUM_ENV", string(EnvDevelopment)))
	switch Environment(raw) {
	case EnvDevelopment, EnvProduction:
		return Environment(raw)
	default:
		c.add("FULCRUM_ENV", "must be one of development, production")
		return EnvDevelopment
	}
}

func logLevel(lookup LookupFunc, c *collector) slog.Level {
	raw := strings.ToLower(strings.TrimSpace(stringValue(lookup, "LOG_LEVEL", "info")))
	switch raw {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		c.add("LOG_LEVEL", "must be one of debug, info, warn, error")
		return slog.LevelInfo
	}
}

func corsOrigins(lookup LookupFunc, env Environment, c *collector) []string {
	raw, ok := lookup("CORS_ALLOWED_ORIGINS")
	if !ok || strings.TrimSpace(raw) == "" {
		if env == EnvProduction {
			c.add("CORS_ALLOWED_ORIGINS", "is required in production")
			return nil
		}
		return []string{"http://localhost:5173"}
	}

	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin == "" {
			continue
		}
		// A wildcard origin cannot be combined with credentials, and an
		// operations console that talks to a private API has no reason to
		// accept requests from arbitrary sites.
		if origin == "*" && env == EnvProduction {
			c.add("CORS_ALLOWED_ORIGINS", "must not contain a wildcard in production")
			continue
		}
		origins = append(origins, origin)
	}
	if len(origins) == 0 && env == EnvProduction {
		c.add("CORS_ALLOWED_ORIGINS", "must list at least one origin in production")
	}
	return origins
}

func requiredString(lookup LookupFunc, key string, c *collector) string {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		c.add(key, "is required")
		return ""
	}
	return strings.TrimSpace(raw)
}

func stringValue(lookup LookupFunc, key, fallback string) string {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	return strings.TrimSpace(raw)
}

func duration(lookup LookupFunc, key string, fallback time.Duration, c *collector) time.Duration {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		c.add(key, "must be a duration such as 500ms or 30s")
		return fallback
	}
	if parsed <= 0 {
		c.add(key, "must be greater than zero")
		return fallback
	}
	return parsed
}

func intValue(lookup LookupFunc, key string, fallback int, c *collector) int {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		c.add(key, "must be an integer")
		return fallback
	}
	return parsed
}

func positiveInt(lookup LookupFunc, key string, fallback int, c *collector) int {
	value := intValue(lookup, key, fallback, c)
	if value <= 0 {
		c.add(key, "must be greater than zero")
		return fallback
	}
	return value
}

// poolSize parses a connection pool bound. The range check is what makes the
// conversion to int32 safe, since pgx sizes its pool with that width.
func poolSize(lookup LookupFunc, key string, fallback int32, c *collector) int32 {
	value := intValue(lookup, key, int(fallback), c)
	if value < 1 || value > maxPoolSize {
		c.add(key, fmt.Sprintf("must be between 1 and %d", maxPoolSize))
		return fallback
	}
	return int32(value)
}

func port(lookup LookupFunc, key string, fallback int, c *collector) int {
	value := intValue(lookup, key, fallback, c)
	if value < 1 || value > 65535 {
		c.add(key, "must be a TCP port between 1 and 65535")
		return fallback
	}
	return value
}

func boolValue(lookup LookupFunc, key string, fallback bool, c *collector) bool {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		c.add(key, "must be a boolean")
		return fallback
	}
	return parsed
}

func floatValue(lookup LookupFunc, key string, fallback float64, c *collector) float64 {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		c.add(key, "must be a number")
		return fallback
	}
	return parsed
}
