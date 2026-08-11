// Package notify delivers server events to Discord through per-server
// incoming webhooks: reachability changes, player joins/leaves, and
// scheduled-restart notices. Delivery is best-effort — a Discord outage
// must never affect running the game server.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/safwyls/wildskeeper/internal/store"
)

// Discord embed accent colors, decimal RGB.
const (
	colorRed   = 0xd83c3e // down
	colorGreen = 0x3aa55d // back up
	colorBlue  = 0x5865f2 // player activity
	colorAmber = 0xf0b232 // restart notices
)

// Event selects which per-server toggle gates a message.
type Event int

const (
	EventStatus Event = iota
	EventPlayers
	EventRestarts
)

type Notifier struct {
	store  *store.Store
	logger *slog.Logger
	client *http.Client

	// suppress mutes status (down) events per server until the given time,
	// so a scheduled restart doesn't page anyone about a planned outage.
	mu       sync.Mutex
	suppress map[int64]time.Time
}

func New(st *store.Store, logger *slog.Logger) *Notifier {
	return &Notifier{
		store:    st,
		logger:   logger,
		client:   &http.Client{Timeout: 10 * time.Second},
		suppress: make(map[int64]time.Time),
	}
}

// SuppressStatus mutes "server unreachable" notifications for the server
// for the given duration. The scheduler calls this before a planned
// restart; "back online" still goes through, closing the loop.
func (n *Notifier) SuppressStatus(serverID int64, d time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.suppress[serverID] = time.Now().Add(d)
}

func (n *Notifier) statusSuppressed(serverID int64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	until, ok := n.suppress[serverID]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(n.suppress, serverID)
		return false
	}
	return true
}

func (n *Notifier) ServerDown(ctx context.Context, srv *store.Server) {
	if n.statusSuppressed(srv.ID) {
		return
	}
	n.send(ctx, srv, EventStatus, embed{
		Title:       "🔴 Server unreachable",
		Description: fmt.Sprintf("**%s** has stopped responding.", srv.Name),
		Color:       colorRed,
	})
}

func (n *Notifier) ServerUp(ctx context.Context, srv *store.Server, downFor time.Duration) {
	desc := fmt.Sprintf("**%s** is responding again.", srv.Name)
	if downFor > 0 {
		desc = fmt.Sprintf("**%s** is responding again after ~%s down.", srv.Name, formatDuration(downFor))
	}
	n.send(ctx, srv, EventStatus, embed{
		Title:       "🟢 Server back online",
		Description: desc,
		Color:       colorGreen,
	})
}

func (n *Notifier) PlayerJoined(ctx context.Context, srv *store.Server, player string, online int) {
	n.send(ctx, srv, EventPlayers, embed{
		Description: fmt.Sprintf("**%s** joined %s · %d online", escapeMarkdown(player), srv.Name, online),
		Color:       colorBlue,
	})
}

func (n *Notifier) PlayerLeft(ctx context.Context, srv *store.Server, player string, online int) {
	n.send(ctx, srv, EventPlayers, embed{
		Description: fmt.Sprintf("**%s** left %s · %d online", escapeMarkdown(player), srv.Name, online),
		Color:       colorBlue,
	})
}

func (n *Notifier) RestartWarning(ctx context.Context, srv *store.Server, minutes int) {
	unit := "minutes"
	if minutes == 1 {
		unit = "minute"
	}
	n.send(ctx, srv, EventRestarts, embed{
		Title:       "🕐 Restart soon",
		Description: fmt.Sprintf("**%s** restarts in %d %s.", srv.Name, minutes, unit),
		Color:       colorAmber,
	})
}

// SaveOutcome is what the save before a restart achieved. Dragonwilds does
// not save on shutdown, so this is the difference between a clean restart
// and losing everything since the last autosave — which makes it worth
// saying out loud rather than burying in a log line. It lives here because
// rendering events into words is this package's job; the scheduler only
// reports which one happened.
type SaveOutcome int

const (
	// SaveDone: the world was written before the restart.
	SaveDone SaveOutcome = iota
	// SaveUnsupported: this server has no command channel to save through
	// (no dwbridge mod). A capability gap, not a fault — and a standing one,
	// so the restart will cost play time every time until it's closed.
	SaveUnsupported
	// SaveFailed: a save was possible and went wrong. Usually transient.
	SaveFailed
	// SaveSkipped: no game client at all, so nothing was attempted.
	SaveSkipped
)

// String is the phrase used in logs and the audit trail. The Discord
// wording is separate — that one is written for players, this one for
// whoever is reading back what happened.
func (o SaveOutcome) String() string {
	switch o {
	case SaveDone:
		return "world saved"
	case SaveUnsupported:
		return "world not saved (no command bridge)"
	case SaveFailed:
		return "world save failed"
	default:
		return "world save not attempted"
	}
}

