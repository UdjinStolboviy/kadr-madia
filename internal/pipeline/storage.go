package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
)

// target is a resolved storage profile with a ready driver.
type target struct {
	profile *db.StorageProfile
	driver  storage.Driver
}

// bucket returns the bucket that stores renditions of the given visibility.
func (t *target) bucket(visibility string) string {
	if visibility == "public" {
		return t.profile.PublicBucket
	}
	return t.profile.PrivateBucket
}

// publicURL is the CDN URL of a public object.
func (t *target) publicURL(key string) string {
	return strings.TrimRight(t.profile.PublicBaseURL, "/") + "/" + key
}

type cachedDriver struct {
	updatedAt time.Time
	driver    storage.Driver
}

// drivers caches one driver per storage profile, rebuilt when the profile
// changes.
type drivers struct {
	mu    sync.Mutex
	cache map[string]cachedDriver
}

// storageCredentials mirrors the plaintext of storage_profiles.credentials_enc
// written by the admin API.
type storageCredentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// storageFor resolves the storage profile of a project: its own profile, or
// the platform profile when none is assigned.
func (s *Service) storageFor(ctx context.Context, p *db.Project) (*target, error) {
	if p.StorageProfileID != nil {
		return s.storageByProfile(ctx, *p.StorageProfileID)
	}
	profiles, err := s.store.ListStorageProfiles(ctx)
	if err != nil {
		return nil, err
	}
	for _, sp := range profiles {
		if sp.IsPlatform {
			return s.targetFor(sp)
		}
	}
	return nil, apiErr(http.StatusConflict, CodeStorageMissing,
		"project %s has no storage profile and no platform profile exists; assign one in the admin console", p.Slug)
}

func (s *Service) storageByProfile(ctx context.Context, profileID string) (*target, error) {
	sp, err := s.store.GetStorageProfile(ctx, profileID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, apiErr(http.StatusConflict, CodeStorageMissing, "storage profile %s does not exist", profileID)
	}
	if err != nil {
		return nil, err
	}
	return s.targetFor(sp)
}

func (s *Service) targetFor(sp *db.StorageProfile) (*target, error) {
	s.drivers.mu.Lock()
	defer s.drivers.mu.Unlock()
	if c, ok := s.drivers.cache[sp.ID]; ok && c.updatedAt.Equal(sp.UpdatedAt) {
		return &target{profile: sp, driver: c.driver}, nil
	}
	pt, err := s.keyring.Decrypt(sp.CredentialsEnc, []byte("storage_profile:"+sp.ID))
	if err != nil {
		return nil, fmt.Errorf("decrypt credentials of storage profile %s: %w", sp.ID, err)
	}
	var creds storageCredentials
	if err := json.Unmarshal(pt, &creds); err != nil {
		return nil, fmt.Errorf("decode credentials of storage profile %s: %w", sp.ID, err)
	}
	d, err := s.newDriver(storage.Config{Provider: sp.Provider, Endpoint: sp.Endpoint, Region: sp.Region,
		AccessKeyID: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey})
	if err != nil {
		return nil, fmt.Errorf("storage profile %s: %w", sp.ID, err)
	}
	s.drivers.cache[sp.ID] = cachedDriver{updatedAt: sp.UpdatedAt, driver: d}
	return &target{profile: sp, driver: d}, nil
}
