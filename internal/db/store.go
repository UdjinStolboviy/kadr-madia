package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = errors.New("db: not found")
	// ErrConflict is returned on UNIQUE / PRIMARY KEY violations.
	ErrConflict = errors.New("db: conflict")
)

// ID prefixes, see DATA_MODEL.md.
const (
	PrefixAdmin          = "adm"
	PrefixProject        = "prj"
	PrefixAPIKey         = "key"
	PrefixStorageProfile = "stp"
	PrefixPreset         = "pst"
)

// NewID returns "<prefix>_<ulid>" in lowercase. The random part comes from
// crypto/rand (80 bits), so ids are not guessable.
func NewID(prefix string) string {
	id := ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader)
	return prefix + "_" + strings.ToLower(id.String())
}

// dbtx is satisfied by *sql.DB and *sql.Tx.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store is the data access layer. Reads go to the reader pool, writes to the
// single writer connection. Inside InTx both point at the transaction.
type Store struct {
	db *DB
	w  dbtx
	r  dbtx
}

// NewStore wraps an open DB.
func NewStore(d *DB) *Store {
	return &Store{db: d, w: d.W, r: d.R}
}

// DB returns the underlying database handle.
func (s *Store) DB() *DB { return s.db }

// InTx runs fn inside a write transaction (BEGIN IMMEDIATE). The Store passed
// to fn routes reads and writes through the transaction. Nested calls reuse
// the outer transaction.
func (s *Store) InTx(ctx context.Context, fn func(tx *Store) error) error {
	if _, nested := s.w.(*sql.Tx); nested {
		return fn(s)
	}
	tx, err := s.db.W.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	if err := fn(&Store{db: s.db, w: tx, r: tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	return nil
}

// mapErr converts driver errors to package sentinels.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return fmt.Errorf("%w: %v", ErrConflict, err)
		}
	}
	return err
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func nullMs(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func fromMs(v int64) time.Time { return time.UnixMilli(v).UTC() }

func fromNullMs(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

func nullStr(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

func fromNullStr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func nullInt(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

func fromNullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
