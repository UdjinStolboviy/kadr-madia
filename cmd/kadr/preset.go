package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

const (
	defaultTextEnableSlugs = "file-compact,file-balanced,file-quality,file-original"
	minPresetTextBytes     = 1024
	maxPresetTextBytes     = 10 << 20
	defaultPresetTextBytes = 2 << 20
)

func cmdPreset(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "enable-text" {
		return fmt.Errorf("%w: kadr preset enable-text [--project SLUG] [--slugs LIST] [--max-bytes N]", errUsage)
	}
	return cmdPresetEnableText(ctx, args[1:], stdout, stderr)
}

func cmdPresetEnableText(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("preset enable-text", flag.ContinueOnError)
	fs.SetOutput(stderr)
	projectSlug := fs.String("project", "", "project slug (default: all projects)")
	slugsRaw := fs.String("slugs", defaultTextEnableSlugs, "comma-separated file preset slugs")
	maxBytes := fs.Int("max-bytes", defaultPresetTextBytes, "max extracted text size in bytes (1024–10485760)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}
	if *maxBytes < minPresetTextBytes || *maxBytes > maxPresetTextBytes {
		return fmt.Errorf("%w: --max-bytes must be between %d and %d", errUsage, minPresetTextBytes, maxPresetTextBytes)
	}
	slugs := parseSlugList(*slugsRaw)
	if len(slugs) == 0 {
		return fmt.Errorf("%w: --slugs must contain at least one preset slug", errUsage)
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer database.Close()
	if pending, err := db.DefaultMigrator().Pending(ctx, database.R); err != nil {
		return err
	} else if len(pending) > 0 {
		return errors.New("database has pending migrations; run `kadr migrate up` first")
	}

	store := db.NewStore(database)
	projects, err := filterProjects(ctx, store, strings.TrimSpace(*projectSlug))
	if err != nil {
		return err
	}
	fileDefaults := defaultFilePresetMap()
	for _, p := range projects {
		for _, slug := range slugs {
			if err := enableTextForPreset(ctx, store, p, slug, *maxBytes, fileDefaults, stdout); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseSlugList(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func filterProjects(ctx context.Context, store *db.Store, slug string) ([]*db.Project, error) {
	projects, err := store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	if slug == "" {
		return projects, nil
	}
	for _, p := range projects {
		if p.Slug == slug {
			return []*db.Project{p}, nil
		}
	}
	return nil, fmt.Errorf("project %q not found", slug)
}

func defaultFilePresetMap() map[string]preset.DefaultPreset {
	out := map[string]preset.DefaultPreset{}
	for _, d := range preset.DefaultPresets() {
		if d.Kind == "file" {
			out[d.Slug] = d
		}
	}
	return out
}

func enableTextForPreset(ctx context.Context, store *db.Store, p *db.Project, slug string, maxBytes int, defaults map[string]preset.DefaultPreset, stdout io.Writer) error {
	now := time.Now()
	pr, err := store.GetPresetBySlug(ctx, p.ID, slug)
	if errors.Is(err, db.ErrNotFound) {
		return createMissingFilePreset(ctx, store, p, slug, maxBytes, defaults, now, stdout)
	}
	if err != nil {
		return err
	}
	if pr.Kind != "file" {
		return fmt.Errorf("preset %s/%s has kind %q, want file", p.Slug, slug, pr.Kind)
	}
	var fp preset.FileParams
	if err := json.Unmarshal(pr.Params, &fp); err != nil {
		return fmt.Errorf("preset %s/%s params: %w", p.Slug, slug, err)
	}
	if fp.Text.Enabled {
		fmt.Fprintf(stdout, "%s/%s: already enabled (v%d)\n", p.Slug, slug, pr.CurrentVersion)
		return nil
	}
	fp.Kind = "file"
	fp.Text.Enabled = true
	if fp.Text.MaxBytes <= 0 {
		fp.Text.MaxBytes = maxBytes
	}
	raw, err := json.Marshal(fp)
	if err != nil {
		return err
	}
	var version int
	err = store.InTx(ctx, func(tx *db.Store) error {
		var err error
		version, err = tx.AddPresetVersion(ctx, pr.ID, raw, "cli", now)
		if err != nil {
			return err
		}
		return tx.InsertAudit(ctx, &db.AuditEntry{
			ActorType: db.ActorSystem,
			ProjectID: &p.ID,
			Action:    "preset.version",
			Target:    &pr.ID,
		}, map[string]any{"slug": pr.Slug, "version": version, "via": "cli", "text_enabled": true})
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s/%s: v%d -> v%d (text.enabled=true)\n", p.Slug, slug, pr.CurrentVersion, version)
	return nil
}

func createMissingFilePreset(ctx context.Context, store *db.Store, p *db.Project, slug string, maxBytes int, defaults map[string]preset.DefaultPreset, now time.Time, stdout io.Writer) error {
	def, ok := defaults[slug]
	if !ok {
		fmt.Fprintf(stdout, "%s/%s: not found (skipped)\n", p.Slug, slug)
		return nil
	}
	var fp preset.FileParams
	if err := json.Unmarshal(def.Params, &fp); err != nil {
		return fmt.Errorf("preset %s/%s default params: %w", p.Slug, slug, err)
	}
	fp.Kind = "file"
	fp.Text = preset.FileText{Enabled: true, MaxBytes: maxBytes}
	raw, err := json.Marshal(fp)
	if err != nil {
		return err
	}
	np := &db.Preset{
		ID:             db.NewID(db.PrefixPreset),
		ProjectID:      p.ID,
		Slug:           slug,
		Kind:           "file",
		Description:    def.Description,
		CurrentVersion: 1,
		Params:         raw,
		CreatedAt:      now,
	}
	err = store.InTx(ctx, func(tx *db.Store) error {
		if err := tx.CreatePreset(ctx, np, "cli"); err != nil {
			return err
		}
		return tx.InsertAudit(ctx, &db.AuditEntry{
			ActorType: db.ActorSystem,
			ProjectID: &p.ID,
			Action:    "preset.create",
			Target:    &np.ID,
		}, map[string]any{"slug": slug, "via": "cli", "text_enabled": true})
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s/%s: created v1 (text.enabled=true)\n", p.Slug, slug)
	return nil
}
