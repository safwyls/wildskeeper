package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/safwyls/wildskeeper/internal/crypto"
	"github.com/safwyls/wildskeeper/internal/db"
	"github.com/safwyls/wildskeeper/internal/notify"
	"github.com/safwyls/wildskeeper/internal/store"
)

// discordSpy stands in for the incoming webhook, capturing the payloads
// Palcon would have posted.
type discordSpy struct {
	mu     sync.Mutex
	posts  []map[string]any
	status int
}

func newDiscordSpy(t *testing.T) (*discordSpy, string) {
	t.Helper()
	spy := &discordSpy{status: http.StatusNoContent}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spy.mu.Lock()
		defer spy.mu.Unlock()
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		spy.posts = append(spy.posts, body)
		w.WriteHeader(spy.status)
	}))
	t.Cleanup(srv.Close)
	return spy, srv.URL
}

func (d *discordSpy) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.posts)
}

func (d *discordSpy) last() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.posts) == 0 {
		return nil
	}
	return d.posts[len(d.posts)-1]
}

// description digs the embed description out of the last payload.
func (d *discordSpy) description() string {
	last := d.last()
	if last == nil {
		return ""
	}
	embeds, _ := last["embeds"].([]any)
	if len(embeds) == 0 {
		return ""
	}
	e, _ := embeds[0].(map[string]any)
	s, _ := e["description"].(string)
	return s
}

func (d *discordSpy) setStatus(code int) {
	d.mu.Lock()
	d.status = code
	d.mu.Unlock()
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	box, err := crypto.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return store.New(sqlDB, box)
}

// fixture wires a server with a webhook pointed at the spy. Every toggle is
// on unless the caller changes it.
func fixture(t *testing.T, tweak func(*store.DiscordWebhook)) (*notify.Notifier, *store.Server, *discordSpy) {
	t.Helper()
	st := newTestStore(t)
	spy, url := newDiscordSpy(t)
	ctx := context.Background()

	id, err := st.CreateServer(ctx, &store.Server{Name: "Palhalla", Host: "10.0.0.5", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	w := &store.DiscordWebhook{
		ServerID: id, WebhookURL: url,
		Enabled: true, OnStatus: true, OnPlayers: true, OnRestarts: true,
	}
	if tweak != nil {
		tweak(w)
	}
	if err := st.SetDiscordWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	srv, err := st.GetServer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return notify.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))), srv, spy
}

func TestEventsReachDiscord(t *testing.T) {
	n, srv, spy := fixture(t, nil)
	ctx := context.Background()

	n.ServerDown(ctx, srv)
	if spy.count() != 1 || !strings.Contains(spy.description(), "stopped responding") {
		t.Errorf("ServerDown: %d posts, last %q", spy.count(), spy.description())
	}

	n.ServerUp(ctx, srv, 90*time.Minute)
	if !strings.Contains(spy.description(), "1h 30m") {
		t.Errorf("ServerUp should report how long it was down: %q", spy.description())
	}

	n.PlayerJoined(ctx, srv, "Ada", 3)
	if !strings.Contains(spy.description(), "joined") || !strings.Contains(spy.description(), "3 online") {
		t.Errorf("PlayerJoined: %q", spy.description())
	}

	n.PlayerLeft(ctx, srv, "Ada", 2)
	if !strings.Contains(spy.description(), "left") {
		t.Errorf("PlayerLeft: %q", spy.description())
	}

	n.RestartWarning(ctx, srv, 5)
	if !strings.Contains(spy.description(), "5 minutes") {
		t.Errorf("RestartWarning: %q", spy.description())
	}
	n.RestartWarning(ctx, srv, 1)
	if !strings.Contains(spy.description(), "1 minute.") {
		t.Errorf("RestartWarning should say 'minute' for one: %q", spy.description())
	}

	n.RestartingNow(ctx, srv, notify.SaveDone)
	n.WatchdogRestarted(ctx, srv, 137, 2)
	n.WatchdogGaveUp(ctx, srv, 3)
	n.BackupFailed(ctx, srv, errors.New("disk full"))
	if !strings.Contains(spy.description(), "disk full") {
		t.Errorf("BackupFailed should carry the cause: %q", spy.description())
	}
}

