// Command wkagent is the per-server sidecar agent: it sits next to one
// Dragonwilds game server (or supervises it directly), holding the install
// volume and SteamCMD, and exposes a narrow authenticated API for wildskeeper to
// drive. See docs/sidecar-agent.md for the design.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/safwyls/wildskeeper/internal/wkagent"
)

// version is stamped by the release build via
// -ldflags "-X main.version=v0.x.y"; "dev" means a local build.
var version = "dev"

func main() {
	// Container healthcheck mode: probe our own /healthz and exit. The
	// runtime image (steamcmd base) ships neither wget nor curl, so the
	// binary is its own probe.
	if len(os.Args) > 1 && os.Args[1] == "-healthz" {
		addr := envOr("WKAGENT_ADDR", ":8811")
		if strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr
		}
		resp, err := http.Get("http://" + addr + "/healthz")
		if err != nil || resp.StatusCode != http.StatusNoContent {
			os.Exit(1)
		}
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	appID := 0
	if v := os.Getenv("WKAGENT_APP_ID"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			logger.Error("invalid WKAGENT_APP_ID", "value", v)
			os.Exit(1)
		}
		appID = n
	}

	var stopGrace time.Duration
	if v := os.Getenv("WKAGENT_STOP_GRACE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			logger.Error("invalid WKAGENT_STOP_GRACE", "value", v)
			os.Exit(1)
		}
		stopGrace = d
	}
	gamePort := 0
	if v := os.Getenv("WKAGENT_GAME_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65534 {
			// 65535 is excluded on purpose: the game also uses port+1.
			logger.Error("invalid WKAGENT_GAME_PORT", "value", v)
			os.Exit(1)
		}
		gamePort = n
	}

	var autostart *bool
	if v := os.Getenv("WKAGENT_AUTOSTART"); v != "" {
		b := v == "true" || v == "1"
		autostart = &b
	}

	agent, err := wkagent.New(wkagent.Config{
		Token:       os.Getenv("WKAGENT_TOKEN"),
		InstallDir:  envOr("WKAGENT_INSTALL_DIR", "/dragonwilds"),
		SteamCmd:    envOr("WKAGENT_STEAMCMD", "steamcmd"),
		AppID:       appID,
		Mode:        envOr("WKAGENT_MODE", "companion"),
		GameCommand: os.Getenv("WKAGENT_GAME_CMD"),
		GameArgs:    strings.Fields(os.Getenv("WKAGENT_GAME_ARGS")),
		Launch: wkagent.LaunchConfig{
			// The initial selection only applies to an install that has
			// never been told otherwise — the persisted choice wins, so
			// redeploying the container doesn't silently change which build
			// the server runs.
			Profile:      envOr("WKAGENT_LAUNCH_PROFILE", wkagent.ProfileNative),
			WineBin:      os.Getenv("WKAGENT_WINE_BIN"),
			WinePrefix:   os.Getenv("WKAGENT_WINE_PREFIX"),
			GameExe:      os.Getenv("WKAGENT_GAME_EXE"),
			NativeScript: os.Getenv("WKAGENT_NATIVE_SCRIPT"),
		},
		GamePort:        gamePort,
		StopGrace:       stopGrace,
		AdminPassword:   os.Getenv("WKAGENT_ADMIN_PASSWORD"),
		OwnerID:         os.Getenv("WKAGENT_OWNER_ID"),
		ServerName:      os.Getenv("WKAGENT_SERVER_NAME"),
		WorldName:       os.Getenv("WKAGENT_WORLD_NAME"),
		DockerHost:      os.Getenv("WKAGENT_DOCKER_HOST"),
		DataRoot:        os.Getenv("WKAGENT_DATA_ROOT"),
		PublicHost:      os.Getenv("WKAGENT_PUBLIC_HOST"),
		DefaultRunAs:    os.Getenv("WKAGENT_DEFAULT_RUN_AS"),
		DefaultImageTag: os.Getenv("WKAGENT_DEFAULT_IMAGE_TAG"),
		Autostart:       autostart,
		Version:         version,
		Logger:          logger,
	})
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	// Supervisor boot: install if missing, then start per desired state.
	go agent.Run()

	addr := envOr("WKAGENT_ADDR", ":8811")
	logger.Info("wkagent listening", "addr", addr, "version", version, "apiVersion", wkagent.APIVersion)
	if err := http.ListenAndServe(addr, agent.Handler()); err != nil {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
