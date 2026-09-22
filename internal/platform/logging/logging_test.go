package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatalf("no log line was written")
	}
	// Only the last record matters to the assertions below.
	parts := strings.Split(line, "\n")
	var record map[string]any
	if err := json.Unmarshal([]byte(parts[len(parts)-1]), &record); err != nil {
		t.Fatalf("log line is not valid json: %v (%q)", err, parts[len(parts)-1])
	}
	return record
}

func TestContextIdentifiersAreAttachedToEveryRecord(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := logging.NewJSON(&buf, slog.LevelInfo)

	ctx := logging.WithRequestID(context.Background(), "req-1")
	ctx = logging.WithCorrelationID(ctx, "corr-1")
	ctx = logging.WithTraceID(ctx, "4bf92f3577b34da6a3ce929d0e0e4736")

	logger.InfoContext(ctx, "order created")

	record := decode(t, &buf)
	if record["request_id"] != "req-1" {
		t.Errorf("request_id = %v, want req-1", record["request_id"])
	}
	if record["correlation_id"] != "corr-1" {
		t.Errorf("correlation_id = %v, want corr-1", record["correlation_id"])
	}
	if record["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace_id = %v, want the propagated trace id", record["trace_id"])
	}
	if record["msg"] != "order created" {
		t.Errorf("msg = %v, want the logged message", record["msg"])
	}
}

func TestRecordsWithoutContextIdentifiersStayClean(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := logging.NewJSON(&buf, slog.LevelInfo)
	logger.InfoContext(context.Background(), "started")

	record := decode(t, &buf)
	for _, key := range []string{"request_id", "correlation_id", "trace_id"} {
		if _, present := record[key]; present {
			t.Errorf("key %q must be absent when it was never set, got %v", key, record[key])
		}
	}
}

func TestLevelIsHonoured(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := logging.NewJSON(&buf, slog.LevelWarn)
	logger.InfoContext(context.Background(), "this must not appear")

	if strings.TrimSpace(buf.String()) != "" {
		t.Errorf("a record below the configured level was written: %q", buf.String())
	}
}

func TestAccessorsReturnEmptyWhenUnset(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if got := logging.RequestID(ctx); got != "" {
		t.Errorf("RequestID = %q, want empty", got)
	}
	if got := logging.CorrelationID(ctx); got != "" {
		t.Errorf("CorrelationID = %q, want empty", got)
	}
	if got := logging.TraceID(ctx); got != "" {
		t.Errorf("TraceID = %q, want empty", got)
	}
}

// An idempotency key is chosen by the client and can carry meaning, so it is
// never logged whole. The redacted form still has to be stable enough to
// correlate two log lines about the same key.
func TestRedactKeyHidesTheKeyAndStaysStable(t *testing.T) {
	t.Parallel()

	const key = "customer-42-order-2026-09-21-attempt-1"

	first := logging.RedactKey(key)
	second := logging.RedactKey(key)

	if first != second {
		t.Errorf("redaction is not stable: %q then %q", first, second)
	}
	if strings.Contains(first, key) {
		t.Errorf("redacted form %q still contains the key", first)
	}
	if strings.Contains(first, "attempt-1") {
		t.Errorf("redacted form %q still contains the tail of the key", first)
	}
	if first == logging.RedactKey(key+"x") {
		t.Errorf("two different keys must not redact to the same value")
	}
	if logging.RedactKey("") != "" {
		t.Errorf("an empty key redacts to empty")
	}
}