// The restart notice must never claim a save that didn't happen. On a game
// that doesn't save on shutdown, whether the world reached disk is the one
// fact a player needs to judge what they just lost.
func TestRestartNoticeSaysWhetherTheWorldWasSaved(t *testing.T) {
	n, srv, spy := fixture(t, nil)
	ctx := context.Background()

	n.RestartingNow(ctx, srv, notify.SaveDone)
	if !strings.Contains(spy.description(), "saved its world") {
		t.Errorf("a save that happened should be stated: %q", spy.description())
	}

	n.RestartingNow(ctx, srv, notify.SaveUnsupported)
	got := spy.description()
	if strings.Contains(got, "saved its world") {
		t.Errorf("claimed a save with no bridge to make one: %q", got)
	}
	if !strings.Contains(got, "lost") {
		t.Errorf("should say what the missing save costs: %q", got)
	}

	n.RestartingNow(ctx, srv, notify.SaveFailed)
	if got := spy.description(); strings.Contains(got, "saved its world") {
		t.Errorf("claimed a save that failed: %q", got)
	}
}

// A player name is arbitrary input and must never be able to ping a role,
// nor smuggle Discord formatting into the channel.
func TestPlayerNamesCannotPingOrFormat(t *testing.T) {
	n, srv, spy := fixture(t, nil)
	n.PlayerJoined(context.Background(), srv, "**@everyone**_x_", 1)

	if got := spy.description(); strings.Contains(got, "**@everyone**") {
		t.Errorf("markdown survived escaping: %q", got)
	}
	mentions, _ := spy.last()["allowed_mentions"].(map[string]any)
	parse, _ := mentions["parse"].([]any)
	if mentions == nil || len(parse) != 0 {
		t.Errorf("allowed_mentions must forbid every mention type, got %v", mentions)
	}
}

func TestPerEventTogglesGateDelivery(t *testing.T) {
	ctx := context.Background()

	t.Run("status off", func(t *testing.T) {
		n, srv, spy := fixture(t, func(w *store.DiscordWebhook) { w.OnStatus = false })
		n.ServerDown(ctx, srv)
		n.PlayerJoined(ctx, srv, "Ada", 1)
		if spy.count() != 1 {
			t.Errorf("status events should be muted and player events not: %d posts", spy.count())
		}
	})

	t.Run("players off", func(t *testing.T) {
		n, srv, spy := fixture(t, func(w *store.DiscordWebhook) { w.OnPlayers = false })
		n.PlayerJoined(ctx, srv, "Ada", 1)
		n.PlayerLeft(ctx, srv, "Ada", 0)
		if spy.count() != 0 {
			t.Errorf("player events should be muted: %d posts", spy.count())
		}
	})

	t.Run("restarts off", func(t *testing.T) {
		n, srv, spy := fixture(t, func(w *store.DiscordWebhook) { w.OnRestarts = false })
		n.RestartWarning(ctx, srv, 5)
		n.RestartingNow(ctx, srv, notify.SaveDone)
		if spy.count() != 0 {
			t.Errorf("restart events should be muted: %d posts", spy.count())
		}
	})

	t.Run("webhook disabled entirely", func(t *testing.T) {
		n, srv, spy := fixture(t, func(w *store.DiscordWebhook) { w.Enabled = false })
		n.ServerDown(ctx, srv)
		n.PlayerJoined(ctx, srv, "Ada", 1)
		n.RestartWarning(ctx, srv, 5)
		if spy.count() != 0 {
			t.Errorf("a disabled webhook should deliver nothing: %d posts", spy.count())
		}
	})
}

// A planned restart shouldn't page anyone about the outage it causes — but
// "back online" still goes through, closing the loop.
func TestSuppressStatusMutesOnlyTheOutage(t *testing.T) {
	n, srv, spy := fixture(t, nil)
	ctx := context.Background()

	n.SuppressStatus(srv.ID, time.Minute)
	n.ServerDown(ctx, srv)
	if spy.count() != 0 {
		t.Fatalf("a suppressed outage still posted: %d", spy.count())
	}

	n.ServerUp(ctx, srv, time.Minute)
	if spy.count() != 1 {
		t.Errorf("'back online' should survive suppression: %d posts", spy.count())
	}
}

