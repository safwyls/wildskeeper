// Package agentctl is wildskeeper's client for a server's wkagent sidecar
// (docs/sidecar-agent.md) — the same role dockerctl plays for the docker
// socket proxy: a small, scoped client with errors worth showing a user.
package agentctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/safwyls/wildskeeper/internal/wkagent"
)

// ErrNotConfigured means this server has no agent URL; agent-backed
// features are off rather than broken.
var ErrNotConfigured = errors.New("no agent is configured for this server")

// ErrRejected wraps agent 4xx responses: the agent is reachable and
// working, but refused the request (bad token, mis-set install dir, ...).
// Callers surface these as the user's configuration problem, not a gateway
// failure.
var ErrRejected = errors.New("agent rejected the request")

// ErrBusy is the agent's 409: a job is already running.
var ErrBusy = errors.New("the agent is already running a job")

// ErrNotFound is the agent's 404 — the thing asked about isn't there.
//
// Split out of ErrRejected because "it's already gone" and "I refuse" want
// opposite handling: a caller destroying a container can treat the former
// as success and carry on, where folding both into one sentinel leaves it
// unable to tell a finished job from a forbidden one.
var ErrNotFound = errors.New("the agent has no such thing")

// Job and Health mirror the agent's wire types; the agent package owns
// them so the two binaries can't drift.
type (
	Job    = wkagent.Job
	Health = wkagent.Health
)

type Client struct {
	base  string
	token string
	http  *http.Client
}

// New builds a client for an agent URL like http://wkagent-main:8811.
// An empty URL returns ErrNotConfigured so callers can treat "feature off"
// distinctly.
func New(rawURL, token string) (*Client, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, ErrNotConfigured
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return nil, fmt.Errorf("unsupported agent url %q: use http:// or https://", rawURL)
	}
	return &Client{
		base:  strings.TrimSuffix(rawURL, "/"),
		token: token,
		http:  &http.Client{},
	}, nil
}

// BaseURL exposes the configured endpoint (for host inference).
func (c *Client) BaseURL() string { return c.base }

// do performs one JSON round-trip, decoding a success body into out (when
// non-nil) and mapping error responses onto the sentinel errors above.
func (c *Client) do(ctx context.Context, method, path string, body, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var buf *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		buf = bytes.NewReader(b)
	} else {
		buf = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, buf)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("agent unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		msg := ""
		var parsed struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(resp.Body).Decode(&parsed) == nil {
			msg = parsed.Error
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Errorf("%w: the agent rejected the token — re-check it on both sides", ErrRejected)
		case http.StatusConflict:
			return fmt.Errorf("%w: %s", ErrBusy, msg)
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", ErrNotFound, msg)
		case http.StatusBadRequest:
			return fmt.Errorf("%w: %s", ErrRejected, msg)
		}
		return fmt.Errorf("agent returned %d: %s", resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Health(ctx context.Context) (*Health, error) {
	var h Health
	if err := c.do(ctx, http.MethodGet, "/v1/health", nil, &h, 10*time.Second); err != nil {
		return nil, err
	}
	return &h, nil
}

// GameSelfExitWindow is how long a supervised game that accepted an in-game
// shutdown is given to exit on its own before the agent signals it. Shared
// rather than redeclared per caller so the manual and scheduled power flows
// can't drift apart.
const GameSelfExitWindow = 20 * time.Second

// Supervisor returns the agent and its health iff it reports supervisor
// mode — the signal that the agent, not docker, owns the game process.
// Every failure (none configured, unreachable, companion mode) collapses to
// nil, so callers fall back to docker uniformly instead of each inventing
// their own idea of "is there an agent here".
//
// One implementation on purpose. Wildskeeper asks this question in at least
// three places — the power handlers, the scheduler, and the SteamCMD gate —
// and a server that answers differently depending on which one asked is
// precisely the class of bug this exists to prevent.
func Supervisor(ctx context.Context, agentURL, token string) (*Client, *Health) {
	client, err := New(agentURL, token)
	if err != nil {
		return nil, nil
	}
	h, err := client.Health(ctx)
	if err != nil || h.Mode != "supervisor" || h.Game == nil {
		return nil, nil
	}
	return client, h
}

// ClearSteamCache empties the SteamCMD cache dirs next to the game server
// and reports how many entries went. The generous timeout covers a cache
// full of partially-downloaded depot chunks.
func (c *Client) ClearSteamCache(ctx context.Context) (int, error) {
	var res struct {
		Removed int `json:"removed"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/steam/clear-cache", nil, &res, 60*time.Second); err != nil {
		return 0, err
	}
	return res.Removed, nil
}

// StartUpdate kicks off a SteamCMD app_update job and returns immediately;
// poll progress via Health (or Job). ErrBusy when one is already running.
func (c *Client) StartUpdate(ctx context.Context, validate bool) (*Job, error) {
	var res struct {
		Job *Job `json:"job"`
	}
	body := map[string]bool{"validate": validate}
	if err := c.do(ctx, http.MethodPost, "/v1/steam/update", body, &res, 15*time.Second); err != nil {
		return nil, err
	}
	return res.Job, nil
}

func (c *Client) Job(ctx context.Context, id string) (*Job, error) {
	var res struct {
		Job *Job `json:"job"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/jobs/"+id, nil, &res, 10*time.Second); err != nil {
		return nil, err
	}
	return res.Job, nil
}

// BridgeCommand relays a dwbridge command to the agent, returning the mod's
// data payload (which may be nil). The error vocabulary is the shared one:
// ErrRejected when the mod doesn't implement the command (the agent's 400),
// and a plain error carrying the agent's message on 503 when the bridge is
// down. The timeout is generous because the far end is a game tick, not an
// HTTP handler — the agent applies its own tighter bound on the mod round
// trip and this only needs to outlast it.
func (c *Client) BridgeCommand(ctx context.Context, command string, args map[string]string) (json.RawMessage, error) {
	var res struct {
		Data json.RawMessage `json:"data"`
	}
	body := map[string]any{"command": command, "args": args}
	if err := c.do(ctx, http.MethodPost, "/v1/bridge/command", body, &res, 30*time.Second); err != nil {
		return nil, err
	}
	return res.Data, nil
}

// SetLaunchProfile chooses which of the game's builds the agent starts next
// — native Linux, or the Windows build under Wine that can carry the
// dwbridge mod. It applies at the next start by design: the two builds come
// from different Steam depots, so switching is a re-install rather than a
// restart, and the agent refuses to decide that timing for anyone.
func (c *Client) SetLaunchProfile(ctx context.Context, profile string) (*wkagent.LaunchStatus, error) {
	var out wkagent.LaunchStatus
	body := map[string]string{"profile": profile}
	if err := c.do(ctx, http.MethodPut, "/v1/launch", body, &out, 15*time.Second); err != nil {
		return nil, err
	}
	return &out, nil
}
