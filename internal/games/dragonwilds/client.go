package dragonwilds

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/safwyls/wildskeeper/internal/agentctl"
	"github.com/safwyls/wildskeeper/internal/game"
	"github.com/safwyls/wildskeeper/internal/games/dragonwilds/dwlog"
)

// errNoAgent is the config-level failure: without a sidecar there is no
// transport to derive anything from. It maps to 502 like any other
// unreachable-admin-interface error, which is accurate — the fix is
// configuration, not a missing game capability.
var errNoAgent = errors.New("dragonwilds servers are managed through a wkagent sidecar; set the server's agent URL and token")

// trackers is the per-server session state, keyed by agent URL. Clients are
// rebuilt from the row on every API call (store.Server.Client), so the
// state a log-derived player list needs has to outlive them; keying on the
// agent URL keeps one tracker per server without the client knowing row ids.
var (
	trackersMu sync.Mutex
	trackers   = map[string]*dwlog.Tracker{}
)

func trackerFor(agentURL string) *dwlog.Tracker {
	trackersMu.Lock()
	defer trackersMu.Unlock()
	t, ok := trackers[agentURL]
	if !ok {
		t = dwlog.NewTracker(dwlog.RulesV1)
		trackers[agentURL] = t
	}
	return t
}

// logTail is how much of the agent's ring each refresh asks for — the whole
// of it, because the tracker anchors incrementally and the ring is capped
// at the same figure agent-side.
const logTail = 2000

// Client derives Dragonwilds state through the wkagent sidecar. It
// implements game.Client with the honest subset: Info and Players work,
// commands return game.UnsupportedError until the dwbridge mod exists.
type Client struct {
	agent    *agentctl.Client
	agentErr error
	tracker  *dwlog.Tracker
}

// New builds the client for one server. A missing or malformed agent URL is
// carried as a deferred error rather than returned, because game.Definition's
// NewClient contract has no error path — the first call reports it instead.
func New(conn game.Conn) game.Client {
	if conn.AgentURL == "" {
		return &Client{agentErr: errNoAgent}
	}
	a, err := agentctl.New(conn.AgentURL, conn.AgentToken)
	if err != nil {
		return &Client{agentErr: fmt.Errorf("agent: %w", err)}
	}
	return &Client{agent: a, tracker: trackerFor(conn.AgentURL)}
}

// refresh polls the agent once and feeds the tracker: health for process
// state (and the restart-reset key), the log ring for events. It returns
// the game status so callers don't re-fetch.
func (c *Client) refresh(ctx context.Context) (*agentctl.GameStatus, error) {
	if c.agentErr != nil {
		return nil, c.agentErr
	}
	h, err := c.agent.Health(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent health: %w", err)
	}
	if h.Game == nil {
		return nil, errors.New("agent is not supervising a game process (companion mode); dragonwilds needs supervisor mode")
	}
	if h.Game.State != "running" {
		return h.Game, nil
	}
	lines, err := c.agent.GameLogs(ctx, logTail)
	if err != nil {
		return nil, fmt.Errorf("agent logs: %w", err)
	}
	c.tracker.Update(h.Game.StartedAt, lines)
	return h.Game, nil
}

// Info reports liveness and the derived player count. ServerName stays
// empty: the log stream doesn't carry it (v0) and inventing it from the row
// would just echo the user's own input back at them.
func (c *Client) Info(ctx context.Context) (*game.ServerInfo, error) {
	st, err := c.refresh(ctx)
	if err != nil {
		return nil, err
	}
	if st.State != "running" {
		// An error, not a zero Info: the dashboard's "unreachable" state is
		// the truthful rendering of a stopped or crashed process, and the
		// power panel (agent-backed) stays available alongside it.
		return nil, fmt.Errorf("server process is %s", st.State)
	}
	count := len(c.tracker.Sessions())
	// The engine's replicated roster outranks log inference when the mod is
	// publishing it: the Windows build doesn't emit the join line the log
	// rules were built on, so on a modded server the tracker undercounts —
	// and the bridge sees exactly who the server itself thinks is present.
	if st, err := c.agent.BridgeState(ctx); err == nil && st.Available {
		count = len(st.Players)
	}
	return &game.ServerInfo{
		PlayerCount: count,
		Transport:   "agent",
	}, nil
}

