package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/VictoriaMetrics/metrics"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/admin"
	"github.com/UdjinStolboviy/kadr-madia/internal/api/public"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/capabilities"
	"github.com/UdjinStolboviy/kadr-madia/internal/crypto"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/devstorage"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
	"github.com/UdjinStolboviy/kadr-madia/internal/sdnotify"
	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
	"github.com/UdjinStolboviy/kadr-madia/web"
)

const (
	shutdownTimeout = 30 * time.Second
	// jobDrainTimeout is how long running jobs may finish after a shutdown
	// signal before their leases are released back to the queue.
	jobDrainTimeout     = 20 * time.Second
	webhookDrainTimeout = 10 * time.Second
)

func cmdServe(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fakeEngine := fs.Bool("fake-engine", false, "use a fake image engine that writes placeholder files (development without libvips)")
	devStorage := fs.Bool("dev-storage", false, "run an in-process S3-compatible server and a \"Dev storage\" platform profile (requires KADR_DEV=1; address: KADR_DEV_STORAGE_ADDR, default "+devstorage.DefaultAddr+")")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: serve takes no positional arguments", errUsage)
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if *devStorage && !cfg.Dev {
		return fmt.Errorf("%w: --dev-storage is a development tool and requires KADR_DEV=1", errUsage)
	}
	log := newLogger(cfg, stderr)
	slog.SetDefault(log)
	started := time.Now()

	keyring, err := loadKeyring(cfg, log)
	if err != nil {
		return err
	}

	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer database.Close()

	status, err := db.DefaultMigrator().Status(ctx, database.R)
	if err != nil {
		return err
	}
	pending := 0
	for _, s := range status {
		if s.AppliedAt.IsZero() {
			pending++
		} else if s.ChecksumChanged {
			log.Warn("applied migration differs from the embedded file", "version", s.Version, "name", s.Name)
		}
	}
	if pending > 0 {
		return fmt.Errorf("database has %d pending migration(s); run `kadr migrate up` first", pending)
	}
	store := db.NewStore(database)

	if *devStorage {
		ds, err := startDevStorage(ctx, cfg.DBPath, store, keyring, log)
		if err != nil {
			return err
		}
		defer func() {
			shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = ds.Shutdown(shCtx)
		}()
	}

	// Probe external tools in the background; /admin/api/system shows
	// "probe in progress" until it finishes.
	caps := capabilities.NewHolder()
	go func() {
		r := capabilities.Probe(ctx, cfg.TmpDir)
		caps.Set(r)
		log.Info("capabilities probed", "vips", r.Vips.OK, "vips_avif", r.VipsAVIF.OK, "ffmpeg", r.FFmpeg.OK,
			"ffprobe", r.FFprobe.OK, "rclone", r.Rclone.OK, "vaapi", r.VAAPI.OK)
	}()

	set := metrics.NewSet()
	set.NewGauge("kadr_db_wal_bytes", func() float64 { _, wal := database.SizeInfo(); return float64(wal) })
	set.NewGauge("kadr_tmp_disk_free_bytes", func() float64 {
		free, _ := capabilities.DiskFree(cfg.TmpDir)
		return float64(free)
	})

	// Media pipeline: queue, image engine, webhooks.
	q := queue.New(store, nil)
	dispatcher := webhook.New(webhook.Config{Store: store, Keyring: keyring, Logger: log, Metrics: set})
	svc := pipeline.New(pipeline.Config{
		Store: store, Queue: q, Engine: newImageEngine(cfg.PoolImage, *fakeEngine, log), Video: newVideoEngine(log), Keyring: keyring,
		Logger: log, Metrics: set, TmpDir: cfg.TmpDir, Webhooks: dispatcher,
	})
	if err := svc.CleanStaleWorkDirs(ctx); err != nil {
		log.Warn("clean stale work dirs", "err", err)
	}
	runner := queue.NewRunner(q, queue.RunnerConfig{
		Pools:  map[string]int{queue.PoolImage: cfg.PoolImage, queue.PoolVideo: cfg.PoolVideo, queue.PoolMaintenance: 1},
		Logger: log, Metrics: set,
	})
	svc.Register(runner)

	ui := web.Dist()
	if ui == nil {
		log.Warn("admin UI not embedded (web/dist missing at build time); serving fallback page")
	}
	adminSrv := admin.New(admin.Config{
		Store: store, Keyring: keyring, Logger: log, Metrics: set, Dev: cfg.Dev, Pepper: cfg.APIKeyPepper,
		Build: admin.BuildInfo{Version: version, Commit: commit, StartedAt: started},
		Caps:  caps, TmpDir: cfg.TmpDir, UI: ui, FallbackHTML: web.FallbackHTML(), Webhooks: dispatcher,
	})
	publicHandler := public.Handler(public.Config{
		Store: store, Logger: log, Metrics: set, Pipeline: svc, TmpDir: cfg.TmpDir,
		Verifier: &auth.KeyVerifier{Store: store, Pepper: cfg.APIKeyPepper},
	})

	servers := []*namedServer{
		{name: "admin", addr: cfg.AdminAddr, srv: newHTTPServer(adminSrv.Handler(), log)},
		{name: "public", addr: cfg.PublicAddr, srv: newHTTPServer(publicHandler, log)},
	}
	// Bind both listeners before reporting readiness so port conflicts fail fast.
	for _, s := range servers {
		ln, err := net.Listen("tcp", s.addr)
		if err != nil {
			for _, o := range servers {
				if o.ln != nil {
					o.ln.Close()
				}
			}
			return fmt.Errorf("listen %s (%s): %w", s.name, s.addr, err)
		}
		s.ln = ln
	}

	errCh := make(chan error, len(servers))
	for _, s := range servers {
		log.Info("listening", "server", s.name, "addr", s.ln.Addr().String())
		go func(s *namedServer) {
			if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("%s server: %w", s.name, err)
			}
		}(s)
	}

	bgCtx, bgCancel := context.WithCancel(ctx)
	var bg sync.WaitGroup
	bg.Add(2)
	go func() {
		defer bg.Done()
		sessionJanitor(bgCtx, store, log)
	}()
	go func() {
		defer bg.Done()
		q.RunScheduler(bgCtx, pipeline.Schedules(), log)
	}()
	runner.Start(ctx)
	dispatcher.Start(ctx)

	if ok, err := sdnotify.Notify(sdnotify.Ready); err != nil {
		log.Warn("sd_notify READY failed", "err", err)
	} else if ok {
		log.Debug("sd_notify READY sent")
	}
	log.Info("kadr started", "version", version, "commit", commit, "dev", cfg.Dev)

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case runErr = <-errCh:
		log.Error("server failed", "err", runErr)
	}

	_, _ = sdnotify.Notify(sdnotify.Stopping)
	shCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func(s *namedServer) {
			defer wg.Done()
			if err := s.srv.Shutdown(shCtx); err != nil {
				log.Error("graceful shutdown failed", "server", s.name, "err", err)
			}
		}(s)
	}
	wg.Wait()
	// HTTP is drained; stop claiming jobs, let running ones finish (their
	// leases are released after jobDrainTimeout), then stop webhooks.
	bgCancel()
	var drain sync.WaitGroup
	drain.Add(2)
	go func() { defer drain.Done(); runner.Shutdown(jobDrainTimeout) }()
	go func() { defer drain.Done(); dispatcher.Shutdown(webhookDrainTimeout) }()
	drain.Wait()
	bg.Wait()
	log.Info("kadr stopped")
	return runErr
}

