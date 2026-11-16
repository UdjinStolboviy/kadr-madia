package main

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

func cmdMigrate(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 1 || (args[0] != "up" && args[0] != "status") {
		return fmt.Errorf("%w: kadr migrate up|status", errUsage)
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
	m := db.DefaultMigrator()

	if args[0] == "status" {
		st, err := m.Status(ctx, database.R)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "VERSION\tNAME\tAPPLIED AT\tNOTE")
		for _, s := range st {
			applied, note := "pending", ""
			if !s.AppliedAt.IsZero() {
				applied = s.AppliedAt.Format("2006-01-02 15:04:05Z")
			}
			if s.ChecksumChanged {
				note = "file changed after apply"
			}
			fmt.Fprintf(tw, "%04d\t%s\t%s\t%s\n", s.Version, s.Name, applied, note)
		}
		return tw.Flush()
	}

	applied, err := m.Up(ctx, database.W)
	for _, a := range applied {
		fmt.Fprintf(stdout, "applied %04d_%s\n", a.Version, a.Name)
	}
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Fprintf(stdout, "database %s is up to date\n", cfg.DBPath)
	}
	return nil
}
