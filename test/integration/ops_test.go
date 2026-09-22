//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	opsinfra "github.com/xrodriguezb/fulcrum/internal/ops/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// The operational snapshot is what an operator reads during an incident, so the
// numbers have to come from the same rows the rest of the system writes.
func TestOperationalSnapshotReflectsRealRows(t *testing.T) {
	pool := newPool(t)
	manager := postgres.NewTxManager(pool)
	reader := opsinfra.NewReader(manager)

	empty, err := reader.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if empty.Outbox.Pending != 0 || empty.DeadLetters != 0 || empty.StuckIdempotencyKeys != 0 {
		t.Fatalf("an empty database reported %+v", empty)
	}

	seedOutbox(t, pool, 3)
	if _, err := pool.Exec(t.Context(),
		`UPDATE outbox_events SET attempts = 2, last_error = 'broker unreachable'
		 WHERE id = (SELECT id FROM outbox_events LIMIT 1)`); err != nil {
		t.Fatalf("mark one event failing: %v", err)
	}

	// A claim that never completed is the visible trace of a crash between the
	// idempotency claim and the business transaction.
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO idempotency_keys (key, request_fingerprint, status, created_at, expires_at)
		 VALUES ('stuck-key', '\x00', 'in_progress', now() - interval '10 minutes', now() + interval '1 hour')`); err != nil {
		t.Fatalf("insert a stuck key: %v", err)
	}

	if _, err := pool.Exec(t.Context(),
		`INSERT INTO dead_letter_events
		   (id, event_id, consumer_name, event_type, payload, attempts, first_failed_at, last_failed_at, failure_reason)
		 VALUES (gen_random_uuid(), gen_random_uuid(), 'order-projector', 'order.created', '{}'::jsonb, 5,
		         now() - interval '5 minutes', now(), 'The event failed repeatedly and exhausted its retry budget.')`); err != nil {
		t.Fatalf("insert a dead letter: %v", err)
	}

	snapshot, err := reader.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Outbox.Pending != 3 {
		t.Errorf("pending = %d, want 3", snapshot.Outbox.Pending)
	}
	if snapshot.Outbox.Failing != 1 {
		t.Errorf("failing = %d, want 1", snapshot.Outbox.Failing)
	}
	if snapshot.DeadLetters != 1 {
		t.Errorf("dead letters = %d, want 1", snapshot.DeadLetters)
	}
	if snapshot.StuckIdempotencyKeys != 1 {
		t.Errorf("stuck keys = %d, want 1", snapshot.StuckIdempotencyKeys)
	}
	if snapshot.Outbox.OldestUnpublishedAge <= 0 {
		t.Errorf("the oldest unpublished age is %v, want a positive duration", snapshot.Outbox.OldestUnpublishedAge)
	}
	if snapshot.ObservedAt.IsZero() || time.Since(snapshot.ObservedAt) > time.Minute {
		t.Errorf("observed at = %v, want a recent instant", snapshot.ObservedAt)
	}

	entries, total, err := reader.DeadLetters(t.Context(), 10, 0)
	if err != nil {
		t.Fatalf("dead letters: %v", err)
	}
	if total != 1 || len(entries) != 1 {
		t.Fatalf("dead letters returned %d of %d", len(entries), total)
	}
	if entries[0].ConsumerName != "order-projector" {
		t.Errorf("consumer name = %q", entries[0].ConsumerName)
	}
	for _, leak := range []string{"pq:", "SQLSTATE", "goroutine"} {
		if strings.Contains(entries[0].FailureReason, leak) {
			t.Errorf("the stored reason leaked %q: %s", leak, entries[0].FailureReason)
		}
	}
}

// A partial rollback has to be possible, not only a full one, or an operator
// facing a bad migration has to take the whole schema down to undo it.
func TestMigrationsCanBeReversedOneStepAtATime(t *testing.T) {
	pool := newPool(t)
	ctx := t.Context()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()

	migrator, err := postgres.NewMigrator(conn)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}

	before, err := migrator.AppliedVersions(ctx)
	if err != nil {
		t.Fatalf("applied versions: %v", err)
	}
	if len(before) < 2 {
		t.Fatalf("the test needs at least two applied migrations, found %d", len(before))
	}

	if err := migrator.Down(ctx, 1); err != nil {
		t.Fatalf("down one step: %v", err)
	}

	after, err := migrator.AppliedVersions(ctx)
	if err != nil {
		t.Fatalf("applied versions after one step: %v", err)
	}
	if len(after) != len(before)-1 {
		t.Errorf("one step removed %d migrations, want 1", len(before)-len(after))
	}
	if after[len(after)-1] != before[len(before)-2] {
		t.Errorf("the wrong migration was reversed: now at %d, want %d",
			after[len(after)-1], before[len(before)-2])
	}

	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
}