// RestartingNow announces the restart, and says whether the world made it to
// disk first. Claiming a save that didn't happen would be worse than saying
// nothing: it's exactly the fact a player needs to know to judge what they
// lost.
func (n *Notifier) RestartingNow(ctx context.Context, srv *store.Server, save SaveOutcome) {
	desc := fmt.Sprintf("**%s** saved its world and is restarting now.", srv.Name)
	switch save {
	case SaveUnsupported:
		desc = fmt.Sprintf("**%s** is restarting now. Its world could not be saved first — no command bridge is running — so anything since the last autosave is lost.", srv.Name)
	case SaveFailed:
		desc = fmt.Sprintf("**%s** is restarting now. The save before the restart failed, so anything since the last autosave may be lost.", srv.Name)
	case SaveSkipped:
		desc = fmt.Sprintf("**%s** is restarting now.", srv.Name)
	}
	n.send(ctx, srv, EventRestarts, embed{
		Title:       "🔄 Scheduled restart",
		Description: desc,
		Color:       colorAmber,
	})
}

func (n *Notifier) WatchdogRestarted(ctx context.Context, srv *store.Server, exitCode, attempt int) {
	n.send(ctx, srv, EventStatus, embed{
		Title:       "🐕 Watchdog restart",
		Description: fmt.Sprintf("**%s** exited with code %d; the watchdog is restarting it (attempt %d).", srv.Name, exitCode, attempt),
		Color:       colorAmber,
	})
}

func (n *Notifier) WatchdogGaveUp(ctx context.Context, srv *store.Server, attempts int) {
	n.send(ctx, srv, EventStatus, embed{
		Title:       "🛑 Watchdog stopped retrying",
		Description: fmt.Sprintf("**%s** keeps crashing (%d restarts in a row). The watchdog is standing down until the server stays up or someone intervenes.", srv.Name, attempts),
		Color:       colorRed,
	})
}

// BackupFailed fires once per failure streak (the caller dedupes) — a
// backup that quietly stops working is the failure mode backups exist to
// prevent.
func (n *Notifier) BackupFailed(ctx context.Context, srv *store.Server, cause error) {
	n.send(ctx, srv, EventStatus, embed{
		Title:       "💾 Backup failed",
		Description: fmt.Sprintf("**%s**'s scheduled save backup failed: %s", srv.Name, cause),
		Color:       colorRed,
	})
}

// Test sends a test message and, unlike every other notification, reports
// failure to the caller — it exists so the settings UI can prove the pasted
// webhook actually works.
func (n *Notifier) Test(ctx context.Context, srv *store.Server) error {
	w, err := n.store.GetDiscordWebhook(ctx, srv.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("no Discord webhook configured for this server")
		}
		return err
	}
	return n.post(ctx, w.WebhookURL, embed{
		Title:       "👋 Wildskeeper test",
		Description: fmt.Sprintf("Notifications for **%s** are wired up.", srv.Name),
		Color:       colorGreen,
	})
}

type embed struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description"`
	Color       int    `json:"color"`
}

// send loads the server's webhook config and delivers the embed if the
// event's toggle is on. Failures are logged, never returned: notifications
// ride along on collector/scheduler loops that have their own jobs to do.
func (n *Notifier) send(ctx context.Context, srv *store.Server, ev Event, e embed) {
	w, err := n.store.GetDiscordWebhook(ctx, srv.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			n.logger.Error("notify: loading webhook config", "server", srv.ID, "error", err)
		}
		return
	}
	if !w.Enabled {
		return
	}
	switch ev {
	case EventStatus:
		if !w.OnStatus {
			return
		}
	case EventPlayers:
		if !w.OnPlayers {
			return
		}
	case EventRestarts:
		if !w.OnRestarts {
			return
		}
	}
	if err := n.post(ctx, w.WebhookURL, e); err != nil {
		n.logger.Warn("notify: discord delivery failed", "server", srv.ID, "error", err)
	}
}

func (n *Notifier) post(ctx context.Context, webhookURL string, e embed) error {
	payload, err := json.Marshal(map[string]any{
		"username": "Wildskeeper",
		"embeds":   []embed{e},
		// Player names are arbitrary input; never let one ping a role or
		// @everyone, no matter what it contains.
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	// Discord returns 204 on success; 4xx means a bad/revoked webhook.
	if res.StatusCode >= 300 {
		return fmt.Errorf("discord returned %s", res.Status)
	}
	return nil
}

// ValidateWebhookURL accepts only real Discord incoming-webhook endpoints,
// so a typo'd or malicious URL can't turn Wildskeeper into a generic HTTP
// client for someone else's server.
func ValidateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("not a valid URL")
	}
	if u.Scheme != "https" {
		return errors.New("webhook URL must be https")
	}
	switch u.Hostname() {
	case "discord.com", "discordapp.com", "ptb.discord.com", "canary.discord.com":
	default:
		return errors.New("webhook URL must be a discord.com webhook")
	}
	if !strings.HasPrefix(u.Path, "/api/webhooks/") {
		return errors.New("webhook URL must start with /api/webhooks/")
	}
	return nil
}

// escapeMarkdown neutralizes Discord formatting in player names, which are
// arbitrary user input.
func escapeMarkdown(s string) string {
	r := strings.NewReplacer("*", "\\*", "_", "\\_", "~", "\\~", "`", "\\`", "|", "\\|", ">", "\\>", "#", "\\#")
	return r.Replace(s)
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return "1m"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
