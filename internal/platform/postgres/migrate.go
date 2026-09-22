package postgres

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/xrodriguezb/fulcrum/migrations"
)

// ErrInvalidMigrationSet is returned when the migration files on disk do not
// form a usable set. It is a distinct sentinel because this failure is a
// packaging mistake, not a runtime condition.
var ErrInvalidMigrationSet = errors.New("invalid migration set")

// migrationFileName matches 0001_create_orders.up.sql and its down counterpart.
var migrationFileName = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.(up|down)\.sql$`)

// advisoryLockID namespaces the lock this runner takes. Two API instances
// starting at the same moment must not apply the same migration twice, and
// PostgreSQL advisory locks are the cheapest way to serialize that without an
// external coordinator.
const advisoryLockID int64 = 6835121

// Migration is one versioned schema change and its reversal.
type Migration struct {
	Version int
	Name    string
	Up      string
	Down    string
}

// ParseMigrations reads and validates a migration set. Every version needs both
// directions, versions are unique, and statements are non-empty.
func ParseMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read migration directory: %w", ErrInvalidMigrationSet, err)
	}

	type pair struct {
		name string
		up   string
		down string
	}
	byVersion := map[int]*pair{}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := migrationFileName.FindStringSubmatch(path.Base(entry.Name()))
		if match == nil {
			return nil, fmt.Errorf("%w: file name %q must look like 0001_create_orders.up.sql",
				ErrInvalidMigrationSet, entry.Name())
		}

		version, convErr := strconv.Atoi(match[1])
		if convErr != nil || version <= 0 {
			return nil, fmt.Errorf("%w: file name %q carries an unusable version",
				ErrInvalidMigrationSet, entry.Name())
		}

		content, readErr := fs.ReadFile(fsys, entry.Name())
		if readErr != nil {
			return nil, fmt.Errorf("%w: cannot read %q: %w", ErrInvalidMigrationSet, entry.Name(), readErr)
		}
		if strings.TrimSpace(string(content)) == "" {
			return nil, fmt.Errorf("%w: %q is empty", ErrInvalidMigrationSet, entry.Name())
		}

		existing, ok := byVersion[version]
		if !ok {
			existing = &pair{name: match[2]}
			byVersion[version] = existing
		}
		if existing.name != match[2] {
			return nil, fmt.Errorf("%w: duplicate version %d used by %q and %q",
				ErrInvalidMigrationSet, version, existing.name, match[2])
		}

		if match[3] == "up" {
			if existing.up != "" {
				return nil, fmt.Errorf("%w: duplicate up migration for version %d", ErrInvalidMigrationSet, version)
			}
			existing.up = string(content)
		} else {
			if existing.down != "" {
				return nil, fmt.Errorf("%w: duplicate down migration for version %d", ErrInvalidMigrationSet, version)
			}
			existing.down = string(content)
		}
	}

	out := make([]Migration, 0, len(byVersion))
	for version, p := range byVersion {
		if p.up == "" {
			return nil, fmt.Errorf("%w: version %d has no up migration", ErrInvalidMigrationSet, version)
		}
		if p.down == "" {
			return nil, fmt.Errorf("%w: version %d has no down migration", ErrInvalidMigrationSet, version)
		}
		out = append(out, Migration{Version: version, Name: p.name, Up: p.up, Down: p.down})
	}

	// Numeric ordering, not lexical: 0010 must follow 0002.
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// EmbeddedMigrations returns the migration set compiled into the binary.
func EmbeddedMigrations() ([]Migration, error) {
	return ParseMigrations(migrations.FS)
}

// Conn is the subset of pgx used by the runner. Accepting an interface lets the
// runner work with a pool, a single connection, or a transaction.
type Conn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Migrator applies and reverses migrations against a database.
type Migrator struct {
	conn       Conn
	migrations []Migration
}

// NewMigrator builds a migrator over the embedded set.
func NewMigrator(conn Conn) (*Migrator, error) {
	set, err := EmbeddedMigrations()
	if err != nil {
		return nil, err
	}
	return &Migrator{conn: conn, migrations: set}, nil
}

// NewMigratorWith builds a migrator over an explicit set, which the integration
// tests use to exercise partial sets.
func NewMigratorWith(conn Conn, set []Migration) *Migrator {
	return &Migrator{conn: conn, migrations: set}
}

// Up applies every migration that has not been applied yet, in version order.
func (m *Migrator) Up(ctx context.Context) error {
	if err := m.ensureSchemaTable(ctx); err != nil {
		return err
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	applied, err := m.appliedVersions(ctx)
	if err != nil {
		return err
	}

	for _, migration := range m.migrations {
		if applied[migration.Version] {
			continue
		}
		if _, execErr := m.conn.Exec(ctx, migration.Up); execErr != nil {
			return fmt.Errorf("apply migration %04d_%s: %w", migration.Version, migration.Name, execErr)
		}
		const record = `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`
		if _, execErr := m.conn.Exec(ctx, record, migration.Version, migration.Name); execErr != nil {
			return fmt.Errorf("record migration %04d_%s: %w", migration.Version, migration.Name, execErr)
		}
	}
	return nil
}

// Down reverses the given number of applied migrations, newest first. A steps
// value of zero reverses everything, which is what the migration test uses.
func (m *Migrator) Down(ctx context.Context, steps int) error {
	if err := m.ensureSchemaTable(ctx); err != nil {
		return err
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	applied, err := m.appliedVersions(ctx)
	if err != nil {
		return err
	}

	reversed := make([]Migration, len(m.migrations))
	copy(reversed, m.migrations)
	sort.Slice(reversed, func(i, j int) bool { return reversed[i].Version > reversed[j].Version })

	remaining := steps
	for _, migration := range reversed {
		if steps > 0 && remaining == 0 {
			break
		}
		if !applied[migration.Version] {
			continue
		}
		if _, execErr := m.conn.Exec(ctx, migration.Down); execErr != nil {
			return fmt.Errorf("revert migration %04d_%s: %w", migration.Version, migration.Name, execErr)
		}
		const forget = `DELETE FROM schema_migrations WHERE version = $1`
		if _, execErr := m.conn.Exec(ctx, forget, migration.Version); execErr != nil {
			return fmt.Errorf("forget migration %04d_%s: %w", migration.Version, migration.Name, execErr)
		}
		remaining--
	}
	return nil
}

// AppliedVersions reports which versions the database believes are applied.
func (m *Migrator) AppliedVersions(ctx context.Context) ([]int, error) {
	if err := m.ensureSchemaTable(ctx); err != nil {
		return nil, err
	}
	applied, err := m.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(applied))
	for version := range applied {
		out = append(out, version)
	}
	sort.Ints(out)
	return out, nil
}

func (m *Migrator) ensureSchemaTable(ctx context.Context) error {
	const create = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version     int         PRIMARY KEY,
  name        text        NOT NULL,
  applied_at  timestamptz NOT NULL DEFAULT now()
)`
	if _, err := m.conn.Exec(ctx, create); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

func (m *Migrator) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := m.conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if scanErr := rows.Scan(&version); scanErr != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", scanErr)
		}
		applied[version] = true
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", rows.Err())
	}
	return applied, nil
}

// lock serializes concurrent migration attempts across instances. The returned
// function releases the lock and is safe to call once.
func (m *Migrator) lock(ctx context.Context) (func(), error) {
	if _, err := m.conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); err != nil {
		return nil, fmt.Errorf("take migration lock: %w", err)
	}
	return func() {
		// The unlock runs on a background context on purpose: if the caller's
		// context is already cancelled, the lock still has to be released.
		if _, err := m.conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, advisoryLockID); err != nil {
			_ = err // Releasing is best effort; the session ending releases it anyway.
		}
	}, nil
}
