// Package api wires up the HTTP server: auth, server CRUD, and the
// per-server RCON/REST actions, plus serving the built React SPA.
package api

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/safwyls/wildskeeper/internal/agentfiles"
	"github.com/safwyls/wildskeeper/internal/backup"
	"github.com/safwyls/wildskeeper/internal/dockerctl"
	"github.com/safwyls/wildskeeper/internal/games/dragonwilds/dwsave"
	"github.com/safwyls/wildskeeper/internal/notify"
	"github.com/safwyls/wildskeeper/internal/savecache"
	"github.com/safwyls/wildskeeper/internal/store"
)

type Server struct {
	store     *store.Store
	jwtSecret []byte
	logger    *slog.Logger
	// CookieSecure marks session cookies Secure (set from COOKIE_SECURE
	// for deployments behind TLS; default off for plain-HTTP LAN use).
	CookieSecure bool
	// docker is nil when no DOCKER_HOST is set; power control is then
	// simply unavailable rather than broken.
	docker *dockerctl.Client
	// notifier delivers Discord messages; the API only uses it for the
	// "send a test message" endpoint.
	notifier *notify.Notifier
	// backups runs the snapshot schedule and owns the archive directory.
	backups *backup.Runner
	// files resolves save/config to a local path — a bind mount, or the
	// agent-synced cache (docs/sidecar-agent.md phase 2).
	files *agentfiles.Syncer
	// Provisioner, when set (like CookieSecure, assigned after New), lets
	// the new-server wizard deploy stacks itself instead of handing the
	// operator a file. Two implementations during the Ilmari migration:
	// the legacy provisioner-mode wkagent, and the shared Ilmari host
	// service — see provisioner.go.
	Provisioner Provisioner
	// Worlds, when set (assigned after New, like Provisioner), is the
	// Dragonwilds save-reader cache behind GET /servers/{id}/world. Nil
	// means the endpoint reports the world as unavailable — the pre-Phase-3
	// behavior, and what api tests that don't care about saves get.
	Worlds *savecache.Cache[dwsave.World]
	// The pal advisor has two possible sources, resolved in advisor():
	// a key saved through the admin UI (uiAdvisor, encrypted in the store)
	// wins over one from the environment (envAdvisor, set by main). Both
	// nil means the feature is simply absent — see internal/advisor.
	advisorMu    sync.RWMutex
	envAdvisor   AdvisorClient
	uiAdvisor    AdvisorClient
	loginLimiter *loginLimiter
}

func New(st *store.Store, jwtSecret []byte, logger *slog.Logger, docker *dockerctl.Client, notifier *notify.Notifier, backups *backup.Runner, files *agentfiles.Syncer) *Server {
	return &Server{store: st, jwtSecret: jwtSecret, logger: logger, docker: docker, notifier: notifier, backups: backups, files: files, loginLimiter: newLoginLimiter()}
}

