package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

func TestPresetEnableTextCLI(t *testing.T) {
	t.Setenv("KADR_DEV", "1")
	t.Chdir(t.TempDir())

	var out, errb bytes.Buffer
	if code := run([]string{"migrate", "up"}, nil, &out, &errb); code != 0 {
		t.Fatalf("migrate: %s", errb.String())
	}

	ctx := context.Background()
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	store := db.NewStore(database)
	now := time.Now()
	proj := &db.Project{
		ID:          db.NewID(db.PrefixProject),
		Slug:        "acme",
		Name:        "Acme",
		Environment: "live",
		DefaultMode: "balanced",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateProject(ctx, proj); err != nil {
		t.Fatal(err)
	}

	seedFilePreset := func(slug string, mut func(*preset.FileParams)) {
		t.Helper()
		fp := preset.FileParams{
			Kind:      "file",
			Limits:    preset.FileLimits{MaxInputBytes: 100 << 20, Accept: preset.DefaultFileFormats},
			Optimize:  preset.FileOptimize{Strategy: preset.FileStrategyDownsample, ImageDPI: 150, JPEGQuality: 75, Linearize: true, MinSavingsPercent: 5},
			Thumbnail: preset.FileThumb{Enabled: true, MaxSide: 640, MaxBytes: 20 << 10, Formats: []string{"avif"}},
			Text:      preset.FileText{Enabled: false},
		}
		if mut != nil {
			mut(&fp)
		}
		raw, err := json.Marshal(fp)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CreatePreset(ctx, &db.Preset{
			ID:             db.NewID(db.PrefixPreset),
			ProjectID:      proj.ID,
			Slug:           slug,
			Kind:           "file",
			Description:    slug,
			CurrentVersion: 1,
			Params:         raw,
			CreatedAt:      now,
		}, "seed"); err != nil {
			t.Fatal(err)
		}
	}

	seedFilePreset("file-compact", nil)
	seedFilePreset("file-balanced", func(fp *preset.FileParams) {
		fp.Optimize.ImageDPI = 120
	})
	seedFilePreset("file-quality", func(fp *preset.FileParams) {
		fp.Optimize.Strategy = preset.FileStrategyLossless
		fp.Optimize.ImageDPI = 0
		fp.Optimize.JPEGQuality = 0
	})
	// Intentionally omit file-original to verify creation from defaults.
	database.Close()

	out.Reset()
	errb.Reset()
	if code := run([]string{"preset", "enable-text"}, nil, &out, &errb); code != 0 {
		t.Fatalf("preset enable-text exit=%d stderr=%s stdout=%s", code, errb.String(), out.String())
	}

	database, err = db.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store = db.NewStore(database)

	checkPreset := func(slug string, wantVer int, wantDPI int) {
		t.Helper()
		pr, err := store.GetPresetBySlug(ctx, proj.ID, slug)
		if err != nil {
			t.Fatalf("get %s: %v", slug, err)
		}
		if pr.CurrentVersion != wantVer {
			t.Fatalf("%s version = %d, want %d", slug, pr.CurrentVersion, wantVer)
		}
		var fp preset.FileParams
		if err := json.Unmarshal(pr.Params, &fp); err != nil {
			t.Fatalf("unmarshal %s: %v", slug, err)
		}
		if !fp.Text.Enabled || fp.Text.MaxBytes != 2<<20 {
			t.Fatalf("%s text = %+v, want enabled=true maxBytes=2MiB", slug, fp.Text)
		}
		if wantDPI > 0 && fp.Optimize.ImageDPI != wantDPI {
			t.Fatalf("%s imageDpi = %d, want %d", slug, fp.Optimize.ImageDPI, wantDPI)
		}
	}

	checkPreset("file-compact", 2, 150)
	checkPreset("file-balanced", 2, 120)
	checkPreset("file-quality", 2, 0)
	checkPreset("file-original", 1, 0)

	// Second run must be idempotent.
	out.Reset()
	errb.Reset()
	if code := run([]string{"preset", "enable-text"}, nil, &out, &errb); code != 0 {
		t.Fatalf("second run exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "already enabled") {
		t.Fatalf("expected 'already enabled' in output:\n%s", out.String())
	}
	checkPreset("file-compact", 2, 150)
	checkPreset("file-balanced", 2, 120)
	checkPreset("file-quality", 2, 0)
	checkPreset("file-original", 1, 0)

	// Unknown project filter must fail.
	out.Reset()
	errb.Reset()
	if code := run([]string{"preset", "enable-text", "--project", "missing"}, nil, &out, &errb); code == 0 {
		t.Fatalf("expected non-zero exit for unknown project, got 0")
	}
}
