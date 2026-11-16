package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Admin is an administrator account.
type Admin struct {
	ID            string
	Email         string
	PasswordHash  string
	TOTPSecretEnc []byte // encrypted; may be set while TOTP is still pending
	TOTPEnabledAt *time.Time
	Role          string
	CreatedAt     time.Time
	LastLoginAt   *time.Time
}

// TOTPEnabled reports whether a second factor is required at login.
func (a *Admin) TOTPEnabled() bool { return a.TOTPEnabledAt != nil && len(a.TOTPSecretEnc) > 0 }

const adminCols = `id, email, password_hash, totp_secret_enc, totp_enabled_at, role, created_at, last_login_at`

func scanAdmin(row interface{ Scan(...any) error }) (*Admin, error) {
	var a Admin
	var enabled, last sql.NullInt64
	var created int64
	if err := row.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.TOTPSecretEnc, &enabled, &a.Role, &created, &last); err != nil {
		return nil, mapErr(err)
	}
	a.TOTPEnabledAt = fromNullMs(enabled)
	a.CreatedAt = fromMs(created)
	a.LastLoginAt = fromNullMs(last)
	return &a, nil
}

// CreateAdmin inserts a new admin. Returns ErrConflict if the email exists.
func (s *Store) CreateAdmin(ctx context.Context, a *Admin) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO admins (id, email, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		a.ID, a.Email, a.PasswordHash, a.Role, ms(a.CreatedAt))
	if err != nil {
		return fmt.Errorf("create admin: %w", mapErr(err))
	}
	return nil
}

// GetAdminByEmail looks up an admin case-insensitively.
func (s *Store) GetAdminByEmail(ctx context.Context, email string) (*Admin, error) {
	return scanAdmin(s.r.QueryRowContext(ctx, `SELECT `+adminCols+` FROM admins WHERE email = ?`, email))
}

// GetAdmin looks up an admin by id.
func (s *Store) GetAdmin(ctx context.Context, id string) (*Admin, error) {
	return scanAdmin(s.r.QueryRowContext(ctx, `SELECT `+adminCols+` FROM admins WHERE id = ?`, id))
}

// TouchAdminLogin records a successful login.
func (s *Store) TouchAdminLogin(ctx context.Context, id string, at time.Time) error {
	_, err := s.w.ExecContext(ctx, `UPDATE admins SET last_login_at = ? WHERE id = ?`, ms(at), id)
	return mapErr(err)
}

// SetAdminTOTP stores the (encrypted) TOTP secret and enabled timestamp.
// A nil secret clears TOTP entirely; a nil enabledAt keeps it pending.
func (s *Store) SetAdminTOTP(ctx context.Context, id string, secretEnc []byte, enabledAt *time.Time) error {
	res, err := s.w.ExecContext(ctx, `UPDATE admins SET totp_secret_enc = ?, totp_enabled_at = ? WHERE id = ?`,
		secretEnc, nullMs(enabledAt), id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
