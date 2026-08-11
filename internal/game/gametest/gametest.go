// Package gametest registers a REST-shaped fake game for tests of the
// shared layer (API handlers, collector, scheduler, watchdog).
//
// Dragonwilds itself has no query protocol — its client derives state
// through the sidecar agent — which makes it a poor instrument for testing
// the shared plumbing: every test would need a fake agent and a synthetic
// log. This game speaks the plain HTTP vocabulary the shared tests' fakes
// already serve (the `/v1/api/*` paths inherited from wildskeeper's Palworld
// REST protocol), so a test can point a row at an httptest server and watch
// exactly which calls arrive. Production code never registers it; importing
// this package from non-test code is a review error.
package gametest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/safwyls/wildskeeper/internal/game"
)

// ID is the game id test rows set to resolve to this client.
const ID = "resttest"

func init() {
	game.Register(&game.Definition{
		ID:              ID,
		Name:            "REST test game",
		DefaultGamePort: 8211,
		NewClient:       newClient,
		CanonicalUID:    func(uid string) string { return strings.ToLower(strings.TrimSpace(uid)) },
		Features:        game.AllFeatures(),
	})
}

// newClient mirrors the shape shared code expects from a two-tier game:
// with PreferREST the client also implements game.ExtendedClient; without
// it, settings and metrics are honestly absent and callers must degrade.
func newClient(conn game.Conn) game.Client {
	b := &base{
		url:  fmt.Sprintf("http://%s:%d", conn.Host, conn.RESTPort),
		http: &http.Client{Timeout: 10 * time.Second},
	}
	if !conn.PreferREST {
		return b
	}
	return &extended{base: b}
}

type base struct {
	url  string
	http *http.Client
}

// extended adds the REST-only surface.
type extended struct{ *base }

func (b *base) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.url+path, payload)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	// 501 is how a fake server says "this game can't", mirroring the shape
	// real clients have: Dragonwilds returns *game.UnsupportedError for a
	// command with no channel to reach the game through. Without this a
	// shared-layer test could only ever produce a generic fault, and the
	// capability/fault split — which callers branch on — would be untestable
	// from here.
	if resp.StatusCode == http.StatusNotImplemented {
		return &game.UnsupportedError{Op: strings.TrimPrefix(path, "/v1/api/"), Reason: strings.TrimSpace(string(data))}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("game api %s: %s: %s", path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (b *base) Info(ctx context.Context) (*game.ServerInfo, error) {
	var info game.ServerInfo
	if err := b.do(ctx, http.MethodGet, "/v1/api/info", nil, &info); err != nil {
		return nil, err
	}
	players, err := b.Players(ctx)
	if err != nil {
		return nil, err
	}
	info.PlayerCount = len(players)
	info.Transport = "rest"
	return &info, nil
}

func (b *base) Players(ctx context.Context) ([]game.Player, error) {
	var res struct {
		Players []game.Player `json:"players"`
	}
	if err := b.do(ctx, http.MethodGet, "/v1/api/players", nil, &res); err != nil {
		return nil, err
	}
	return res.Players, nil
}

func (b *base) Broadcast(ctx context.Context, message string) error {
	return b.do(ctx, http.MethodPost, "/v1/api/announce", map[string]string{"message": message}, nil)
}

func (b *base) Kick(ctx context.Context, playerUID, message string) error {
	return b.do(ctx, http.MethodPost, "/v1/api/kick", map[string]string{"userid": playerUID, "message": message}, nil)
}

func (b *base) Ban(ctx context.Context, playerUID, message string) error {
	return b.do(ctx, http.MethodPost, "/v1/api/ban", map[string]string{"userid": playerUID, "message": message}, nil)
}

func (b *base) Unban(ctx context.Context, playerUID string) error {
	return b.do(ctx, http.MethodPost, "/v1/api/unban", map[string]string{"userid": playerUID}, nil)
}

func (b *base) Save(ctx context.Context) error {
	return b.do(ctx, http.MethodPost, "/v1/api/save", nil, nil)
}

func (b *base) Shutdown(ctx context.Context, waitSeconds int, message string) error {
	return b.do(ctx, http.MethodPost, "/v1/api/shutdown",
		map[string]any{"waittime": waitSeconds, "message": message}, nil)
}

func (e *extended) Settings(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := e.do(ctx, http.MethodGet, "/v1/api/settings", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *extended) Metrics(ctx context.Context) (*game.Metrics, error) {
	var out game.Metrics
	if err := e.do(ctx, http.MethodGet, "/v1/api/metrics", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
