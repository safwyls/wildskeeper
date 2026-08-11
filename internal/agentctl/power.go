package agentctl

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/safwyls/wildskeeper/internal/wkagent"
)

// GameStatus mirrors the agent's wire type, like Job and Health.
type GameStatus = wkagent.GameStatus

// Power performs start/stop/restart on a supervisor-mode agent's game and
// returns the post-action status. Stop legitimately waits out the game's
// grace period, so the timeout mirrors dockerctl's stop margin.
//
// graceful (stop/restart only) tells the agent the game has already been
// asked to shut itself down in-game and that exit should be given that
// long to finish before the process is signalled; the agent waits it out,
// so it extends this call's budget too. Zero means signal immediately.
func (c *Client) Power(ctx context.Context, action string, graceful time.Duration) (*GameStatus, error) {
	var res struct {
		Game *GameStatus `json:"game"`
	}
	path := "/v1/power/" + action
	if graceful > 0 {
		path += "?graceful=" + graceful.String()
	}
	if err := c.do(ctx, http.MethodPost, path, nil, &res, 90*time.Second+graceful); err != nil {
		return nil, fmt.Errorf("agent %s: %w", action, err)
	}
	return res.Game, nil
}

// GameLogs returns the supervised game's recent output.
func (c *Client) GameLogs(ctx context.Context, tail int) ([]string, error) {
	var res struct {
		Lines []string `json:"lines"`
	}
	path := "/v1/power/logs?tail=" + strconv.Itoa(tail)
	if err := c.do(ctx, http.MethodGet, path, nil, &res, 20*time.Second); err != nil {
		return nil, err
	}
	return res.Lines, nil
}

// ProvisionResult reports what a provisioner-mode agent created.
type ProvisionResult struct {
	Container string `json:"container"`
	ID        string `json:"id"`
	DataDir   string `json:"dataDir"`
}

// Provision asks a provisioner-mode agent to instantiate the Palworld
// supervisor template. The generous timeout covers the image pull a first
// provision performs.
func (c *Client) Provision(ctx context.Context, req wkagent.ProvisionRequest) (*ProvisionResult, error) {
	var res ProvisionResult
	if err := c.do(ctx, http.MethodPost, "/v1/provision", req, &res, 10*time.Minute); err != nil {
		return nil, err
	}
	return &res, nil
}

// DiscoveredServer mirrors the provisioner's wire type.
type DiscoveredServer = wkagent.DiscoveredServer

// Discover lists Palworld-shaped containers on the provisioner's host.
func (c *Client) Discover(ctx context.Context) ([]DiscoveredServer, error) {
	var res struct {
		Servers []DiscoveredServer `json:"servers"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/discover", nil, &res, 30*time.Second); err != nil {
		return nil, err
	}
	return res.Servers, nil
}

// DestroyResult mirrors the provisioner's wire type.
type DestroyResult = wkagent.DestroyResult

// Destroy asks the provisioner to remove a container it created.
//
// The budget has to clear the agent's own worst case with margin, per the
// rule in dockerctl: a container list, then a stop that may use its full
// 30s grace inside a 90s request, then a 60s remove. Aborting early would
// report a gateway failure for a destroy the daemon goes on to complete,
// leaving the row and the container disagreeing.
func (c *Client) Destroy(ctx context.Context, container string) (*DestroyResult, error) {
	var res DestroyResult
	if err := c.do(ctx, http.MethodPost, "/v1/destroy",
		map[string]string{"container": container}, &res, 3*time.Minute); err != nil {
		return nil, err
	}
	return &res, nil
}

// AdoptResult mirrors the provisioner's wire type.
type AdoptResult = wkagent.AdoptResult

// Adopt recovers a wkagent container's registration data (secrets
// included) so wildskeeper can re-register a server whose row was lost.
func (c *Client) Adopt(ctx context.Context, container string) (*AdoptResult, error) {
	var res AdoptResult
	if err := c.do(ctx, http.MethodPost, "/v1/adopt", map[string]string{"container": container}, &res, 30*time.Second); err != nil {
		return nil, err
	}
	return &res, nil
}

// RecreateAgent rebuilds a provisioned agent container on a different
// wkagent image, keeping its configuration. The timeout is generous
// because it pulls an image first — the Wine variant is well over a
// gigabyte, and a slow pull is not a failure.
func (c *Client) RecreateAgent(ctx context.Context, container, imageTag string) (*wkagent.RecreateResult, error) {
	var res wkagent.RecreateResult
	req := wkagent.RecreateRequest{Container: container, ImageTag: imageTag}
	if err := c.do(ctx, http.MethodPost, "/v1/provision/recreate", req, &res, 15*time.Minute); err != nil {
		return nil, err
	}
	return &res, nil
}
