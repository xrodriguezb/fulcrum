package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/platform/config"
)

// valid is the smallest environment that must load. Tests copy it and mutate one
// key so that each case names exactly one reason to fail.
func valid() map[string]string {
	return map[string]string{
		"FULCRUM_ENV":  "development",
		"DATABASE_URL": "postgres://fulcrum:secret@localhost:5432/fulcrum?sslmode=disable",
		"NATS_URL":     "nats://localhost:4222",
	}
}

func lookupFrom(env map[string]string) config.LookupFunc {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

func TestLoadRejectsMissingRequiredVariables(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		remove  []string
		wantIn  []string
		wantOut []string
	}{
		{
			name:   "missing database url",
			remove: []string{"DATABASE_URL"},
			wantIn: []string{"DATABASE_URL"},
		},
		{
			name:   "missing broker url",
			remove: []string{"NATS_URL"},
			wantIn: []string{"NATS_URL"},
		},
		{
			name:   "every required variable is reported at once",
			remove: []string{"DATABASE_URL", "NATS_URL"},
			wantIn: []string{"DATABASE_URL", "NATS_URL"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := valid()
			for _, key := range tc.remove {
				delete(env, key)
			}

			_, err := config.Load(lookupFrom(env))
			if err == nil {
				t.Fatalf("expected an error, got none")
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err.Error(), want)
				}
			}
		})
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		set    map[string]string
		wantIn string
	}{
		{name: "unknown environment", set: map[string]string{"FULCRUM_ENV": "staging-ish"}, wantIn: "FULCRUM_ENV"},
		{name: "unparsable duration", set: map[string]string{"HTTP_READ_TIMEOUT": "soon"}, wantIn: "HTTP_READ_TIMEOUT"},
		{name: "non numeric port", set: map[string]string{"HTTP_PORT": "http"}, wantIn: "HTTP_PORT"},
		{name: "port out of range", set: map[string]string{"HTTP_PORT": "70000"}, wantIn: "HTTP_PORT"},
		{name: "zero publisher workers", set: map[string]string{"OUTBOX_WORKERS": "0"}, wantIn: "OUTBOX_WORKERS"},
		{name: "negative batch size", set: map[string]string{"OUTBOX_BATCH_SIZE": "-5"}, wantIn: "OUTBOX_BATCH_SIZE"},
		{name: "unknown log level", set: map[string]string{"LOG_LEVEL": "chatty"}, wantIn: "LOG_LEVEL"},
		{name: "empty database url", set: map[string]string{"DATABASE_URL": "   "}, wantIn: "DATABASE_URL"},
		{
			name:   "wildcard cors origin in production",
			set:    map[string]string{"FULCRUM_ENV": "production", "CORS_ALLOWED_ORIGINS": "*"},
			wantIn: "CORS_ALLOWED_ORIGINS",
		},
		{
			name:   "consumer retry cap below base delay",
			set:    map[string]string{"CONSUMER_RETRY_BASE": "5s", "CONSUMER_RETRY_CAP": "1s"},
			wantIn: "CONSUMER_RETRY_CAP",
		},
		{
			name:   "claim lease shorter than a publish",
			set:    map[string]string{"OUTBOX_CLAIM_LEASE": "2s", "NATS_PUBLISH_TIMEOUT": "5s"},
			wantIn: "OUTBOX_CLAIM_LEASE",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := valid()
			for k, v := range tc.set {
				env[k] = v
			}

			_, err := config.Load(lookupFrom(env))
			if err == nil {
				t.Fatalf("expected an error, got none")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantIn)
			}
		})
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(lookupFrom(valid()))
	if err != nil {
		t.Fatalf("expected the minimal environment to load, got %v", err)
	}

	if cfg.HTTP.Port != 8080 {
		t.Errorf("HTTP.Port = %d, want 8080", cfg.HTTP.Port)
	}
	if cfg.HTTP.ReadHeaderTimeout <= 0 || cfg.HTTP.ReadTimeout <= 0 ||
		cfg.HTTP.WriteTimeout <= 0 || cfg.HTTP.IdleTimeout <= 0 {
		t.Errorf("every HTTP timeout must have a non-zero default, got %+v", cfg.HTTP)
	}
	if cfg.HTTP.ShutdownTimeout <= 0 {
		t.Errorf("HTTP.ShutdownTimeout must have a non-zero default")
	}
	if cfg.HTTP.MaxBodyBytes <= 0 {
		t.Errorf("HTTP.MaxBodyBytes must have a non-zero default")
	}
	if cfg.Outbox.Workers <= 0 || cfg.Outbox.BatchSize <= 0 {
		t.Errorf("outbox defaults must be positive, got %+v", cfg.Outbox)
	}
	if cfg.Outbox.ClaimLease <= cfg.NATS.PublishTimeout {
		t.Errorf("the default claim lease %v must outlast the default publish timeout %v",
			cfg.Outbox.ClaimLease, cfg.NATS.PublishTimeout)
	}
	if cfg.Consumer.MaxAttempts <= 0 {
		t.Errorf("Consumer.MaxAttempts must have a non-zero default")
	}
	if cfg.Idempotency.TTL != 24*time.Hour {
		t.Errorf("Idempotency.TTL = %v, want 24h", cfg.Idempotency.TTL)
	}
	if !cfg.IsDevelopment() {
		t.Errorf("FULCRUM_ENV=development must report IsDevelopment")
	}
}

