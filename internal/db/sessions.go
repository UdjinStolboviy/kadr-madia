package db

import (
	"context"
	"database/sql"
	"time"
)

// Session is a server-side admin session. The token itself is never stored,
// only its SHA-256.
type Session struct {
	IDHash    []byte
	AdminID   string
	CSRFToken string
	IP        string
	UserAgent string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateSession inserts a session.
func (s *Store) CreateSession(ctx context.Context, ss *Session) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO admin_sessions (id_hash, admin_id, csrf_token, ip, user_agent, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ss.IDHash, ss.AdminID, ss.CSRFToken, ss.IP, ss.UserAgent, ms(ss.CreatedAt), ms(ss.ExpiresAt))
	return mapErr(err)
}

// GetSession returns a session that has not expired at now.
func (s *Store) GetSession(ctx context.Context, idHash []byte, now time.Time) (*Session, error) {
	var ss Session
	var ip, ua sql.NullString
	var created, expires int64
	err := s.r.QueryRowContext(ctx,
		`SELECT id_hash, admin_id, csrf_token, ip, user_agent, created_at, expires_at
		 FROM admin_sessions WHERE id_hash = ? AND expires_at > ?`, idHash, ms(now)).
		Scan(&ss.IDHash, &ss.AdminID, &ss.CSRFToken, &ip, &ua, &created, &expires)
	if err != nil {
		return nil, mapErr(err)
	}
	ss.IP, ss.UserAgent = ip.String, ua.String
	ss.CreatedAt, ss.ExpiresAt = fromMs(created), fromMs(expires)
	return &ss, nil
}

// ExtendSession moves the expiry of a session (sliding window).
func (s *Store) ExtendSession(ctx context.Context, idHash []byte, expiresAt time.Time) error {
	_, err := s.w.ExecContext(ctx, `UPDATE admin_sessions SET expires_at = ? WHERE id_hash = ?`, ms(expiresAt), idHash)
	return mapErr(err)
}

// DeleteSession removes one session (logout).
func (s *Store) DeleteSession(ctx context.Context, idHash []byte) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM admin_sessions WHERE id_hash = ?`, idHash)
	return mapErr(err)
}

// DeleteAdminSessions removes all sessions of an admin except keepHash.
func (s *Store) DeleteAdminSessions(ctx context.Context, adminID string, keepHash []byte) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM admin_sessions WHERE admin_id = ? AND id_hash <> ?`, adminID, keepHash)
	return mapErr(err)
}

// DeleteExpiredSessions purges expired sessions and returns how many.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at <= ?`, ms(now))
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}