// Players is the log-derived session list. With the v1 rules the log lines
// carry the real player id, which becomes PlayerUID/UserID in canonical
// spelling so it matches ids from any other source (ini KnownPlayerList,
// save data). A session without an id — v0 rules, or a line that only
// named the player — falls back to the name, which is stable and unique in
// practice on a six-slot friends server.
func (c *Client) Players(ctx context.Context) ([]game.Player, error) {
	st, err := c.refresh(ctx)
	if err != nil {
		return nil, err
	}
	if st.State != "running" {
		return nil, fmt.Errorf("server process is %s", st.State)
	}
	sessions := c.tracker.Sessions()
	players := make([]game.Player, 0, len(sessions))
	for _, s := range sessions {
		uid := s.Name
		if s.ID != "" {
			uid = CanonicalUID(s.ID)
		}
		players = append(players, game.Player{
			Name:      s.Name,
			PlayerUID: uid,
			UserID:    uid,
		})
	}
	c.enrichFromBridge(ctx, &players)
	return players, nil
}

// enrichFromBridge overlays the dwbridge mod's live telemetry onto the
// log-derived roster: positions for players both sources know, and rows for
// players only the engine's replicated roster carries (a session the log
// parser missed survives here with the name as its id, same as a v0 line).
// Best-effort by design — the log roster stood alone before the bridge
// existed and must keep working when the mod is absent, stale, or the extra
// round-trip fails; ids stay log-derived because the bridge doesn't carry
// them, and identity (kick/ban someday) matters more than coordinates.
func (c *Client) enrichFromBridge(ctx context.Context, players *[]game.Player) {
	st, err := c.agent.BridgeState(ctx)
	if err != nil || !st.Available {
		return
	}
	byName := make(map[string]int, len(*players))
	for i := range *players {
		byName[(*players)[i].Name] = i
	}
	for _, bp := range st.Players {
		if i, ok := byName[bp.Name]; ok {
			(*players)[i].LocationX = bp.X
			(*players)[i].LocationY = bp.Y
		} else {
			*players = append(*players, game.Player{
				Name: bp.Name, PlayerUID: bp.Name, UserID: bp.Name,
				LocationX: bp.X, LocationY: bp.Y,
			})
		}
	}
}

// The command tier. Dragonwilds has no native command protocol, so these
// route through the dwbridge UE4SS mod when it is present and running
// (docs/dragonwilds-recon.md, "Command surface"). Without a live mod each
// returns a *game.UnsupportedError, which the API maps to 501 — capability
// truth ("this server has no bridge"), not a fault. A bridge that is present
// but unreachable, or a command the mod refuses, returns a plain error (502)
// instead: that is a fault, and the difference is what lets the UI say the
// right thing.

// bridgeReady reports whether op can travel over the bridge right now. It
// returns nil when the command would be carried, an *game.UnsupportedError
// when nothing on this server could carry it (no agent, no mod, or a mod
// without that verb), and a plain error when the agent itself can't be
// reached. It is the single decision behind both running a command and
// probing for one, so Supports can never promise what bridgeCommand would
// refuse.
func (c *Client) bridgeReady(ctx context.Context, op string) error {
	if c.agentErr != nil {
		// No agent is configured at all, so no bridge can exist: that's a
		// stable capability answer (501), not a transient fault. It differs
		// from Info/Players, which return the raw 502 because they need the
		// agent to derive live data — a command needs it to find a bridge,
		// and a server with no agent simply has none.
		return &game.UnsupportedError{Op: op, Reason: commandReason(op)}
	}
	h, err := c.agent.Health(ctx)
	if err != nil {
		return fmt.Errorf("agent health: %w", err)
	}
	if h.Bridge == nil || !h.Bridge.Available {
		// No command channel exists (no mod), or it exists but is down. The
		// former is a capability gap; report it as one with the honest
		// reason. (A down-but-present bridge is rare and also best surfaced
		// as "can't right now" rather than a scary gateway error.)
		return &game.UnsupportedError{Op: op, Reason: commandReason(op)}
	}
	if !containsString(h.Bridge.Commands, op) {
		return &game.UnsupportedError{Op: op, Reason: "the dwbridge mod on this server does not implement " + op + " yet"}
	}
	return nil
}

