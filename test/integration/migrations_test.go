//go:build integration

package integration

import (
	"testing"

	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// expectedTables is every table the application owns. A table added without
// being listed here is a table whose rollback nobody checked.
var expectedTables = []string{
	"orders", "order_lines", "inventory_items", "idempotency_keys",
	"outbox_events", "processed_events", "dead_letter_events",
}

// The schema has to be reachable from nothing and reversible back to nothing.
// A migration set that only works forward is a migration set nobody can roll
// back under pressure.
func TestMigrationsApplyAndReverseFromEmpty(t *testing.T) {
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

	applied, err := migrator.AppliedVersions(ctx)
	if err != nil {
		t.Fatalf("applied versions: %v", err)
	}
	if len(applied) == 0 {
		t.Fatalf("no migration was applied")
	}

	for _, table := range expectedTables {
		var exists bool
		const query = `SELECT to_regclass($1) IS NOT NULL`
		if err := conn.QueryRow(ctx, query, table).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s does not exist after migrating up", table)
		}
	}

	if err := migrator.Down(ctx, 0); err != nil {
		t.Fatalf("migrate down: %v", err)
	}

	for _, table := range expectedTables {
		var exists bool
		const query = `SELECT to_regclass($1) IS NOT NULL`
		if err := conn.QueryRow(ctx, query, table).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if exists {
			t.Errorf("table %s still exists after migrating down", table)
		}
	}

	remaining, err := migrator.AppliedVersions(ctx)
	if err != nil {
		t.Fatalf("applied versions after down: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("schema_migrations still lists %v after a full rollback", remaining)
	}

	// Leave the database usable for the rest of the package.
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("re-apply migrations: %v", err)
	}
}
