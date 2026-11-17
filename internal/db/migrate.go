package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

// Migration is one forward-only schema change. Files are named
// NNNN_description.sql; NNNN is the version.
type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

// MigrationStatus describes an embedded migration and whether it is applied.
type MigrationStatus struct {
	Migration
	AppliedAt       time.Time // zero if pending
	ChecksumChanged bool      // applied, but the embedded file differs
}

// Migrator applies embedded migrations. It tracks state in schema_migrations.
type Migrator struct {
	migrations []Migration
}

// NewMigrator loads migrations from fsys (files matching *.sql at the root).
func NewMigrator(fsys fs.FS) (*Migrator, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: read dir: %w", err)
	}
	seen := map[int]string{}
	var ms []Migration
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		num, name, ok := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("migrate: bad file name %q (want NNNN_name.sql)", e.Name())
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrate: duplicate version %d (%s, %s)", v, prev, e.Name())
		}
		seen[v] = e.Name()
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(body)
		ms = append(ms, Migration{Version: v, Name: name, SQL: string(body), Checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })
	return &Migrator{migrations: ms}, nil
}

// DefaultMigrator returns a migrator over the migrations embedded in the binary.
func DefaultMigrator() *Migrator {
	sub, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		panic(err) // embedded FS layout is fixed at compile time
	}
	m, err := NewMigrator(sub)
	if err != nil {
		panic(err)
	}
	return m
}

const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  checksum   TEXT NOT NULL,
  applied_at INTEGER NOT NULL
)`

// Status reports every embedded migration and its state. Read-only: if the
// schema_migrations table does not exist yet, everything is pending.
func (m *Migrator) Status(ctx context.Context, q *sql.DB) ([]MigrationStatus, error) {
	applied := map[int]struct {
		checksum string
		at       int64
	}{}
	var exists int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("migrate: inspect schema: %w", err)
	}
	if exists == 1 {
		rows, err := q.QueryContext(ctx, `SELECT version, checksum, applied_at FROM schema_migrations`)
		if err != nil {
			return nil, fmt.Errorf("migrate: read state: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var v int
			var cs string
			var at int64
			if err := rows.Scan(&v, &cs, &at); err != nil {
				return nil, fmt.Errorf("migrate: scan state: %w", err)
			}
			applied[v] = struct {
				checksum string
				at       int64
			}{cs, at}
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("migrate: read state: %w", err)
		}
	}
	out := make([]MigrationStatus, 0, len(m.migrations))
	for _, mg := range m.migrations {
		st := MigrationStatus{Migration: mg}
		if a, ok := applied[mg.Version]; ok {
			st.AppliedAt = time.UnixMilli(a.at).UTC()
			st.ChecksumChanged = a.checksum != mg.Checksum
		}
		out = append(out, st)
	}
	return out, nil
}

// Pending returns migrations that are not applied yet.
func (m *Migrator) Pending(ctx context.Context, q *sql.DB) ([]Migration, error) {
	st, err := m.Status(ctx, q)
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, s := range st {
		if s.AppliedAt.IsZero() {
			out = append(out, s.Migration)
		}
	}
	return out, nil
}

// Up applies all pending migrations, each in its own transaction, and
// returns the ones that were applied.
func (m *Migrator) Up(ctx context.Context, w *sql.DB) ([]Migration, error) {
	if _, err := w.ExecContext(ctx, createMigrationsTable); err != nil {
		return nil, fmt.Errorf("migrate: create state table: %w", err)
	}
	pending, err := m.Pending(ctx, w)
	if err != nil {
		return nil, err
	}
	var done []Migration
	for _, mg := range pending {
		if err := applyOne(ctx, w, mg); err != nil {
			return done, err
		}
		done = append(done, mg)
	}
	return done, nil
}

func applyOne(ctx context.Context, w *sql.DB, mg Migration) error {
	tx, err := w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate %04d: begin: %w", mg.Version, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	bump := strings.Contains(mg.SQL, "-- kadr:bump-schema-version")
	var schemaVersion int64
	if bump {
		if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
			return fmt.Errorf("migrate %04d: read schema_version: %w", mg.Version, err)
		}
	}
	if _, err := tx.ExecContext(ctx, mg.SQL); err != nil {
		return fmt.Errorf("migrate %04d_%s: %w", mg.Version, mg.Name, err)
	}
	if bump { // migrations that edit sqlite_master must force a schema reload
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA schema_version = %d`, schemaVersion+1)); err != nil {
			return fmt.Errorf("migrate %04d: bump schema_version: %w", mg.Version, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		mg.Version, mg.Name, mg.Checksum, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("migrate %04d: record: %w", mg.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate %04d: commit: %w", mg.Version, err)
	}
	return nil
}
