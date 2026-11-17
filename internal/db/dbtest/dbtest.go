// Package dbtest provides a migrated temporary database for tests.
package dbtest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// Open returns a Store over a fresh, fully migrated database in t.TempDir().
func Open(t testing.TB) *db.Store {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "kadr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := db.DefaultMigrator().Up(ctx, d.W); err != nil {
		t.Fatal(err)
	}
	return db.NewStore(d)
}
