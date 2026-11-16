package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/VictoriaMetrics/metrics"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/admin"
	"github.com/UdjinStolboviy/kadr-madia/internal/api/public"
	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/capabilities"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/sdnotify"
	"github.com/UdjinStolboviy/kadr-madia/web"
)

const shutdownTimeout = 30 * time.Second

func cmdServe(ctx context.Context, args []string, stderr io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("%w: serve takes no arguments", errUsage)
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
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

	ui := web.Dist()
	if ui == nil {
		log.Warn("admin UI not embedded (web/dist missing at build time); serving fallback page")
	}
	adminSrv := admin.New(admin.Config{
		Store: store, Keyring: keyring, Logger: log, Metrics: set, Dev: cfg.Dev, Pepper: cfg.APIKeyPepper,
		Build: admin.BuildInfo{Version: version, Commit: commit, StartedAt: started},
		Caps:  caps, TmpDir: cfg.TmpDir, UI: ui, FallbackHTML: web.FallbackHTML(),
	})
	publicHandler := public.Handler(public.Config{
		Store: store, Logger: log, Metrics: set,
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
	bg.Add(1)
	go func() {
		defer bg.Done()
		sessionJanitor(bgCtx, store, log)
	}()

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
	bgCancel()
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
