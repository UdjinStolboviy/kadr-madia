package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/UdjinStolboviy/kadr-madia/internal/config"
)

// cmdRestoreDrill restores the newest Litestream replica into a scratch
// directory and verifies it: PRAGMA integrity_check, applied migrations and
// row counts of the core tables, compared with the live database when
// present. The restored copy is deleted unless --keep is given.
func cmdRestoreDrill(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("restore-drill", flag.ContinueOnError)
	cfgPath := fs.String("config", "/etc/kadr/litestream.yml", "Litestream config file")
	replica := fs.String("replica", "", "replica URL (s3://bucket/path) instead of --config")
	keep := fs.Bool("keep", false, "keep the restored database and print its path")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	bin, err := exec.LookPath("litestream")
	if err != nil {
		return fmt.Errorf("litestream is not installed: %w", err)
	}
	dir, err := os.MkdirTemp("", "kadr-restore-drill-")
	if err != nil {
		return err
	}
	if !*keep {
		defer cleanupDrillDir(dir)
	}
	out := filepath.Join(dir, "restored.db")

	var cmdArgs []string
	if *replica != "" {
		cmdArgs = []string{"restore", "-o", out, *replica}
	} else {
		cmdArgs = []string{"restore", "-config", *cfgPath, "-o", out, cfg.DBPath}
	}
	start := time.Now()
	cmd := exec.CommandContext(ctx, bin, cmdArgs...)
	cmd.Stdout, cmd.Stderr = stdout, stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("litestream restore failed: %w", err)
	}
	fmt.Fprintf(stdout, "restored in %s\n", time.Since(start).Round(time.Millisecond))

	restored, err := inspectDB(ctx, out)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "integrity_check: %s\nmigrations applied: %d (latest %s)\n", restored.integrity, restored.migrations, restored.latest)
	if restored.integrity != "ok" {
		return fmt.Errorf("restored database failed integrity_check: %s", restored.integrity)
	}
	if restored.migrations == 0 {
		return fmt.Errorf("restored database has no applied migrations")
	}
	live, lerr := inspectDB(ctx, cfg.DBPath)
	fmt.Fprintf(stdout, "\n%-22s %10s %10s\n", "table", "restored", "live")
	for _, t := range coreTables {
		l := "-"
		if lerr == nil {
			l = fmt.Sprint(live.counts[t])
		}
		fmt.Fprintf(stdout, "%-22s %10d %10s\n", t, restored.counts[t], l)
	}
	if *keep {
		fmt.Fprintf(stdout, "\nrestored database kept at %s\n", out)
	}
	fmt.Fprintln(stdout, "\nrestore drill passed.")
	return nil
}

// cleanupDrillDir deletes the scratch directory created by MkdirTemp above.
func cleanupDrillDir(dir string) {
	if filepath.Base(dir) == "" || filepath.Dir(dir) == dir {
		return
	}
	_ = os.RemoveAll(dir)
}

var coreTables = []string{"projects", "api_keys", "presets", "assets", "blobs", "renditions", "uploads", "jobs", "events", "audit_log"}

type dbReport struct {
	integrity  string
	migrations int
	latest     string
	counts     map[string]int64
}

func inspectDB(ctx context.Context, path string) (*dbReport, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	d, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	r := &dbReport{counts: map[string]int64{}}
	if err := d.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&r.integrity); err != nil {
		return nil, fmt.Errorf("integrity_check: %w", err)
	}
	var latest sql.NullString
	if err := d.QueryRowContext(ctx, `SELECT count(*), max(version) FROM schema_migrations`).Scan(&r.migrations, &latest); err == nil {
		r.latest = latest.String
	}
	for _, t := range coreTables {
		var n int64
		if err := d.QueryRowContext(ctx, `SELECT count(*) FROM `+t).Scan(&n); err == nil { // table names are constants
			r.counts[t] = n
		}
	}
	return r, nil
}
