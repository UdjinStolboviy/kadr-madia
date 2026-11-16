package db

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "kadr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestMigratorEmbedded(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	m := DefaultMigrator()

	pending, err := m.Pending(ctx, d.R)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) == 0 || pending[0].Version != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	applied, err := m.Up(ctx, d.W)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(pending) {
		t.Fatalf("applied %d, want %d", len(applied), len(pending))
	}
	again, err := m.Up(ctx, d.W)
	if err != nil || len(again) != 0 {
		t.Fatalf("second Up: %v, %d", err, len(again))
	}
	pending, _ = m.Pending(ctx, d.R)
	if len(pending) != 0 {
		t.Fatalf("still pending: %d", len(pending))
	}

	var fk int
	if err := d.R.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v", fk, err)
	}
	var mode string
	if err := d.W.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
}

func TestMigratorFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	m, err := NewMigrator(fstest.MapFS{
		"0001_ok.sql":  {Data: []byte("CREATE TABLE a (x INTEGER);")},
		"0002_bad.sql": {Data: []byte("CREATE TABLE b (x INTEGER); THIS IS NOT SQL;")},
	})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := m.Up(ctx, d.W)
	if err == nil || len(applied) != 1 {
		t.Fatalf("applied=%d err=%v", len(applied), err)
	}
	var n int
	d.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = 'b'`).Scan(&n)
	if n != 0 {
		t.Fatal("table b exists although its migration failed")
	}
	st, _ := m.Status(ctx, d.R)
	if st[0].AppliedAt.IsZero() || !st[1].AppliedAt.IsZero() {
		t.Fatalf("status = %+v", st)
	}
}

func TestMigratorRejectsBadNames(t *testing.T) {
	if _, err := NewMigrator(fstest.MapFS{"init.sql": {Data: []byte("")}}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := NewMigrator(fstest.MapFS{"0001_a.sql": {}, "0001_b.sql": {}}); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	d := openTemp(t)
	if _, err := DefaultMigrator().Up(context.Background(), d.W); err != nil {
		t.Fatal(err)
	}
	return NewStore(d)
}

// projects.storage_profile_id references a table declared later in 0001;
// make sure the forward reference is enforced with foreign_keys=ON.
func TestProjectStorageProfileForeignKey(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	bogus := "stp_missing"
	err := s.CreateProject(ctx, &Project{ID: NewID(PrefixProject), Slug: "acme", Name: "Acme", Environment: "live",
		StorageProfileID: &bogus, CreatedAt: now, UpdatedAt: now})
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("expected FK violation, got %v", err)
	}
	sp := &StorageProfile{ID: NewID(PrefixStorageProfile), Name: "r2", Provider: "r2", Endpoint: "https://x", Region: "auto",
		PublicBucket: "pub", PrivateBucket: "priv", PublicBaseURL: "https://cdn", CredentialsEnc: []byte{1}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateStorageProfile(ctx, sp); err != nil {
		t.Fatal(err)
	}
	p := &Project{ID: NewID(PrefixProject), Slug: "acme", Name: "Acme", Environment: "live", StorageProfileID: &sp.ID, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	dup := *p
	dup.ID = NewID(PrefixProject)
	if err := s.CreateProject(ctx, &dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup slug err = %v", err)
	}
	n, err := s.CountStorageProfileUsage(ctx, sp.ID)
	if err != nil || n != 1 {
		t.Fatalf("usage = %d, %v", n, err)
	}
	got, err := s.GetProject(ctx, p.ID)
	if err != nil || *got.StorageProfileID != sp.ID || len(got.AllowedOrigins) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := s.GetProject(ctx, "prj_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID("prj"), NewID("prj")
	if a == b || !strings.HasPrefix(a, "prj_") || len(a) != 4+26 || strings.ToLower(a) != a {
		t.Fatalf("ids %q %q", a, b)
	}
}
