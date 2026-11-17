package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// GetSetting reads a JSON setting into v. It returns ErrNotFound when unset.
func (s *Store) GetSetting(ctx context.Context, key string, v any) error {
	var raw string
	err := s.r.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return mapErr(err)
	}
	return json.Unmarshal([]byte(raw), v)
}

// SetSetting stores v as the JSON value of key.
func (s *Store) SetSetting(ctx context.Context, key string, v any, now time.Time) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.w.ExecContext(ctx,
		`INSERT INTO settings (key, value_json, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, key, string(b), ms(now))
	return mapErr(err)
}
