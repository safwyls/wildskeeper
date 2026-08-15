// Command wildskeeper is Wildskeeper: a self-hosted management console for
// RuneScape: Dragonwilds dedicated servers.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/safwyls/wildskeeper/internal/advisor"
	"github.com/safwyls/wildskeeper/internal/agentctl"
	"github.com/safwyls/wildskeeper/internal/agentfiles"
	"github.com/safwyls/wildskeeper/internal/api"
	"github.com/safwyls/wildskeeper/internal/backup"
	"github.com/safwyls/wildskeeper/internal/collector"
	"github.com/safwyls/wildskeeper/internal/config"
	"github.com/safwyls/wildskeeper/internal/crypto"
	"github.com/safwyls/wildskeeper/internal/db"
	"github.com/safwyls/wildskeeper/internal/dockerctl"
	"github.com/safwyls/wildskeeper/internal/games/dragonwilds/dwsave"
	"github.com/safwyls/wildskeeper/internal/ilmari"
	"github.com/safwyls/wildskeeper/internal/notify"
	"github.com/safwyls/wildskeeper/internal/savecache"
	"github.com/safwyls/wildskeeper/internal/sched"
	"github.com/safwyls/wildskeeper/internal/store"
	"github.com/safwyls/wildskeeper/internal/watchdog"
	"github.com/safwyls/wildskeeper/web"

	// Populates the game registry. Without it every server row would
	// resolve to "unknown game" — see internal/games.
	_ "github.com/safwyls/wildskeeper/internal/games"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	if err := run(logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// adoptLegacyDB renames a palcon.db left by a pre-rename deployment (the
// app was palcon-derived and kept its DB filename until 2026-08) to the
// current name, sidecar WAL/SHM files included, so the rename doesn't
// silently start an empty database. No-op once wildskeeper.db exists.
func adoptLegacyDB(cfg *config.Config, logger *slog.Logger) {
	if _, err := os.Stat(cfg.DBPath()); err == nil {
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		legacy := filepath.Join(cfg.DataDir, "palcon.db"+suffix)
		if _, err := os.Stat(legacy); err != nil {
			continue
		}
		if err := os.Rename(legacy, cfg.DBPath()+suffix); err != nil {
			logger.Error("adopting legacy palcon.db", "file", legacy, "error", err)
			return
		}
		logger.Info("adopted legacy database file", "from", legacy)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	adoptLegacyDB(cfg, logger)

	sqlDB, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	box, err := crypto.New(cfg.EncryptionKey)
	if err != nil {
		return err
	}
	st := store.New(sqlDB, box)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := api.BootstrapAdmin(ctx, st, cfg.AdminUsername, cfg.AdminPassword); err != nil {
		return err
	}

	distFS, err := web.Dist()
	if err != nil {
		return err
	}

	// Discord notifications: the collector reports reachability changes
	// and player joins/leaves through it, the scheduler restart notices.
	notifier := notify.New(st, logger)

	// Samples server health in the background so the dashboard charts have
	// history to draw, rather than only what's happened since page load.
	// Shutdown is awaited below: it closes out open play sessions.
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		collector.New(st, notifier, logger).Run(ctx)
	}()

	// Resolves each server's save/config to a local path — a bind mount,
	// or a cache mirrored from its wkagent sidecar (phase 2).
	files := agentfiles.New(cfg.DataDir, logger)

	// For agent-backed servers this loop drives the save sync that backups
	// snapshot, and keeps the world-metadata parse warm so the Saves page
	// opens onto a cache hit. The reader is dwsave, the Phase 3 SPUD
	// parser; savecache adds the mtime keying and stale-serving around it.
	worlds := savecache.New[dwsave.World](dwsave.Source{})
	go collector.NewSaveRefresher(st, worlds, files, logger).Run(ctx)

	// Optional: without DOCKER_HOST, power control is simply absent.
	var docker *dockerctl.Client
	if cfg.DockerHost != "" {
		docker, err = dockerctl.New(cfg.DockerHost)
		if err != nil {
			return fmt.Errorf("configuring docker control: %w", err)
		}
		logger.Info("docker control enabled", "endpoint", cfg.DockerHost)
	}

	// Runs scheduled restarts (warnings included) for every server.
	go sched.New(st, notifier, docker, logger).Run(ctx)

	// Crash watchdog: revives watched containers after an unclean exit.
	// Meaningless without docker control, so it only runs alongside it.
	if docker != nil {
		go watchdog.New(st, docker, notifier, logger).Run(ctx)
	}

	// Save backups: zip snapshots of the read-only save mount into the
	// data dataset, on each server's schedule.
	backups := backup.New(st, notifier, logger, cfg.DataDir, files)
	go backups.Run(ctx)

	apiServer := api.New(st, cfg.JWTSecret, logger, docker, notifier, backups, files)
	apiServer.CookieSecure = cfg.CookieSecure
	// The same cache the refresher warms serves GET /servers/{id}/world.
	apiServer.Worlds = worlds
	// Optional: the pal advisor rides whichever model key is set, Anthropic
	// first when both are — a deterministic pick beats erroring on a config
	// most operators set by copying one line from .env.example.
	switch {
	case cfg.AnthropicAPIKey != "":
		apiServer.SetEnvAdvisor(advisor.NewClaude(cfg.AnthropicAPIKey, ""))
		logger.Info("pal advisor enabled", "provider", "anthropic", "source", "env")
	case cfg.GeminiAPIKey != "":
		gem, err := advisor.NewGemini(ctx, cfg.GeminiAPIKey, "")
		if err != nil {
			return fmt.Errorf("configuring gemini advisor: %w", err)
		}
		apiServer.SetEnvAdvisor(gem)
		logger.Info("pal advisor enabled", "provider", "gemini", "source", "env")
	}
	// A key saved through the admin UI wins over the environment. Unusable
	// (rotated ENCRYPTION_KEY, say) is a warning, not a startup failure —
	// the admin can paste a fresh key without touching the host.
	if provider, err := apiServer.LoadStoredAdvisor(ctx); err != nil {
		logger.Warn("stored advisor key unusable", "error", err)
	} else if provider != "" {
		logger.Info("pal advisor enabled", "provider", provider, "source", "ui")
	}
	// Optional one-click provisioning (docs/sidecar-agent.md phase 5).
	// Ilmari wins when both are set: it is the cut-over flag of the
	// migration, and the legacy provisioner stays configured underneath so
	// the fallback is deleting one env var.
	switch {
	case cfg.IlmariURL != "":
		client, err := ilmari.New(cfg.IlmariURL, cfg.IlmariToken)
		if err != nil {
			return fmt.Errorf("configuring ilmari: %w", err)
		}
		apiServer.Provisioner = api.NewIlmariProvisioner(client)
		logger.Info("provisioner enabled", "endpoint", cfg.IlmariURL, "via", "ilmari")
	case cfg.ProvisionerURL != "":
		provisioner, err := agentctl.New(cfg.ProvisionerURL, cfg.ProvisionerToken)
		if err != nil {
			return fmt.Errorf("configuring provisioner: %w", err)
		}
		apiServer.Provisioner = provisioner
		logger.Info("provisioner enabled", "endpoint", cfg.ProvisionerURL, "via", "wkagent")
	}
	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           apiServer.Routes(distFS),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		err := httpServer.Shutdown(shutdownCtx)
		// The collector ends the sessions of whoever is still online on its
		// way out. Exiting without waiting strands those joins, and an
		// unclosed join reads as a session that never ended.
		select {
		case <-collectorDone:
		case <-shutdownCtx.Done():
			logger.Warn("collector did not finish closing open sessions")
		}
		return err
	case err := <-errCh:
		return err
	}
}
