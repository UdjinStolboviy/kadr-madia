// Package devstorage runs an in-process S3-compatible server (gofakes3) for
// local development and contract tests (`KADR_DEV=1 kadr serve
// --dev-storage`). It is never meant for production: it does not verify
// request signatures and keeps everything on the local disk.
//
// Layout: path-style buckets kadr-public and kadr-private on
// http://127.0.0.1:9000 (KADR_DEV_STORAGE_ADDR). Public objects are served
// at http://<addr>/kadr-public/<key>, which is also the public base URL of
// the "Dev storage" platform profile created by EnsureProfile. Presigned PUTs
// work from browsers: the server answers CORS preflights for any origin.
package devstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3afero"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/spf13/afero"

	"github.com/UdjinStolboviy/kadr-madia/internal/crypto"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// Defaults of the dev storage server and its profile.
const (
	DefaultAddr   = "127.0.0.1:9000"
	PublicBucket  = "kadr-public"
	PrivateBucket = "kadr-private"
	ProfileName   = "Dev storage"
	// Static credentials: gofakes3 accepts any signature, but the S3 driver
	// requires non-empty keys.
	AccessKeyID     = "kadr-dev"
	SecretAccessKey = "kadr-dev-secret"
	region          = "us-east-1"
)

// Config configures Start.
type Config struct {
	// Addr is the listen address (default DefaultAddr). It must be a
	// loopback address: the server has no authentication.
	Addr string
	// Dir persists buckets and objects on disk. Empty keeps them in memory
	// (lost on restart).
	Dir    string
	Logger *slog.Logger
}

// Server is a running dev storage server.
type Server struct {
	srv *http.Server
	ln  net.Listener
	// Endpoint is the S3 endpoint, e.g. http://127.0.0.1:9000.
	Endpoint string
}

// PublicBaseURL is where public objects are served.
func (s *Server) PublicBaseURL() string { return s.Endpoint + "/" + PublicBucket }

// Start listens on cfg.Addr, creates the buckets and serves S3 requests in
// the background until Shutdown.
func Start(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("devstorage: invalid address %q: %w", cfg.Addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("devstorage: address %q must be a loopback address (the server has no authentication)", cfg.Addr)
	}

	backend, err := newBackend(cfg.Dir)
	if err != nil {
		return nil, err
	}
	for _, b := range []string{PublicBucket, PrivateBucket} {
		exists, err := backend.BucketExists(b)
		if err != nil {
			return nil, fmt.Errorf("devstorage: check bucket %s: %w", b, err)
		}
		if !exists {
			if err := backend.CreateBucket(b); err != nil {
				return nil, fmt.Errorf("devstorage: create bucket %s: %w", b, err)
			}
		}
	}
	faker := gofakes3.New(backend,
		gofakes3.WithLogger(gofakes3.DiscardLog()),
		// Presigned URLs carry their date in the query and stay valid for
		// minutes; do not reject them as skewed.
		gofakes3.WithTimeSkewLimit(0),
	)

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("devstorage: listen %s: %w", cfg.Addr, err)
	}
	s := &Server{
		ln:       ln,
		Endpoint: "http://" + ln.Addr().String(),
		srv: &http.Server{
			Handler:           faker.Server(),
			ReadHeaderTimeout: 10 * time.Second,
			ErrorLog:          slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelWarn),
		},
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			cfg.Logger.Error("dev storage server failed", "err", err)
		}
	}()
	return s, nil
}

// Shutdown stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

func newBackend(dir string) (gofakes3.Backend, error) {
	if dir == "" {
		return s3mem.New(), nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("devstorage: create %s: %w", dir, err)
	}
	b, err := s3afero.MultiBucket(afero.NewBasePathFs(afero.NewOsFs(), dir))
	if err != nil {
		return nil, fmt.Errorf("devstorage: open %s: %w", dir, err)
	}
	return b, nil
}

// credentials mirrors the plaintext of storage_profiles.credentials_enc
// written by the admin API.
type credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// EnsureProfile makes sure a storage profile named ProfileName points at
// endpoint. It creates the profile when absent (as the platform profile
// unless another platform profile exists) and updates its endpoint and
// public base URL when the address changed. Credentials are encrypted like
// those of any other profile. It reports whether the profile was created.
func EnsureProfile(ctx context.Context, store *db.Store, kr *crypto.Keyring, endpoint string, now time.Time) (*db.StorageProfile, bool, error) {
	publicBase := endpoint + "/" + PublicBucket
	profiles, err := store.ListStorageProfiles(ctx)
	if err != nil {
		return nil, false, err
	}
	hasPlatform := false
	for _, p := range profiles {
		if p.Name == ProfileName {
			if p.Endpoint == endpoint && p.PublicBaseURL == publicBase {
				return p, false, nil
			}
			p.Endpoint, p.PublicBaseURL, p.UpdatedAt = endpoint, publicBase, now
			if err := store.UpdateStorageProfile(ctx, p); err != nil {
				return nil, false, fmt.Errorf("devstorage: update profile: %w", err)
			}
			return p, false, nil
		}
		hasPlatform = hasPlatform || p.IsPlatform
	}
	p := &db.StorageProfile{
		ID: db.NewID(db.PrefixStorageProfile), Name: ProfileName, Provider: "s3", Endpoint: endpoint, Region: region,
		PublicBucket: PublicBucket, PrivateBucket: PrivateBucket, PublicBaseURL: publicBase,
		IsPlatform: !hasPlatform, CreatedAt: now, UpdatedAt: now,
	}
	pt, err := json.Marshal(credentials{AccessKeyID: AccessKeyID, SecretAccessKey: SecretAccessKey})
	if err != nil {
		return nil, false, err
	}
	if p.CredentialsEnc, err = kr.Encrypt(pt, []byte("storage_profile:"+p.ID)); err != nil {
		return nil, false, fmt.Errorf("devstorage: encrypt credentials: %w", err)
	}
	if err := store.CreateStorageProfile(ctx, p); err != nil {
		return nil, false, fmt.Errorf("devstorage: create profile: %w", err)
	}
	return p, true, nil
}
