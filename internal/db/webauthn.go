package db

import (
	"context"
	"database/sql"
	"time"
)

// PrefixWebAuthn is the id prefix of webauthn_credentials rows.
const PrefixWebAuthn = "wak"

// WebAuthnCredential is a registered passkey / security key of an admin.
type WebAuthnCredential struct {
	ID           string
	AdminID      string
	CredentialID []byte
	PublicKey    []byte
	SignCount    uint32
	Transports   string // comma separated
	AAGUID       []byte
	Flags        uint8
	Name         string
	CreatedAt    time.Time
	LastUsedAt   *time.Time
}

const webauthnCols = `id, admin_id, credential_id, public_key, sign_count, transports, aaguid, flags, name, created_at, last_used_at`

func scanWebAuthn(row interface{ Scan(...any) error }) (*WebAuthnCredential, error) {
	var c WebAuthnCredential
	var sc, flags, created int64
	var last sql.NullInt64
	if err := row.Scan(&c.ID, &c.AdminID, &c.CredentialID, &c.PublicKey, &sc, &c.Transports, &c.AAGUID, &flags, &c.Name, &created, &last); err != nil {
		return nil, mapErr(err)
	}
	c.SignCount, c.Flags = uint32(sc), uint8(flags)
	c.CreatedAt = fromMs(created)
	c.LastUsedAt = fromNullMs(last)
	return &c, nil
}

// CreateWebAuthnCredential inserts a credential. ErrConflict if the
// credential id is already registered.
func (s *Store) CreateWebAuthnCredential(ctx context.Context, c *WebAuthnCredential) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO webauthn_credentials (`+webauthnCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.AdminID, c.CredentialID, c.PublicKey, int64(c.SignCount), c.Transports, c.AAGUID, int64(c.Flags), c.Name,
		ms(c.CreatedAt), nullMs(c.LastUsedAt))
	return mapErr(err)
}

// ListWebAuthnCredentials returns the credentials of an admin, oldest first.
func (s *Store) ListWebAuthnCredentials(ctx context.Context, adminID string) ([]*WebAuthnCredential, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+webauthnCols+` FROM webauthn_credentials WHERE admin_id = ? ORDER BY created_at, id`, adminID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*WebAuthnCredential
	for rows.Next() {
		c, err := scanWebAuthn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, mapErr(rows.Err())
}

// GetWebAuthnCredentialByCredID finds a credential by its authenticator id.
func (s *Store) GetWebAuthnCredentialByCredID(ctx context.Context, credID []byte) (*WebAuthnCredential, error) {
	return scanWebAuthn(s.r.QueryRowContext(ctx, `SELECT `+webauthnCols+` FROM webauthn_credentials WHERE credential_id = ?`, credID))
}

// DeleteWebAuthnCredential removes a credential of the given admin.
func (s *Store) DeleteWebAuthnCredential(ctx context.Context, adminID, id string) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE id = ? AND admin_id = ?`, id, adminID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchWebAuthnCredential records a successful assertion.
func (s *Store) TouchWebAuthnCredential(ctx context.Context, id string, signCount uint32, flags uint8, at time.Time) error {
	_, err := s.w.ExecContext(ctx, `UPDATE webauthn_credentials SET sign_count = ?, flags = ?, last_used_at = ? WHERE id = ?`,
		int64(signCount), int64(flags), ms(at), id)
	return mapErr(err)
}
