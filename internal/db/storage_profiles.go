package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// StorageProfile describes an S3-compatible storage account (platform R2 or
// BYOS). Credentials are an encrypted JSON blob.
type StorageProfile struct {
	ID             string
	Name           string
	Provider       string
	Endpoint       string
	Region         string
	PublicBucket   string
	PrivateBucket  string
	PublicBaseURL  string
	PrivateBaseURL *string
	CredentialsEnc []byte
	IsPlatform     bool
	LastTestAt     *time.Time
	LastTestOK     *bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const storageProfileCols = `id, name, provider, endpoint, region, public_bucket, private_bucket, public_base_url,
	private_base_url, credentials_enc, is_platform, last_test_at, last_test_ok, created_at, updated_at`

func scanStorageProfile(row interface{ Scan(...any) error }) (*StorageProfile, error) {
	var p StorageProfile
	var priv sql.NullString
	var lastAt, lastOK sql.NullInt64
	var created, updated int64
	if err := row.Scan(&p.ID, &p.Name, &p.Provider, &p.Endpoint, &p.Region, &p.PublicBucket, &p.PrivateBucket,
		&p.PublicBaseURL, &priv, &p.CredentialsEnc, &p.IsPlatform, &lastAt, &lastOK, &created, &updated); err != nil {
		return nil, mapErr(err)
	}
	p.PrivateBaseURL = fromNullStr(priv)
	p.LastTestAt = fromNullMs(lastAt)
	if lastOK.Valid {
		ok := lastOK.Int64 == 1
		p.LastTestOK = &ok
	}
	p.CreatedAt, p.UpdatedAt = fromMs(created), fromMs(updated)
	return &p, nil
}

// CreateStorageProfile inserts a profile.
func (s *Store) CreateStorageProfile(ctx context.Context, p *StorageProfile) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO storage_profiles (id, name, provider, endpoint, region, public_bucket, private_bucket,
		  public_base_url, private_base_url, credentials_enc, is_platform, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Provider, p.Endpoint, p.Region, p.PublicBucket, p.PrivateBucket, p.PublicBaseURL,
		nullStr(p.PrivateBaseURL), p.CredentialsEnc, p.IsPlatform, ms(p.CreatedAt), ms(p.UpdatedAt))
	if err != nil {
		return fmt.Errorf("create storage profile: %w", mapErr(err))
	}
	return nil
}

// UpdateStorageProfile writes all mutable fields of p (test results excluded).
func (s *Store) UpdateStorageProfile(ctx context.Context, p *StorageProfile) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE storage_profiles SET name = ?, provider = ?, endpoint = ?, region = ?, public_bucket = ?,
		  private_bucket = ?, public_base_url = ?, private_base_url = ?, credentials_enc = ?, is_platform = ?,
		  updated_at = ? WHERE id = ?`,
		p.Name, p.Provider, p.Endpoint, p.Region, p.PublicBucket, p.PrivateBucket, p.PublicBaseURL,
		nullStr(p.PrivateBaseURL), p.CredentialsEnc, p.IsPlatform, ms(p.UpdatedAt), p.ID)
	if err != nil {
		return fmt.Errorf("update storage profile: %w", mapErr(err))
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStorageProfileTestResult records the outcome of a connectivity test.
func (s *Store) SetStorageProfileTestResult(ctx context.Context, id string, at time.Time, ok bool) error {
	_, err := s.w.ExecContext(ctx, `UPDATE storage_profiles SET last_test_at = ?, last_test_ok = ? WHERE id = ?`, ms(at), ok, id)
	return mapErr(err)
}

// GetStorageProfile returns a profile by id.
func (s *Store) GetStorageProfile(ctx context.Context, id string) (*StorageProfile, error) {
	return scanStorageProfile(s.r.QueryRowContext(ctx, `SELECT `+storageProfileCols+` FROM storage_profiles WHERE id = ?`, id))
}

// ListStorageProfiles returns all profiles ordered by creation time.
func (s *Store) ListStorageProfiles(ctx context.Context) ([]*StorageProfile, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+storageProfileCols+` FROM storage_profiles ORDER BY created_at, id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*StorageProfile{}
	for rows.Next() {
		p, err := scanStorageProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, mapErr(rows.Err())
}

// DeleteStorageProfile removes a profile.
func (s *Store) DeleteStorageProfile(ctx context.Context, id string) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM storage_profiles WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
