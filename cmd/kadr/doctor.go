package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/UdjinStolboviy/kadr-madia/internal/capabilities"
	"github.com/UdjinStolboviy/kadr-madia/internal/config"
	"github.com/UdjinStolboviy/kadr-madia/internal/crypto"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// lowDiskBytes triggers a warning for the tmp directory (OPERATIONS.md §9).
const lowDiskBytes = 5 << 30

type checkRow struct {
	name     string
	required bool
	ok       bool
	warn     bool
	version  string
	detail   string
}

// cmdDoctor prints a table of checks and reports whether all required ones pass.
func cmdDoctor(ctx context.Context, args []string, stdout io.Writer) (bool, error) {
	if len(args) > 0 {
		return false, fmt.Errorf("%w: doctor takes no arguments", errUsage)
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return false, err
	}
	var rows []checkRow

	if err := cfg.Validate(); err != nil {
		rows = append(rows, checkRow{name: "config", required: true, detail: err.Error()})
	} else {
		mode := "production"
		if cfg.Dev {
			mode = "dev (KADR_DEV=1)"
		}
		rows = append(rows, checkRow{name: "config", required: true, ok: true, detail: mode})
	}

	rows = append(rows, tmpCheck(cfg.TmpDir))
	caps := capabilities.Probe(ctx, cfg.TmpDir)
	capRow := func(name string, c capabilities.Cap, required bool) checkRow {
		return checkRow{name: name, required: required, ok: c.OK, warn: !c.OK && !required, version: c.Version, detail: c.Detail}
	}
	rows = append(rows,
		capRow("vips", caps.Vips, true),
		capRow("vips_avif", caps.VipsAVIF, true),
		capRow("ffmpeg", caps.FFmpeg, true),
		capRow("ffprobe", caps.FFprobe, true),
		capRow("rclone", caps.Rclone, false),
		capRow("vaapi", caps.VAAPI, false),
	)
	rows = append(rows, dbCheck(ctx, cfg.DBPath), masterKeyCheck(cfg))

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CHECK\tSTATUS\tREQUIRED\tVERSION\tDETAIL")
	allOK := true
	for _, r := range rows {
		status := "OK"
		switch {
		case !r.ok && r.required:
			status, allOK = "FAIL", false
		case !r.ok || r.warn:
			status = "WARN"
		}
		req := "no"
		if r.required {
			req = "yes"
		}
		ver := r.version
		if ver == "" {
			ver = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.name, status, req, ver, r.detail)
	}
	if err := tw.Flush(); err != nil {
		return false, err
	}
	if allOK {
		fmt.Fprintln(stdout, "\nAll required checks passed.")
	} else {
		fmt.Fprintln(stdout, "\nSome required checks failed.")
	}
	return allOK, nil
}

func tmpCheck(dir string) checkRow {
	r := checkRow{name: "tmp_dir", required: true}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		r.detail = fmt.Sprintf("%s: %v", dir, err)
		return r
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		r.detail = fmt.Sprintf("%s is not writable: %v", dir, err)
		return r
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	r.ok = true
	free, err := capabilities.DiskFree(dir)
	if err != nil {
		r.detail = fmt.Sprintf("%s writable; free space unknown: %v", dir, err)
		r.warn = true
		return r
	}
	r.detail = fmt.Sprintf("%s writable, %.1f GiB free", dir, float64(free)/(1<<30))
	if free < lowDiskBytes {
		r.warn = true
		r.detail += " (below 5 GiB)"
	}
	return r
}

func dbCheck(ctx context.Context, path string) checkRow {
	r := checkRow{name: "database", required: true}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		r.detail = fmt.Sprintf("%s does not exist; run `kadr migrate up`", path)
		return r
	}
	d, err := db.Open(ctx, path)
	if err != nil {
		r.detail = err.Error()
		return r
	}
	defer d.Close()
	pending, err := db.DefaultMigrator().Pending(ctx, d.R)
	if err != nil {
		r.detail = err.Error()
		return r
	}
	if len(pending) > 0 {
		r.detail = fmt.Sprintf("%s: %d pending migration(s); run `kadr migrate up`", path, len(pending))
		return r
	}
	size, wal := d.SizeInfo()
	r.ok = true
	r.detail = fmt.Sprintf("%s migrated (%d KiB, WAL %d KiB)", path, size>>10, wal>>10)
	return r
}

func masterKeyCheck(cfg *config.Config) checkRow {
	r := checkRow{name: "master_key", required: true}
	if _, err := os.Stat(cfg.MasterKeyFile); errors.Is(err, os.ErrNotExist) && cfg.Dev {
		r.ok, r.warn = true, true
		r.detail = cfg.MasterKeyFile + " missing; it will be generated on first start (dev mode)"
		return r
	}
	if _, err := crypto.LoadKeyFile(cfg.MasterKeyFile); err != nil {
		r.detail = err.Error()
		return r
	}
	r.ok = true
	r.detail = cfg.MasterKeyFile + " readable, 32 bytes"
	if st, err := os.Stat(cfg.MasterKeyFile); err == nil && st.Mode().Perm()&0o007 != 0 {
		r.warn = true
		r.detail += fmt.Sprintf(" (mode %v is world-accessible)", st.Mode().Perm())
	}
	return r
}
