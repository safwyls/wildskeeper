package dragonwilds_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/safwyls/wildskeeper/internal/game"
	"github.com/safwyls/wildskeeper/internal/games/dragonwilds"
)

// fakeAgent is a wkagent just complete enough for the derived client:
// /v1/health with a supervised-game status, /v1/power/logs with a ring.
type fakeAgent struct {
	mu        sync.Mutex
	state     string
	startedAt time.Time
	lines     []string
	// bridge, when set, is reported in health as the bridge object; nil
	// means no bridge (the no-mod case). bridgeStatus/bridgeBody let a test
	// script the command endpoint's reply.
	bridge       map[string]any
	bridgeStatus int
	bridgeBody   map[string]any
	lastCommand  map[string]any
	// bridgeState, when set, is served at /v1/bridge/state; nil answers
	// 404 like an agent from before the telemetry channel existed.
	bridgeState map[string]any
}

func newFakeAgent(t *testing.T) (*fakeAgent, string) {
	t.Helper()
	f := &fakeAgent{state: "running", startedAt: time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			h := map[string]any{
				"agent": "wkagent", "apiVersion": 1, "mode": "supervisor",
				"game": map[string]any{"state": f.state, "startedAt": f.startedAt},
				"job":  nil,
			}
			if f.bridge != nil {
				h["bridge"] = f.bridge
			}
			json.NewEncoder(w).Encode(h)
		case "/v1/power/logs":
			json.NewEncoder(w).Encode(map[string]any{"lines": f.lines})
		case "/v1/bridge/command":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			f.lastCommand = in
			status := f.bridgeStatus
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
			if f.bridgeBody != nil {
				json.NewEncoder(w).Encode(f.bridgeBody)
			} else {
				json.NewEncoder(w).Encode(map[string]any{"ok": true})
			}
		case "/v1/bridge/state":
			if f.bridgeState == nil {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(f.bridgeState)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeAgent) set(state string, lines ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = state
	f.lines = lines
}

func newClient(t *testing.T, url string) game.Client {
	t.Helper()
	return dragonwilds.New(game.Conn{AgentURL: url, AgentToken: "tok"})
}

func TestInfoAndPlayersDeriveFromAgent(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("running",
		"[x][1]LogDomMatcherSession: Player ADDED to session [aaaa000000000000000000000000aaaa]-[Vexmarrow]",
		"[x][2]LogDomMatcherSession: Player ADDED to session [bbbb000000000000000000000000bbbb]-[Kaelith]",
		"[x][3]LogDomMatcherSession: Player Removed from session [bbbb000000000000000000000000bbbb]-[Kaelith]",
	)
	c := newClient(t, url)
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.PlayerCount != 1 || info.Transport != "agent" {
		t.Fatalf("info = %+v", info)
	}
	players, err := c.Players(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(players) != 1 || players[0].Name != "Vexmarrow" || players[0].UserID != "aaaa000000000000000000000000aaaa" {
		t.Fatalf("players = %+v", players)
	}
}

// The bridge telemetry overlays the log roster: positions for players both
// sources know, a full row for players only the engine reports — and none
// of it may break the log-only path (agents predating the endpoint 404 it).
func TestPlayersMergeBridgeTelemetry(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("running",
		"[x][1]LogDomMatcherSession: Player ADDED to session [aaaa000000000000000000000000aaaa]-[Vexmarrow]",
	)
	agent.mu.Lock()
	agent.bridgeState = map[string]any{
		"available": true,
		"players": []map[string]any{
			{"name": "Vexmarrow", "x": 12345.6, "y": -789.0, "z": 42.0},
			{"name": "Kaelith", "x": 100.0, "y": 200.0, "z": 0.0},
		},
	}
	agent.mu.Unlock()

	players, err := newClient(t, url).Players(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(players) != 2 {
		t.Fatalf("players = %+v, want the log player plus the bridge-only one", players)
	}
	if players[0].Name != "Vexmarrow" || players[0].LocationX != 12345.6 || players[0].LocationY != -789.0 {
		t.Errorf("log-derived player did not gain its live position: %+v", players[0])
	}
	if players[0].UserID != "aaaa000000000000000000000000aaaa" {
		t.Errorf("the log-derived id must survive the merge: %+v", players[0])
	}
	if players[1].Name != "Kaelith" || players[1].LocationX != 100.0 {
		t.Errorf("bridge-only player missing or unpositioned: %+v", players[1])
	}

	// The count follows the bridge too: the Windows build lacks the join
	// log line, so live telemetry is the only honest census on a modded
	// server (2 here, where the log knows only 1).
	info, err := newClient(t, url).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.PlayerCount != 2 {
		t.Errorf("PlayerCount = %d, want the bridge's 2", info.PlayerCount)
	}

	// A stale bridge must change nothing: the roster is the log's alone.
	agent.mu.Lock()
	agent.bridgeState = map[string]any{"available": false}
	agent.mu.Unlock()
	players, err = newClient(t, url).Players(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(players) != 1 || players[0].LocationX != 0 {
		t.Errorf("unavailable bridge state leaked into the roster: %+v", players)
	}
}

func TestStoppedProcessReadsAsUnreachableNotUnsupported(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("stopped")
	c := newClient(t, url)
	_, err := c.Info(context.Background())
	if err == nil {
		t.Fatal("expected error for stopped process")
	}
	var unsupported *game.UnsupportedError
	if errors.As(err, &unsupported) {
		t.Fatal("a stopped process is a 502-shaped condition, not a capability gap")
	}
}

func TestTrackerSurvivesClientRebuilds(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("running", "[x][1]LogDomMatcherSession: Player ADDED to session [aaaa000000000000000000000000aaaa]-[Vexmarrow]")
	if _, err := newClient(t, url).Players(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A fresh client (as every API call builds) must still know Vexmarrow
	// even if his join line has scrolled out of the ring.
	agent.set("running", "[x][9]LogWorld: noise")
	players, err := newClient(t, url).Players(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(players) != 1 || players[0].Name != "Vexmarrow" {
		t.Fatalf("players = %+v", players)
	}
}

func TestRestartResetsSessions(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("running", "[x][1]LogDomMatcherSession: Player ADDED to session [aaaa000000000000000000000000aaaa]-[Vexmarrow]")
	c := newClient(t, url)
	if _, err := c.Players(context.Background()); err != nil {
		t.Fatal(err)
	}
	agent.mu.Lock()
	agent.startedAt = agent.startedAt.Add(time.Hour)
	agent.lines = nil
	agent.mu.Unlock()
	players, err := c.Players(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(players) != 0 {
		t.Fatalf("players after restart = %+v", players)
	}
}

func TestCommandsReturnTypedUnsupported(t *testing.T) {
	_, url := newFakeAgent(t)
	c := newClient(t, url)
	ctx := context.Background()
	calls := map[string]func() error{
		"broadcast": func() error { return c.Broadcast(ctx, "hi") },
		"kick":      func() error { return c.Kick(ctx, "uid", "msg") },
		"ban":       func() error { return c.Ban(ctx, "uid", "msg") },
		"unban":     func() error { return c.Unban(ctx, "uid") },
		"save":      func() error { return c.Save(ctx) },
		"shutdown":  func() error { return c.Shutdown(ctx, 30, "msg") },
	}
	for op, call := range calls {
		var unsupported *game.UnsupportedError
		if err := call(); !errors.As(err, &unsupported) {
			t.Errorf("%s: err = %v, want UnsupportedError", op, err)
		} else if unsupported.Op != op {
			t.Errorf("%s: Op = %q", op, unsupported.Op)
		}
	}
}

func TestMetricsReportHonestSubset(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("running", "[x][1]LogDomMatcherSession: Player ADDED to session [aaaa000000000000000000000000aaaa]-[Vexmarrow]")
	ext, ok := newClient(t, url).(game.ExtendedClient)
	if !ok {
		t.Fatal("client should implement ExtendedClient for metrics")
	}
	m, err := ext.Metrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.CurrentPlayerNum != 1 || m.MaxPlayerNum != dragonwilds.MaxPlayers {
		t.Fatalf("metrics = %+v", m)
	}
	if m.UptimeSeconds <= 0 {
		t.Fatalf("uptime = %d, want > 0", m.UptimeSeconds)
	}
	if m.ServerFPS != 0 || m.ServerFrameTime != 0 {
		t.Fatalf("fps/frametime should stay zero (not reported): %+v", m)
	}
}

func TestNoAgentConfiguredIsDeferredError(t *testing.T) {
	c := dragonwilds.New(game.Conn{})
	if _, err := c.Info(context.Background()); err == nil {
		t.Fatal("expected configuration error")
	}
}

// The ids below are synthetic but real-shaped (32 hex, EOS ProductUserId
// form). A live account's id was used to establish the shape; committing
// someone's actual account identifier into a repo is a different thing
// entirely, so the fixtures are made up.
func TestCanonicalUID(t *testing.T) {
	const lower = "0a1b2c3d4e5f60718293a4b5c6d7e8f9"
	upper := strings.ToUpper(lower)

	cases := []struct{ name, in, want string }{
		// The case-folding case is the whole point: the Settings screen
		// shows a Player ID lowercase, while the values the server writes
		// itself (ServerGuid, WorldSaveGuid) are uppercase. Both spellings
		// have to land on the same key or a match silently never happens.
		{"lowercase id is left alone", lower, lower},
		{"uppercase id folds to lowercase", upper, lower},
		{"mixed case folds too", "0A1b2C3d4E5f60718293a4b5c6d7e8f9", lower},
		{"surrounding space is trimmed", "  " + upper + "  ", lower},
		// Anything not 32-hex is trimmed and otherwise untouched: folding
		// an unknown format could collide two distinct ids.
		{"non-hex is preserved", "Player-Name_42", "Player-Name_42"},
		{"wrong length is preserved", "0A1B2C3D", "0A1B2C3D"},
		{"empty stays empty", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dragonwilds.CanonicalUID(tc.in); got != tc.want {
				t.Errorf("CanonicalUID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Canonicalizing twice must not change the answer — the collector applies
// it on every poll, so a non-idempotent transform would drift.
func TestCanonicalUIDIsIdempotent(t *testing.T) {
	for _, in := range []string{"0A1B2C3D4E5F60718293A4B5C6D7E8F9", "  Name  ", ""} {
		once := dragonwilds.CanonicalUID(in)
		if twice := dragonwilds.CanonicalUID(once); twice != once {
			t.Errorf("CanonicalUID(%q): %q then %q", in, once, twice)
		}
	}
}

// setBridge configures the health bridge object the fake agent reports.
func (f *fakeAgent) setBridge(available bool, commands ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bridge = map[string]any{"available": available, "version": "dwbridge/test", "commands": toAnySlice(commands)}
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// TestCommandsWithoutBridgeAreUnsupported: with no bridge in health, every
// command is a capability gap (UnsupportedError → 501), not a fault.
func TestCommandsWithoutBridgeAreUnsupported(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("running")
	c := newClient(t, url)

	calls := map[string]func() error{
		"broadcast": func() error { return c.Broadcast(context.Background(), "hi") },
		"kick":      func() error { return c.Kick(context.Background(), "id", "") },
		"ban":       func() error { return c.Ban(context.Background(), "id", "") },
		"unban":     func() error { return c.Unban(context.Background(), "id") },
		"save":      func() error { return c.Save(context.Background()) },
	}
	for name, call := range calls {
		err := call()
		var unsupported *game.UnsupportedError
		if !errors.As(err, &unsupported) {
			t.Errorf("%s without bridge: err = %v, want UnsupportedError", name, err)
		}
	}
}

// TestSaveRoutesThroughBridge: with a live bridge offering "save", the
// command reaches the agent's bridge endpoint and succeeds.
func TestSaveRoutesThroughBridge(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("running")
	agent.setBridge(true, "ping", "save")
	c := newClient(t, url)

	if err := c.Save(context.Background()); err != nil {
		t.Fatalf("Save with bridge: %v", err)
	}
	if agent.lastCommand["command"] != "save" {
		t.Errorf("agent saw command %v, want save", agent.lastCommand["command"])
	}
}

// TestUnimplementedBridgeCommandIsUnsupported: a live bridge that offers
// only "save" must report kick as an honest capability gap, distinct from
// "no bridge" but still an UnsupportedError.
func TestUnimplementedBridgeCommandIsUnsupported(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("running")
	agent.setBridge(true, "save")
	c := newClient(t, url)

	err := c.Kick(context.Background(), "id", "")
	var unsupported *game.UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("kick against a save-only bridge: err = %v, want UnsupportedError", err)
	}
	if !strings.Contains(err.Error(), "kick") {
		t.Errorf("error should name the missing command: %v", err)
	}
}

// TestBridgeCommandFailureIsFault: a live bridge that returns an error (503
// from the agent) is a fault, not a capability gap — a plain error, not
// UnsupportedError, so the UI says "unreachable" rather than "can't".
func TestBridgeCommandFailureIsFault(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("running")
	agent.setBridge(true, "save")
	agent.mu.Lock()
	agent.bridgeStatus = http.StatusServiceUnavailable
	agent.bridgeBody = map[string]any{"error": "dwbridge mod is not responding"}
	agent.mu.Unlock()
	c := newClient(t, url)

	err := c.Save(context.Background())
	var unsupported *game.UnsupportedError
	if err == nil || errors.As(err, &unsupported) {
		t.Fatalf("Save against a down bridge: err = %v, want a plain fault error", err)
	}
}

// Shutdown stays unsupported by design: stopping the process is the agent's
// job, not the mod's.
func TestShutdownStaysUnsupported(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("running")
	agent.setBridge(true, "save", "shutdown") // even if a mod offered it
	c := newClient(t, url)

	err := c.Shutdown(context.Background(), 30, "")
	var unsupported *game.UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Errorf("Shutdown: err = %v, want UnsupportedError (agent power controls own this)", err)
	}
}

// prober asserts the client through the published optional interface, so
// these tests also prove it actually satisfies game.CommandProber — the
// thing the API handler type-asserts for.
func prober(t *testing.T, c game.Client) game.CommandProber {
	t.Helper()
	p, ok := c.(game.CommandProber)
	if !ok {
		t.Fatal("the dragonwilds client no longer implements game.CommandProber")
	}
	return p
}

// The probe's whole value is that it agrees with reality, so this asserts
// the agreement directly rather than the two answers separately: for every
// bridge shape, what Supports promises is what Save actually does. A probe
// that drifts from the command is worse than no probe — it would put a
// working-looking button in front of a server that can't serve it, or hide
// one that could.
func TestSupportsAgreesWithTheCommandItPredicts(t *testing.T) {
	cases := []struct {
		name      string
		bridge    []string // nil = no bridge at all
		available bool
		want      bool
	}{
		{name: "no bridge", bridge: nil, want: false},
		{name: "bridge down", bridge: []string{"ping", "save"}, available: false, want: false},
		{name: "bridge without save", bridge: []string{"ping"}, available: true, want: false},
		{name: "bridge with save", bridge: []string{"ping", "save"}, available: true, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent, url := newFakeAgent(t)
			agent.set("running")
			if tc.bridge != nil {
				agent.setBridge(tc.available, tc.bridge...)
			}
			c := newClient(t, url)

			got, reason := prober(t, c).Supports(context.Background(), "save")
			if got != tc.want {
				t.Errorf("Supports(save) = %v, want %v (reason %q)", got, tc.want, reason)
			}
			// The command's own verdict, by the only distinction that
			// matters to a caller: did it refuse as a capability gap?
			err := c.Save(context.Background())
			var unsupported *game.UnsupportedError
			servable := !errors.As(err, &unsupported)
			if servable != tc.want {
				t.Errorf("Save was servable = %v but Supports said %v", servable, got)
			}
			if !tc.want && reason == "" {
				t.Error("an unsupported command must explain itself")
			}
		})
	}
}

// A probe must never perform what it is asking about — the one way this
// could be actively harmful.
func TestSupportsDoesNotRunTheCommand(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("running")
	agent.setBridge(true, "ping", "save")
	c := newClient(t, url)

	if ok, _ := prober(t, c).Supports(context.Background(), "save"); !ok {
		t.Fatal("expected save to be supported")
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.lastCommand != nil {
		t.Errorf("probing sent a command to the bridge: %v", agent.lastCommand)
	}
}

// Shutdown is not the bridge's to offer, however the mod is configured.
func TestSupportsRefusesShutdownEvenWithAFullBridge(t *testing.T) {
	agent, url := newFakeAgent(t)
	agent.set("running")
	agent.setBridge(true, "ping", "save", "shutdown")
	c := newClient(t, url)

	if ok, reason := prober(t, c).Supports(context.Background(), "shutdown"); ok || reason == "" {
		t.Errorf("Supports(shutdown) = %v, %q; want a refusal with a reason", ok, reason)
	}
}