// bridgeCommand runs op through the mod, translating the two failure shapes.
// A missing or mod-less bridge, or a command the running mod does not
// implement, is an UnsupportedError; anything else (bridge down mid-call,
// handler error) is returned as-is.
func (c *Client) bridgeCommand(ctx context.Context, op string, args map[string]string) error {
	if err := c.bridgeReady(ctx, op); err != nil {
		return err
	}
	if _, err := c.agent.BridgeCommand(ctx, op, args); err != nil {
		return fmt.Errorf("dwbridge %s: %w", op, err)
	}
	return nil
}

// Supports answers the capability question without firing the command, so
// the console can say what a server will do before anyone asks it to. An
// unreachable agent is reported as unsupported with the reason attached:
// from the caller's side "can't right now" and "can't ever" both mean the
// control shouldn't promise anything, and the reason distinguishes them.
func (c *Client) Supports(ctx context.Context, op string) (bool, string) {
	if op == "shutdown" {
		// Not a bridge command and never will be — see Shutdown below.
		return false, reasonNoShutdown
	}
	err := c.bridgeReady(ctx, op)
	if err == nil {
		return true, ""
	}
	var unsupported *game.UnsupportedError
	if errors.As(err, &unsupported) {
		return false, unsupported.Reason
	}
	return false, err.Error()
}

const (
	reasonNoBridge   = "commands need the dwbridge UE4SS mod; this server has none running (see docs/dragonwilds-recon.md)"
	reasonModerate   = "kicking and banning need the dwbridge UE4SS mod running on the server"
	reasonNoSave     = "an on-demand save needs the dwbridge mod; without it, autosave covers running servers and backups snapshot the save directory"
	reasonNoShutdown = "no in-game shutdown exists; stop the server through the agent power controls, which allow a grace period"
)

// commandReason is what to tell someone when op has no bridge to travel
// over. It lives in one place so a probe and the command itself always give
// the same explanation.
func commandReason(op string) string {
	switch op {
	case "save":
		return reasonNoSave
	case "kick", "ban", "unban":
		return reasonModerate
	default:
		return reasonNoBridge
	}
}

func (c *Client) Broadcast(ctx context.Context, message string) error {
	return c.bridgeCommand(ctx, "broadcast", map[string]string{"message": message})
}

func (c *Client) Kick(ctx context.Context, playerUID, message string) error {
	return c.bridgeCommand(ctx, "kick", map[string]string{"playerId": playerUID, "message": message})
}

func (c *Client) Ban(ctx context.Context, playerUID, message string) error {
	return c.bridgeCommand(ctx, "ban", map[string]string{"playerId": playerUID, "message": message})
}

func (c *Client) Unban(ctx context.Context, playerUID string) error {
	return c.bridgeCommand(ctx, "unban", map[string]string{"playerId": playerUID})
}

// Save asks the mod to write the world now. This is the one command verified
// to work headless: the mod calls PersistenceSubsystem:SaveGame, the same
// path the game's own autosave takes, with no player connected.
func (c *Client) Save(ctx context.Context) error {
	return c.bridgeCommand(ctx, "save", nil)
}

// Shutdown is not a bridge command: stopping the process is the agent's job,
// and it already does it with a grace period through the power controls. A
// mod-driven in-game shutdown would be strictly worse (it can't stop a hung
// game), so this stays pointed at the real mechanism.
func (c *Client) Shutdown(ctx context.Context, waitSeconds int, message string) error {
	return &game.UnsupportedError{Op: "shutdown", Reason: reasonNoShutdown}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Metrics lets the collector chart what the derived view does know: player
// count against the hard six-slot cap, and process uptime. FPS and frame
// time stay zero — nothing reports them — which charts read correctly as
// "not reported".
func (c *Client) Metrics(ctx context.Context) (*game.Metrics, error) {
	st, err := c.refresh(ctx)
	if err != nil {
		return nil, err
	}
	if st.State != "running" {
		return nil, fmt.Errorf("server process is %s", st.State)
	}
	return &game.Metrics{
		CurrentPlayerNum: len(c.tracker.Sessions()),
		MaxPlayerNum:     MaxPlayers,
		UptimeSeconds:    int(time.Since(st.StartedAt).Seconds()),
	}, nil
}

// Settings has no live transport — the game can't be asked for its config,
// only the ini read at rest, which is the config editor's job (dwconfig),
// not this client's.
func (c *Client) Settings(ctx context.Context) (map[string]any, error) {
	return nil, &game.UnsupportedError{Op: "settings", Reason: "the game has no live settings query; edit DedicatedServer.ini from the Configuration view"}
}