func TestLoadParsesExplicitValues(t *testing.T) {
	t.Parallel()

	env := valid()
	env["FULCRUM_ENV"] = "production"
	env["HTTP_PORT"] = "9090"
	env["HTTP_READ_TIMEOUT"] = "7s"
	env["OUTBOX_WORKERS"] = "8"
	env["OUTBOX_BATCH_SIZE"] = "250"
	env["CONSUMER_MAX_ATTEMPTS"] = "3"
	env["IDEMPOTENCY_TTL"] = "1h"
	env["CORS_ALLOWED_ORIGINS"] = "https://ops.example.com, https://admin.example.com"
	env["LOG_LEVEL"] = "warn"

	cfg, err := config.Load(lookupFrom(env))
	if err != nil {
		t.Fatalf("expected a valid environment to load, got %v", err)
	}

	if cfg.HTTP.Port != 9090 {
		t.Errorf("HTTP.Port = %d, want 9090", cfg.HTTP.Port)
	}
	if cfg.HTTP.ReadTimeout != 7*time.Second {
		t.Errorf("HTTP.ReadTimeout = %v, want 7s", cfg.HTTP.ReadTimeout)
	}
	if cfg.Outbox.Workers != 8 || cfg.Outbox.BatchSize != 250 {
		t.Errorf("outbox configuration not parsed, got %+v", cfg.Outbox)
	}
	if cfg.Consumer.MaxAttempts != 3 {
		t.Errorf("Consumer.MaxAttempts = %d, want 3", cfg.Consumer.MaxAttempts)
	}
	if cfg.Idempotency.TTL != time.Hour {
		t.Errorf("Idempotency.TTL = %v, want 1h", cfg.Idempotency.TTL)
	}
	want := []string{"https://ops.example.com", "https://admin.example.com"}
	if len(cfg.HTTP.CORSAllowedOrigins) != len(want) {
		t.Fatalf("CORSAllowedOrigins = %v, want %v", cfg.HTTP.CORSAllowedOrigins, want)
	}
	for i, origin := range want {
		if cfg.HTTP.CORSAllowedOrigins[i] != origin {
			t.Errorf("CORSAllowedOrigins[%d] = %q, want %q", i, cfg.HTTP.CORSAllowedOrigins[i], origin)
		}
	}
	if cfg.IsDevelopment() {
		t.Errorf("FULCRUM_ENV=production must not report IsDevelopment")
	}
}

// A configuration error must name the variable and stay free of the value, which
// can be a credential-bearing connection string.
func TestLoadErrorNeverEchoesValues(t *testing.T) {
	t.Parallel()

	env := valid()
	env["DATABASE_URL"] = "postgres://fulcrum:hunter2@localhost:5432/fulcrum"
	env["HTTP_PORT"] = "not-a-port"

	_, err := config.Load(lookupFrom(env))
	if err == nil {
		t.Fatalf("expected an error, got none")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("configuration error leaked a secret: %q", err.Error())
	}
	if strings.Contains(err.Error(), "not-a-port") {
		t.Errorf("configuration error echoed the offending value: %q", err.Error())
	}
}

// Profiling is off unless it is asked for, and its port has to be its own: two
// listeners on one port in one process is a startup failure that would only
// show up when profiling is switched on, which is exactly when nobody wants a
// second problem.
func TestProfilingIsDisabledByDefault(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(lookupFrom(valid()))
	if err != nil {
		t.Fatalf("Load returned %v", err)
	}
	if cfg.Profiling.Enabled {
		t.Errorf("profiling is enabled without being asked for")
	}
	if cfg.Profiling.Port != 6060 {
		t.Errorf("the default profiling port is %d, want 6060", cfg.Profiling.Port)
	}
}

func TestProfilingPortMustNotCollide(t *testing.T) {
	t.Parallel()

	env := valid()
	env["PPROF_ENABLED"] = "true"
	env["HTTP_PORT"] = "8080"
	env["PPROF_PORT"] = "8080"

	_, err := config.Load(lookupFrom(env))
	if err == nil {
		t.Fatalf("a profiling port equal to the http port was accepted")
	}
	if !strings.Contains(err.Error(), "PPROF_PORT") {
		t.Errorf("the failure does not name the offending variable: %v", err)
	}
}

func TestProfilingReadsItsSettings(t *testing.T) {
	t.Parallel()

	env := valid()
	env["PPROF_ENABLED"] = "true"
	env["PPROF_PORT"] = "7070"

	cfg, err := config.Load(lookupFrom(env))
	if err != nil {
		t.Fatalf("Load returned %v", err)
	}
	if !cfg.Profiling.Enabled || cfg.Profiling.Port != 7070 {
		t.Errorf("profiling read as %+v", cfg.Profiling)
	}
}
