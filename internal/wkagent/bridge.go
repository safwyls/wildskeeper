package wkagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The bridge is the agent's half of the dwbridge command channel
// (tools/dwbridge). The game speaks no protocol, so the UE4SS mod and this
// agent talk through files in a shared directory: the agent drops a request
// file, the mod writes a response file and deletes the request. See the mod
// header for the wire format; this is its exact counterpart.
//
// The directory lives under the install root so it rides the same volume the
// mod sees. The agent owns it whether or not it launched the game — the
// bridge's liveness is the mod's heartbeat freshness, not who spawned the
// process — so a manually-run modded game and an agent-supervised one are
// indistinguishable to the control plane.

const (
	// bridgeDirName is the shared directory under the install root. The
	// launch profile points the mod's DWBRIDGE_DIR at the same path.
	bridgeDirName = "dwbridge"
	// heartbeatFresh is how recently the mod must have written its heartbeat
	// for the bridge to count as available. The mod writes every ~2s, so
	// this tolerates a couple of missed beats without flapping.
	heartbeatFresh = 8 * time.Second
	// bridgePoll is how often the agent checks for the response file.
	bridgePoll = 50 * time.Millisecond
	// requestFile and responseFile are the single-flight rendezvous points.
	// Fixed names (not per-id) because the agent serializes commands, which
	// lets the mod avoid directory listing — unreliable under Wine.
	requestFile  = "request.json"
	responseFile = "response.json"
	// bridgeCommandTimeout bounds one command round-trip. Save is the slow
	// case and completes in well under a second on an idle world; a request
	// still unanswered past this means the mod is wedged, not working.
	bridgeCommandTimeout = 15 * time.Second
)

// BridgeStatus is the /v1/health bridge field: whether the mod is live, and
// what it can do. Absent (nil) means no bridge dir exists at all — the
// common case for a server whose game build carries no mod.
type BridgeStatus struct {
	Available bool     `json:"available"`
	Version   string   `json:"version,omitempty"`
	Protocol  int      `json:"protocol,omitempty"`
	Commands  []string `json:"commands,omitempty"`
	// AgeSeconds is how old the last heartbeat is, so an operator can see a
	// bridge that was alive and went quiet rather than one never present.
	AgeSeconds int `json:"ageSeconds,omitempty"`
}

// bridgeHeartbeat mirrors the mod's heartbeat.json.
type bridgeHeartbeat struct {
	TS       int64    `json:"ts"`
	Version  string   `json:"version"`
	Protocol int      `json:"protocol"`
	Commands []string `json:"commands"`
}