// Routes builds the full HTTP handler: JSON API under /api, and the built
// frontend (staticFS) for everything else, with an index.html fallback so
// client-side routing works on refresh/deep links.
func (s *Server) Routes(staticFS fs.FS) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	// The pals payload is the largest thing served (tens of MB of JSON on a
	// big world) and compresses ~10x; this also covers the JS bundles.
	r.Use(middleware.Compress(5))

	r.Route("/api", func(r chi.Router) {
		// No endpoint takes a body anywhere near this size; cap it so
		// json.Decode can't be fed an arbitrarily large request.
		r.Use(maxBodyBytes(1 << 20))
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "not found")
		})
		r.Post("/login", s.handleLogin)

		// The only unauthenticated data endpoint: token-gated, read-only,
		// served entirely from Wildskeeper's own database. See public.go.
		r.Get("/public/status/{token}", s.handlePublicStatus)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Post("/logout", s.handleLogout)
			r.Get("/me", s.handleMe)
			r.Post("/me/password", s.handleChangeOwnPassword)

			// Registered flat rather than via r.Route: a subrouter's "/"
			// only matches /users/, so POST /api/users 404s.
			r.With(s.requireAdmin).Get("/users", s.handleListUsers)
			r.With(s.requireAdmin).Post("/users", s.handleCreateUser)
			r.With(s.requireAdmin).Put("/users/{userID}", s.handleUpdateUser)
			r.With(s.requireAdmin).Delete("/users/{userID}", s.handleDeleteUser)

			// Advisor key management: process-wide (the advisor is one
			// feature, not a per-server one), admin-only, stored encrypted.
			// A key saved here wins over one from the environment.
			r.With(s.requireAdmin).Put("/advisor/key", s.handleSetAdvisorKey)
			r.With(s.requireAdmin).Delete("/advisor/key", s.handleDeleteAdvisorKey)
			r.With(s.requireAdmin).Put("/advisor/settings", s.handleSetAdvisorSettings)
			// Change which model a saved key runs, without re-entering it.
			r.With(s.requireAdmin).Put("/advisor/key/model", s.handleSetAdvisorKeyModel)
			r.Put("/me/advisor-key/model", s.handleSetUserAdvisorKeyModel)
			// A user's own key, shadowing the shared one for their requests
			// only. Any signed-in user; scoped to their account.
			r.Put("/me/advisor-key", s.handleSetUserAdvisorKey)
			r.Delete("/me/advisor-key", s.handleDeleteUserAdvisorKey)
			// Embedded project docs, for the advisor's docs-search tool.
			r.Get("/docs", s.handleDocs)

			r.Get("/servers", s.handleListServers)
			r.With(s.requireAdmin).Post("/servers", s.handleCreateServer)
			// New-server wizard: registers the row and generates the
			// supervisor stack file for the human to deploy. Defaults and
			// discovery let the wizard prefill from the provisioner's
			// config instead of asking.
			r.With(s.requireAdmin).Post("/servers/provision", s.handleProvisionServer)
			r.With(s.requireAdmin).Get("/servers/provision/defaults", s.handleProvisionDefaults)
			r.With(s.requireAdmin).Get("/servers/provision/discover", s.handleProvisionDiscover)
			r.With(s.requireAdmin).Post("/servers/adopt", s.handleAdoptServer)
			r.Route("/servers/{serverID}", func(r chi.Router) {
				r.Get("/", s.handleGetServer)
				r.With(s.requireAdmin).Put("/", s.handleUpdateServer)
				r.With(s.requireAdmin).Delete("/", s.handleDeleteServer)

				r.Get("/info", s.handleServerInfo)
				r.Get("/players", s.handleServerPlayers)
				// What this server's commands can actually do, asked
				// before offering them rather than discovered by a 501.
				r.Get("/capabilities", s.handleServerCapabilities)
				r.With(s.requirePermission(store.PermBroadcast)).Post("/broadcast", s.handleServerBroadcast)
				r.With(s.requirePermission(store.PermModerate)).Post("/kick", s.handleServerKick)
				r.With(s.requirePermission(store.PermModerate)).Post("/ban", s.handleServerBan)
				r.With(s.requirePermission(store.PermModerate)).Post("/unban", s.handleServerUnban)
				r.With(s.requirePermission(store.PermSave)).Post("/save", s.handleServerSave)
				r.With(s.requirePermission(store.PermShutdown)).Post("/shutdown", s.handleServerShutdown)

				// Container power. Reading state is fine for anyone
				// signed in; changing it needs the grant.
				r.Get("/container", s.handleContainerStatus)
				r.With(s.requirePermission(store.PermPower)).Post("/container/{action}", s.handleContainerAction)
				r.With(s.requirePermission(store.PermPower)).Get("/container/logs", s.handleContainerLogs)
				// SteamCMD repair & update — power territory: they exist
				// to get a broken container updating again. Runs via the
				// server's wkagent when configured, else the local
				// install-path mount (cache clear only).
				// Which game build the agent launches. Reading is open to
				// anyone signed in (it explains why commands do or don't
				// work); changing it is power territory — it decides what
				// the next start actually runs.
				r.Get("/launch", s.handleGetLaunch)
				r.With(s.requirePermission(store.PermPower)).Put("/launch", s.handleSetLaunch)
				// Rebuild this server's agent on another wkagent image.
				// Admin-only: it destroys and recreates a container, which
				// is provisioning, not day-to-day power.
				r.With(s.requireAdmin).Post("/agent/image", s.handleRecreateAgent)
				// One-click mod support: the agent copies its baked-in
				// UE4SS+dwbridge kit next to the exe. Power territory like
				// the launch profile it depends on — it changes what the
				// next start runs, not the machine it runs on.
				r.With(s.requirePermission(store.PermPower)).Post("/bridge/install", s.handleInstallBridge)
				r.With(s.requirePermission(store.PermPower)).Post("/steam-cache/clear", s.handleClearSteamCache)
				r.With(s.requirePermission(store.PermPower)).Post("/steam/update", s.handleSteamUpdateStart)
				r.With(s.requirePermission(store.PermPower)).Get("/steam/update", s.handleSteamUpdateStatus)
				r.Get("/settings", s.handleServerSettings)

				// Settings-ini editor (DedicatedServer.ini here; the codec
				// is per-game, see config.go). Gated even for reading: the
				// file holds the admin/join passwords in the clear.
				r.With(s.requirePermission(store.PermSettings)).Get("/config", s.handleGetConfig)
				r.With(s.requirePermission(store.PermSettings)).Put("/config", s.handleUpdateConfig)
				r.With(s.requirePermission(store.PermSettings)).Post("/config/rotate-admin-password", s.handleRotateAdminPassword)

				// Automation: restart schedules are visible to anyone
				// signed in ("when's the next restart?" is player-facing
				// information); changing them, and everything Discord, is
				// admin infrastructure config.
				r.Get("/automation", s.handleGetAutomation)
				r.With(s.requireAdmin).Post("/schedules", s.handleCreateSchedule)
				r.With(s.requireAdmin).Put("/schedules/{scheduleID}", s.handleUpdateSchedule)
				r.With(s.requireAdmin).Delete("/schedules/{scheduleID}", s.handleDeleteSchedule)
				r.With(s.requireAdmin).Put("/discord", s.handleUpdateDiscord)
				r.With(s.requireAdmin).Delete("/discord", s.handleDeleteDiscord)
				r.With(s.requireAdmin).Post("/discord/test", s.handleTestDiscord)
				r.With(s.requireAdmin).Put("/watchdog", s.handleUpdateWatchdog)
				r.With(s.requireAdmin).Put("/public", s.handleUpdatePublicStatus)

				// The world as the save file tells it — see world.go for
				// why it shares the backups' admin-only gate.
				r.With(s.requireAdmin).Get("/world", s.handleServerWorld)

				// Save backups: the archive is the whole world, so even
				// listing is admin-only.
				r.With(s.requireAdmin).Get("/backups", s.handleListBackups)
				r.With(s.requireAdmin).Put("/backups/settings", s.handleUpdateBackupSettings)
				r.With(s.requireAdmin).Post("/backups/run", s.handleRunBackup)
				r.With(s.requireAdmin).Get("/backups/{name}/download", s.handleDownloadBackup)
				r.With(s.requireAdmin).Delete("/backups/{name}", s.handleDeleteBackup)

				// Player join/leave history is player-facing; the audit
				// trail names which admin did what and stays admin-only.
				r.Get("/activity", s.handleServerActivity)
				r.With(s.requireAdmin).Get("/audit", s.handleServerAudit)

				r.Get("/metrics", s.handleServerMetrics)
				r.Get("/metrics/history", s.handleServerMetricsHistory)
				// Pal advisor chat (Claude-backed). GET says whether the
				// process has a key at all; POST answers one question.
				r.Get("/advisor", s.handleAdvisorStatus)
				r.Post("/advisor", s.handleAdvisorChat)

				// Who can see what. Admin-only in both directions: the list of
				// players who asked to be hidden is itself the sort of thing
				// the hiding is meant to keep quiet.
				r.With(s.requireAdmin).Get("/visibility", s.handleServerVisibility)
				r.With(s.requireAdmin).Put("/visibility", s.handleUpdateServerVisibility)
			})
		})
	})

	r.NotFound(spaHandler(staticFS))

	return r
}

// maxBodyBytes caps request body reads; exceeding it makes json.Decode
// fail, which handlers already report as a 400.
func maxBodyBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
