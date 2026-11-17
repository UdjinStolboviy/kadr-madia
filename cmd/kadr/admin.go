package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

func cmdAdmin(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "bootstrap" {
		return cmdAdminBootstrap(ctx, args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("%w: kadr admin create --email EMAIL [--role owner|admin|viewer] [--password-stdin] | kadr admin bootstrap [--email EMAIL]", errUsage)
	}
	fs := flag.NewFlagSet("admin create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	email := fs.String("email", "", "administrator email (required)")
	role := fs.String("role", "owner", "role: owner, admin or viewer")
	pwStdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(*email))
	if err != nil || addr.Address != strings.TrimSpace(*email) {
		return fmt.Errorf("%w: --email must be a valid address", errUsage)
	}
	switch *role {
	case "owner", "admin", "viewer":
	default:
		return fmt.Errorf("%w: --role must be owner, admin or viewer", errUsage)
	}

	password, err := readPassword(stdin, stderr, *pwStdin)
	if err != nil {
		return err
	}
	if len([]rune(password)) < auth.MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", auth.MinPasswordLen)
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

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	store := db.NewStore(database)
	a := &db.Admin{ID: db.NewID(db.PrefixAdmin), Email: strings.ToLower(addr.Address), PasswordHash: hash,
		Role: *role, CreatedAt: time.Now()}
	err = store.InTx(ctx, func(tx *db.Store) error {
		if err := tx.CreateAdmin(ctx, a); err != nil {
			return err
		}
		return tx.InsertAudit(ctx, &db.AuditEntry{ActorType: db.ActorSystem, Action: "admin.create", Target: &a.ID},
			map[string]string{"email": a.Email, "role": a.Role, "via": "cli"})
	})
	if errors.Is(err, db.ErrConflict) {
		return fmt.Errorf("an administrator with email %s already exists", a.Email)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "created administrator %s (%s, role %s)\n", a.Email, a.ID, a.Role)
	return nil
}

// readPassword reads from stdin (--password-stdin) or prompts twice on a TTY
// without echo.
func readPassword(stdin io.Reader, stderr io.Writer, fromStdin bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read password: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	f, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", fmt.Errorf("%w: stdin is not a terminal; use --password-stdin", errUsage)
	}
	fmt.Fprint(stderr, "Password: ")
	p1, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	fmt.Fprint(stderr, "Repeat password: ")
	p2, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if string(p1) != string(p2) {
		return "", errors.New("passwords do not match")
	}
	return string(p1), nil
}

// cmdAdminBootstrap creates the first administrator with a generated password
// and prints the credentials once. It does nothing when an administrator
// already exists, so it is safe to run on every deploy.
func cmdAdminBootstrap(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin bootstrap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	email := fs.String("email", os.Getenv("KADR_BOOTSTRAP_ADMIN_EMAIL"), "email of the first administrator (default admin@kadr.local)")
	url := fs.String("url", os.Getenv("KADR_ADMIN_URL"), "admin console URL to print")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if strings.TrimSpace(*email) == "" {
		*email = "admin@kadr.local"
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(*email))
	if err != nil || addr.Address != strings.TrimSpace(*email) {
		return fmt.Errorf("%w: --email must be a valid address", errUsage)
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
	if n, err := store.CountAdmins(ctx); err != nil {
		return err
	} else if n > 0 {
		fmt.Fprintln(stdout, "administrator already exists; no credentials generated")
		return nil
	}
	password, err := generatePassword(24)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	a := &db.Admin{ID: db.NewID(db.PrefixAdmin), Email: strings.ToLower(addr.Address), PasswordHash: hash, Role: "owner", CreatedAt: time.Now()}
	err = store.InTx(ctx, func(tx *db.Store) error {
		if err := tx.CreateAdmin(ctx, a); err != nil {
			return err
		}
		return tx.InsertAudit(ctx, &db.AuditEntry{ActorType: db.ActorSystem, Action: "admin.create", Target: &a.ID},
			map[string]string{"email": a.Email, "role": a.Role, "via": "bootstrap"})
	})
	if err != nil {
		return err
	}
	target := *url
	if target == "" {
		target = "http://" + cfg.AdminAddr + " (the admin listener; expose it e.g. via cloudflared)"
	}
	line := strings.Repeat("=", 64)
	fmt.Fprintf(stdout, "\n%s\n  KADR ADMIN CONSOLE — FIRST LOGIN (shown once, save it now)\n%s\n", line, line)
	fmt.Fprintf(stdout, "  URL:       %s\n  Email:     %s\n  Password:  %s\n", target, a.Email, password)
	fmt.Fprintf(stdout, "%s\n  Change the password and enable 2FA / a passkey after signing in.\n%s\n\n", line, line)
	return nil
}

// generatePassword returns n characters of crypto-random letters and digits
// without look-alike characters.
func generatePassword(n int) (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}