func TestSuppressionExpires(t *testing.T) {
	n, srv, spy := fixture(t, nil)
	n.SuppressStatus(srv.ID, time.Nanosecond)
	time.Sleep(2 * time.Millisecond)

	n.ServerDown(context.Background(), srv)
	if spy.count() != 1 {
		t.Errorf("suppression outlived its window: %d posts", spy.count())
	}
}

// Delivery is best-effort: notifications ride on loops with their own jobs,
// so a Discord outage must never surface as a panic or a blocked caller.
func TestDeliveryFailuresAreSwallowed(t *testing.T) {
	n, srv, spy := fixture(t, nil)
	spy.setStatus(http.StatusForbidden)

	n.ServerDown(context.Background(), srv)
	if spy.count() != 1 {
		t.Errorf("the post should still have been attempted: %d", spy.count())
	}
}

func TestNoWebhookConfiguredIsSilent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, err := st.CreateServer(ctx, &store.Server{Name: "bare", Host: "h", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := st.GetServer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	n := notify.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Not an error, and not a panic — just nothing to do.
	n.ServerDown(ctx, srv)
	n.PlayerJoined(ctx, srv, "Ada", 1)

	if err := n.Test(ctx, srv); err == nil {
		t.Error("Test should report the missing webhook to the caller")
	} else if !strings.Contains(err.Error(), "no Discord webhook") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// Test is the one call that reports failure, because it exists to prove a
// pasted webhook actually works.
func TestTestReportsSuccessAndFailure(t *testing.T) {
	n, srv, spy := fixture(t, nil)
	ctx := context.Background()

	if err := n.Test(ctx, srv); err != nil {
		t.Fatalf("Test on a good webhook: %v", err)
	}
	if !strings.Contains(spy.description(), "wired up") {
		t.Errorf("test message: %q", spy.description())
	}

	spy.setStatus(http.StatusNotFound)
	if err := n.Test(ctx, srv); err == nil {
		t.Error("Test should surface a revoked webhook")
	}
}

// Test ignores the per-event toggles: proving the webhook works is not the
// same question as whether routine events are switched on.
func TestTestIgnoresTogglesButNotTheURL(t *testing.T) {
	n, srv, spy := fixture(t, func(w *store.DiscordWebhook) {
		w.Enabled, w.OnStatus, w.OnPlayers, w.OnRestarts = false, false, false, false
	})
	if err := n.Test(context.Background(), srv); err != nil {
		t.Fatalf("Test with every toggle off: %v", err)
	}
	if spy.count() != 1 {
		t.Errorf("Test should send regardless of the toggles: %d posts", spy.count())
	}
}

func TestValidateWebhookURL(t *testing.T) {
	valid := []string{
		"https://discord.com/api/webhooks/123/abc",
		"https://discordapp.com/api/webhooks/123/abc",
		"https://ptb.discord.com/api/webhooks/1/x",
		"https://canary.discord.com/api/webhooks/1/x",
	}
	for _, raw := range valid {
		if err := notify.ValidateWebhookURL(raw); err != nil {
			t.Errorf("%s should be valid: %v", raw, err)
		}
	}

	// Anything else would turn Palcon into a generic HTTP client aimed at
	// someone else's server.
	invalid := map[string]string{
		"http://discord.com/api/webhooks/1/x":           "https",
		"https://evil.com/api/webhooks/1/x":             "discord.com",
		"https://discord.com/not/a/webhook":             "/api/webhooks/",
		"https://discord.com.evil.com/api/webhooks/1/x": "discord.com",
		"://nonsense": "",
	}
	for raw, wantFragment := range invalid {
		err := notify.ValidateWebhookURL(raw)
		if err == nil {
			t.Errorf("%s should be rejected", raw)
			continue
		}
		if wantFragment != "" && !strings.Contains(err.Error(), wantFragment) {
			t.Errorf("%s: error %q should mention %q", raw, err, wantFragment)
		}
	}
}
