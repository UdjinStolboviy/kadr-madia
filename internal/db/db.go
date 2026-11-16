// Package db owns the SQLite database: connection setup, embedded migrations
// and the Store with hand-written queries (one file per aggregate, so the
// queries can later be moved to sqlc without touching callers).
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Connection PRAGMAs, mirroring the header of migrations/0001_init.sql.
var commonPragmas = []string{
	"busy_timeout(5000)",
	"foreign_keys(ON)",
	"synchronous(NORMAL)",
	"temp_store(MEMORY)",
	"cache_size(-16000)",
	"mmap_size(134217728)",
}

// DB bundles the single writer connection and the reader pool.
//
// SQLite allows exactly one writer at a time; funnelling all writes through
// one connection (MaxOpenConns=1, BEGIN IMMEDIATE) avoids SQLITE_BUSY on lock
// upgrades. Readers use a separate pool with query_only=ON.
type DB struct {
	Path string
	W    *sql.DB
	R    *sql.DB
}

// Open opens (creating if needed) the database at path.
func Open(ctx context.Context, path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("db: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("db: create dir: %w", err)
	}

	w, err := sql.Open("sqlite", dsn(path, append([]string{"journal_mode(WAL)"}, commonPragmas...), true))
	if err != nil {
		return nil, fmt.Errorf("db: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("db: ping writer: %w", err)
	}
	// The database holds password hashes and encrypted secrets: keep it
	// private to the service user (best effort; -wal/-shm inherit the mode).
	if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
		_ = os.Chmod(path, 0o600)
	}

	r, err := sql.Open("sqlite", dsn(path, append(commonPragmas, "query_only(ON)"), false))
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("db: open readers: %w", err)
	}
	n := max(4, runtime.NumCPU())
	r.SetMaxOpenConns(n)
	r.SetMaxIdleConns(n)
	if err := r.PingContext(ctx); err != nil {
		w.Close()
		r.Close()
		return nil, fmt.Errorf("db: ping readers: %w", err)
	}
	return &DB{Path: path, W: w, R: r}, nil
}

// Ping checks both pools.
func (d *DB) Ping(ctx context.Context) error {
	if err := d.W.PingContext(ctx); err != nil {
		return fmt.Errorf("db: writer: %w", err)
	}
	var one int
	if err := d.R.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("db: reader: %w", err)
	}
	return nil
}

// Close closes both pools.
func (d *DB) Close() error {
	return errors.Join(d.R.Close(), d.W.Close())
}

// SizeInfo returns the size of the main database file and the WAL file.
func (d *DB) SizeInfo() (dbBytes, walBytes int64) {
	if st, err := os.Stat(d.Path); err == nil {
		dbBytes = st.Size()
	}
	if st, err := os.Stat(d.Path + "-wal"); err == nil {
		walBytes = st.Size()
	}
	return dbBytes, walBytes
}

func dsn(path string, pragmas []string, writer bool) string {
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	if writer {
		q.Set("_txlock", "immediate")
	}
	u := url.URL{Scheme: "file", Opaque: filepath.ToSlash(path), RawQuery: q.Encode()}
	return u.String()
}
