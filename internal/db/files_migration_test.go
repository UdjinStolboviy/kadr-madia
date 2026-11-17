package db_test

import (
	"context"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/db/dbtest"
)

// Migration 0005 relaxes the kind CHECK on presets, blobs and assets in place.
func TestFileKindAllowed(t *testing.T) {
	ctx := context.Background()
	s := dbtest.Open(t)
	w := s.DB().W
	if _, err := w.ExecContext(ctx, `INSERT INTO projects (id, slug, name, created_at, updated_at) VALUES ('prj_1','p','P',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ExecContext(ctx, `INSERT INTO presets (id, project_id, slug, kind, current_version, created_at) VALUES ('pst_1','prj_1','doc','file',1,1)`); err != nil {
		t.Fatalf("preset kind file rejected: %v", err)
	}
	if _, err := w.ExecContext(ctx, `INSERT INTO presets (id, project_id, slug, kind, current_version, created_at) VALUES ('pst_2','prj_1','bad','audio',1,1)`); err == nil {
		t.Fatal("unknown kind accepted: the CHECK was removed instead of widened")
	}
	if _, err := w.ExecContext(ctx, `INSERT INTO assets (id, project_id, ns, kind, visibility, status, created_at) VALUES ('ast_1','prj_1','n','file','private','processing',1)`); err != nil {
		t.Fatalf("asset kind file rejected: %v", err)
	}
	var ok string
	if err := w.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check: %q %v", ok, err)
	}
	var fk int
	if err := w.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Fatalf("foreign_key_check: %d %v", fk, err)
	}
}
