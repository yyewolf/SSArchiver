// Package app wires SSArchiver's components together.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/api"
	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/buildinfo"
	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
	"github.com/yyewolf/ssarchiver/internal/viewer"
	"github.com/yyewolf/ssarchiver/internal/web"
)

type Options struct {
	ScoreSaberURL string // tests point this at a fake server
}

type App struct {
	db      *gorm.DB
	Service *service.Service
	Worker  *archiver.Worker
	Handler http.Handler
}

func New(cfg config.Config, opts Options) (*App, error) {
	gdb, err := db.Open(cfg.DBPath())
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(gdb); err != nil {
		_ = db.Close(gdb)
		return nil, err
	}
	store, err := storage.New(cfg.ReplayDir())
	if err != nil {
		_ = db.Close(gdb)
		return nil, fmt.Errorf("app: storage: %w", err)
	}
	limiter := scoresaber.NewLimiter(cfg.HourlyBudget)
	var copts []scoresaber.Option
	if opts.ScoreSaberURL != "" {
		copts = append(copts, scoresaber.WithBaseURL(opts.ScoreSaberURL))
	}
	reg, err := platform.NewRegistry(scoresaber.NewPlatform(scoresaber.NewClient(limiter, copts...), limiter))
	if err != nil {
		_ = db.Close(gdb)
		return nil, fmt.Errorf("app: platforms: %w", err)
	}
	svc := service.New(gdb, store, reg)
	worker := archiver.New(svc)
	vh := viewer.NewHandler(viewer.Bundle(), viewer.DeploySHA)
	if !vh.Available() {
		slog.Warn("ArcViewer bundle not embedded: replays can be downloaded but not watched", "fix", "go generate ./internal/viewer && rebuild")
	}
	w := web.New(web.Deps{Service: svc, Status: worker, Viewer: vh, Config: cfg})
	mux := http.NewServeMux()
	w.Routes(mux)
	api.Register(mux, svc, worker, buildinfo.Version)
	return &App{db: gdb, Service: svc, Worker: worker, Handler: w.Middleware(mux)}, nil
}

func (a *App) Close() error { return db.Close(a.db) }

// Serve runs the HTTP server and the archiver until ctx is cancelled.
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: a.Handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http: %w", err)
		}
		return nil
	})
	g.Go(func() error { return a.Worker.Run(gctx) })
	g.Go(func() error {
		<-gctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	})
	return g.Wait()
}
