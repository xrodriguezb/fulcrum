package postgres_test

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

func TestParseMigrationsOrdersByVersion(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"0002_add_outbox.up.sql":      {Data: []byte("CREATE TABLE outbox_events ();")},
		"0002_add_outbox.down.sql":    {Data: []byte("DROP TABLE outbox_events;")},
		"0010_add_dlq.up.sql":         {Data: []byte("CREATE TABLE dead_letter_events ();")},
		"0010_add_dlq.down.sql":       {Data: []byte("DROP TABLE dead_letter_events;")},
		"0001_create_orders.up.sql":   {Data: []byte("CREATE TABLE orders ();")},
		"0001_create_orders.down.sql": {Data: []byte("DROP TABLE orders;")},
	}

	migrations, err := postgres.ParseMigrations(fsys)
	if err != nil {
		t.Fatalf("ParseMigrations returned %v", err)
	}
	if len(migrations) != 3 {
		t.Fatalf("parsed %d migrations, want 3", len(migrations))
	}

	// Lexical ordering would put 0010 before 0002, which is exactly the bug this
	// assertion exists to prevent.
	wantVersions := []int{1, 2, 10}
	for i, want := range wantVersions {
		if migrations[i].Version != want {
			t.Errorf("migrations[%d].Version = %d, want %d", i, migrations[i].Version, want)
		}
	}
	if migrations[0].Name != "create_orders" {
		t.Errorf("migrations[0].Name = %q, want create_orders", migrations[0].Name)
	}
	if !strings.Contains(migrations[0].Up, "CREATE TABLE orders") {
		t.Errorf("up statement not loaded, got %q", migrations[0].Up)
	}
	if !strings.Contains(migrations[0].Down, "DROP TABLE orders") {
		t.Errorf("down statement not loaded, got %q", migrations[0].Down)
	}
}

func TestParseMigrationsRejectsBrokenSets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		fsys   fstest.MapFS
		wantIn string
	}{
		{
			name: "missing down migration",
			fsys: fstest.MapFS{
				"0001_create_orders.up.sql": {Data: []byte("CREATE TABLE orders ();")},
			},
			wantIn: "down",
		},
		{
			name: "missing up migration",
			fsys: fstest.MapFS{
				"0001_create_orders.down.sql": {Data: []byte("DROP TABLE orders;")},
			},
			wantIn: "up",
		},
		{
			name: "duplicate version",
			fsys: fstest.MapFS{
				"0001_create_orders.up.sql":   {Data: []byte("CREATE TABLE orders ();")},
				"0001_create_orders.down.sql": {Data: []byte("DROP TABLE orders;")},
				"0001_create_lines.up.sql":    {Data: []byte("CREATE TABLE order_lines ();")},
				"0001_create_lines.down.sql":  {Data: []byte("DROP TABLE order_lines;")},
			},
			wantIn: "duplicate",
		},
		{
			name: "unparsable file name",
			fsys: fstest.MapFS{
				"create_orders.up.sql": {Data: []byte("CREATE TABLE orders ();")},
			},
			wantIn: "name",
		},
		{
			name: "empty statement",
			fsys: fstest.MapFS{
				"0001_create_orders.up.sql":   {Data: []byte("   \n")},
				"0001_create_orders.down.sql": {Data: []byte("DROP TABLE orders;")},
			},
			wantIn: "empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := postgres.ParseMigrations(tc.fsys)
			if err == nil {
				t.Fatalf("expected an error, got none")
			}
			if !errors.Is(err, postgres.ErrInvalidMigrationSet) {
				t.Errorf("error %v does not unwrap to ErrInvalidMigrationSet", err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantIn) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantIn)
			}
		})
	}
}

// The embedded set ships with the binary, so a malformed file must fail the unit
// suite rather than the first deployment.
func TestEmbeddedMigrationsAreWellFormed(t *testing.T) {
	t.Parallel()

	migrations, err := postgres.EmbeddedMigrations()
	if err != nil {
		t.Fatalf("the embedded migration set is invalid: %v", err)
	}
	seen := map[int]bool{}
	for _, m := range migrations {
		if seen[m.Version] {
			t.Errorf("version %d appears twice", m.Version)
		}
		seen[m.Version] = true
	}
}