// bridgeResponse mirrors the mod's <id>.resp.
type bridgeResponse struct {
	ID    string          `json:"id"`
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

// errBridgeUnavailable means no live mod is behind the bridge. It maps to a
// 503 at the agent edge: the transport is configured but the far end is
// down, which is neither the caller's fault (4xx) nor the agent's (5xx-ish
// gateway) — it's "try again once the mod is up".
var errBridgeUnavailable = errors.New("dwbridge mod is not responding")

type bridge struct {
	dir string
	// mu serializes commands: the protocol is single-flight (one request.json
	// at a time), so overlapping callers would clobber each other's rendezvous
	// files. A management console issues commands one at a time, so the lock
	// is almost never contended.
	mu sync.Mutex
	// seq makes each request id unique within this agent process, so a
	// response left over from a timed-out command is recognised as stale.
	// start reseeds it across a restart without needing a clock per call.
	seq   uint64
	start int64
}

func newBridge(installDir string) *bridge {
	return &bridge{
		dir:   filepath.Join(installDir, bridgeDirName),
		start: time.Now().UnixNano(),
	}
}

// Status reports the bridge state for health. A missing directory or heartbeat
// is a calm "not available", not an error: most servers never carry a mod.
func (b *bridge) Status() *BridgeStatus {
	hb, age, err := b.heartbeat()
	if err != nil {
		if os.IsNotExist(err) {
			// No dir and no heartbeat: report nothing rather than a false
			// "unavailable" that would imply a bridge was expected.
			if _, statErr := os.Stat(b.dir); os.IsNotExist(statErr) {
				return nil
			}
		}
		return &BridgeStatus{Available: false}
	}
	return &BridgeStatus{
		Available:  age <= heartbeatFresh,
		Version:    hb.Version,
		Protocol:   hb.Protocol,
		Commands:   hb.Commands,
		AgeSeconds: int(age.Seconds()),
	}
}

// BridgePlayer is one live player as the mod reports it: the engine's
// replicated roster entry plus pawn world position (UE units).
type BridgePlayer struct {
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Z    float64 `json:"z"`
}

// BridgeWorld is the in-game clock, fields present only once the mod's
// property probing confirms their names against a live server.
type BridgeWorld struct {
	Day       *float64 `json:"day,omitempty"`
	TimeOfDay *float64 `json:"timeOfDay,omitempty"`
}

// BridgeState is the live telemetry the mod publishes alongside its
// heartbeat (state.json). Available=false covers every "no data" cause the
// same way the heartbeat does: no mod, no world, or a stale file.
type BridgeState struct {
	Available  bool           `json:"available"`
	AgeSeconds int            `json:"ageSeconds,omitempty"`
	Players    []BridgePlayer `json:"players,omitempty"`
	World      *BridgeWorld   `json:"world,omitempty"`
}

// State reads the mod's telemetry file. Same freshness rule as commands:
// data older than heartbeatFresh is "unavailable", not "slightly old" —
// serving a roster from a dead mod would show ghosts.
func (b *bridge) State() *BridgeState {
	data, err := os.ReadFile(filepath.Join(b.dir, "state.json"))
	if err != nil {
		return &BridgeState{}
	}
	var raw struct {
		TS      int64          `json:"ts"`
		Players []BridgePlayer `json:"players"`
		World   *BridgeWorld   `json:"world"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return &BridgeState{}
	}
	age := time.Since(time.Unix(raw.TS, 0))
	if age < 0 {
		age = 0
	}
	if age > heartbeatFresh {
		return &BridgeState{AgeSeconds: int(age.Seconds())}
	}
	// An empty world object ({}) carries no information; nil keeps the
	// JSON honest about which fields the mod actually confirmed.
	world := raw.World
	if world != nil && world.Day == nil && world.TimeOfDay == nil {
		world = nil
	}
	return &BridgeState{
		Available:  true,
		AgeSeconds: int(age.Seconds()),
		Players:    raw.Players,
		World:      world,
	}
}

func (b *bridge) heartbeat() (*bridgeHeartbeat, time.Duration, error) {
	data, err := os.ReadFile(filepath.Join(b.dir, "heartbeat.json"))
	if err != nil {
		return nil, 0, err
	}
	var hb bridgeHeartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		return nil, 0, fmt.Errorf("bridge heartbeat unreadable: %w", err)
	}
	age := time.Since(time.Unix(hb.TS, 0))
	if age < 0 {
		age = 0
	}
	return &hb, age, nil
}

// available reports whether a fresh heartbeat naming this command exists.
// Checked before every command so a mod that died between health and command
// fails fast with errBridgeUnavailable instead of hanging to the timeout.
func (b *bridge) available(command string) error {
	hb, age, err := b.heartbeat()
	if err != nil {
		return errBridgeUnavailable
	}
	if age > heartbeatFresh {
		return fmt.Errorf("%w: last heartbeat %ds ago", errBridgeUnavailable, int(age.Seconds()))
	}
	for _, c := range hb.Commands {
		if c == command {
			return nil
		}
	}
	return fmt.Errorf("the dwbridge mod does not implement %q (it offers: %s)", command, strings.Join(hb.Commands, ", "))
}

// Command runs one command through the mod and returns its data payload. It
// is single-flight (see mu): it clears any stale rendezvous files, writes
// request.json, and waits for a response.json carrying the same id. A
// response with a different id is a leftover from an earlier timed-out
// command and is ignored. On timeout it clears the request so the mod
// doesn't run it late.
func (b *bridge) Command(ctx context.Context, command string, args map[string]string) (json.RawMessage, error) {
	if err := b.available(command); err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	reqPath := filepath.Join(b.dir, requestFile)
	respPath := filepath.Join(b.dir, responseFile)
	// Clear anything a previous command left behind, so a stale response
	// can't be mistaken for this one before the mod overwrites it.
	os.Remove(reqPath)
	os.Remove(respPath)

	b.seq++
	id := fmt.Sprintf("%d-%d", b.start, b.seq)
	req := struct {
		ID      string            `json:"id"`
		Command string            `json:"command"`
		Args    map[string]string `json:"args"`
	}{ID: id, Command: command, Args: args}
	if args == nil {
		req.Args = map[string]string{}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	// Write atomically so the mod's poll never reads a half-written request.
	if err := writeFileAtomic(reqPath, body); err != nil {
		return nil, fmt.Errorf("writing bridge request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, bridgeCommandTimeout)
	defer cancel()
	ticker := time.NewTicker(bridgePoll)
	defer ticker.Stop()

	for {
		if data, err := os.ReadFile(respPath); err == nil {
			var resp bridgeResponse
			if err := json.Unmarshal(data, &resp); err == nil && resp.ID == id {
				os.Remove(respPath)
				if !resp.OK {
					return nil, fmt.Errorf("dwbridge %s failed: %s", command, resp.Error)
				}
				return resp.Data, nil
			}
			// Unparseable (a torn read mid-write) or a stale id: leave it and
			// poll again; the mod will overwrite it with ours.
		}
		select {
		case <-ctx.Done():
			os.Remove(reqPath)
			return nil, fmt.Errorf("%w: %s timed out", errBridgeUnavailable, command)
		case <-ticker.C:
		}
	}
}

// writeFileAtomic writes via a temp sibling and renames, matching the mod's
// own discipline so neither side reads a torn file.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