type namedServer struct {
	name string
	addr string
	srv  *http.Server
	ln   net.Listener
}

func newHTTPServer(h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second, // storage tests can take a while
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// sessionJanitor purges expired admin sessions hourly.
func sessionJanitor(ctx context.Context, store *db.Store, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := store.DeleteExpiredSessions(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Warn("purge expired sessions", "err", err)
		} else if n > 0 {
			log.Debug("purged expired sessions", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// startDevStorage starts the in-process S3 server (data next to the database
// in devstorage/) and makes sure the "Dev storage" profile points at it.
func startDevStorage(ctx context.Context, dbPath string, store *db.Store, kr *crypto.Keyring, log *slog.Logger) (*devstorage.Server, error) {
	ds, err := devstorage.Start(devstorage.Config{
		Addr:   os.Getenv("KADR_DEV_STORAGE_ADDR"),
		Dir:    filepath.Join(filepath.Dir(dbPath), "devstorage"),
		Logger: log,
	})
	if err != nil {
		return nil, err
	}
	p, created, err := devstorage.EnsureProfile(ctx, store, kr, ds.Endpoint, time.Now())
	if err != nil {
		_ = ds.Shutdown(context.Background())
		return nil, err
	}
	if created && !p.IsPlatform {
		log.Warn("another platform storage profile exists; assign \"Dev storage\" to projects explicitly", "profile_id", p.ID)
	}
	log.Warn("dev storage running (no authentication, development only)", "endpoint", ds.Endpoint,
		"public_base_url", ds.PublicBaseURL(), "profile_id", p.ID, "profile_created", created)
	return ds, nil
}
