// Command mdblistaar runs one Sync and exits non-zero if any part of it failed.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golift.io/starr"
	"golift.io/starr/radarr"
	"golift.io/starr/sonarr"

	"github.com/arsac/mdblistaar/internal/config"
	"github.com/arsac/mdblistaar/internal/mdblist"
	"github.com/arsac/mdblistaar/internal/syncer"
)

const httpTimeout = 30 * time.Second

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if err := run(log); err != nil {
		log.Error("sync failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	s := syncer.New(
		log,
		mdblist.New(
			&http.Client{Timeout: httpTimeout},
			mdblist.DefaultBaseURL,
			cfg.MDBListAPIKey,
			"mdblistaar/"+version,
		),
		radarr.New(&starr.Config{
			APIKey: cfg.Radarr.APIKey, URL: cfg.Radarr.URL, Client: starr.Client(httpTimeout, true),
		}),
		sonarr.New(&starr.Config{
			APIKey: cfg.Sonarr.APIKey, URL: cfg.Sonarr.URL, Client: starr.Client(httpTimeout, true),
		}),
		cfg.DryRun,
	)

	log.InfoContext(ctx, "sync starting",
		"version", version, "source_lists", len(cfg.SourceLists), "dry_run", cfg.DryRun)

	return s.Run(ctx, cfg.SourceLists)
}
