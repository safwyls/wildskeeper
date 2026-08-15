// Package ilmari is the client for the Ilmari host-provisioning service
// (github.com/safwyls/ilmari) — the shared, game-agnostic replacement for
// the per-console provisioner-mode agents.
//
// The division of knowledge is the point of the wire shapes here: Ilmari
// knows containers, images, ports and data directories, and this console
// knows what a Dragonwilds server is made of. So everything in this package
// is deliberately dumb — specs in, results out — and the translation from
// "a server named Ashenfall on port 7777" into env vars and port maps lives
// with the caller (internal/api's provisioner adapter), not here and never
// in Ilmari.
package ilmari

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(baseURL, token string) (*Client, error) {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, errors.New("ilmari url is empty")
	}
	if token == "" {
		return nil, errors.New("ilmari token is empty")
	}
	// No client-wide timeout: each call sets its own, because a single
	// deadline can't cover both a fast health read and a provision that
	// legitimately spends minutes pulling an image.
	return &Client{base: baseURL, token: token, http: &http.Client{}}, nil
}

func (c *Client) BaseURL() string { return c.base }

func (c *Client) do(ctx context.Context, method, path string, in, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ilmari unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("ilmari: %s", e.Error)
		}
		return fmt.Errorf("ilmari: %s", resp.Status)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Health mirrors Ilmari's /v1/health — already scoped to this client's
// registration (its own data root, its own allowlist).
type Health struct {
	Service              string   `json:"service"`
	Version              string   `json:"version"`
	APIVersion           int      `json:"apiVersion"`
	Client               string   `json:"client"`
	DataRoot             string   `json:"dataRoot"`
	PublicHost           string   `json:"publicHost"`
	RunAs                string   `json:"runAs"`
	AllowedImagePrefixes []string `json:"allowedImagePrefixes"`
	DockerOk             bool     `json:"dockerOk"`
}

func (c *Client) Health(ctx context.Context) (*Health, error) {
	var h Health
	if err := c.do(ctx, http.MethodGet, "/v1/health", nil, &h, 10*time.Second); err != nil {
		return nil, err
	}
	return &h, nil
}

// PortMap publishes one container port on the host.
type PortMap struct {
	Host      int    `json:"host"`
	Container int    `json:"container"`
	Proto     string `json:"proto,omitempty"`
}

// Spec is Ilmari's provisioning contract: everything the game needs, as
// data. See the package comment for who fills it in.
type Spec struct {
	Name      string            `json:"name"`
	Slug      string            `json:"slug"`
	Image     string            `json:"image"`
	User      string            `json:"user,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Ports     []PortMap         `json:"ports,omitempty"`
	DataMount string            `json:"dataMount,omitempty"`
}

type PlaceResult struct {
	Container string `json:"container"`
	DataDir   string `json:"dataDir"`
	Image     string `json:"image"`
}

// Provision places one container. The generous timeout covers the image
// pull a first placement performs.
func (c *Client) Provision(ctx context.Context, spec Spec) (*PlaceResult, error) {
	var res PlaceResult
	if err := c.do(ctx, http.MethodPost, "/v1/provision", spec, &res, 10*time.Minute); err != nil {
		return nil, err
	}
	return &res, nil
}

type RecreateResult struct {
	Container string `json:"container"`
	Image     string `json:"image"`
	Previous  string `json:"previousImage"`
}

// Recreate rebuilds a container on a different image, keeping everything
// else. Long timeout for the same reason as Provision: the Wine image is
// over a gigabyte and a slow pull is not a failure.
func (c *Client) Recreate(ctx context.Context, container, image string) (*RecreateResult, error) {
	var res RecreateResult
	body := map[string]string{"container": container, "image": image}
	if err := c.do(ctx, http.MethodPost, "/v1/provision/recreate", body, &res, 15*time.Minute); err != nil {
		return nil, err
	}
	return &res, nil
}

type DestroyResult struct {
	Container string `json:"container"`
	DataDir   string `json:"dataDir"`
}

// Destroy removes a container, keeping its data directory. The budget
// covers a stop that legitimately uses its full grace period.
func (c *Client) Destroy(ctx context.Context, container string) (*DestroyResult, error) {
	var res DestroyResult
	body := map[string]string{"container": container}
	if err := c.do(ctx, http.MethodPost, "/v1/provision/destroy", body, &res, 3*time.Minute); err != nil {
		return nil, err
	}
	return &res, nil
}

// Discovered is one adoption candidate: a container this console owns, or
// an unlabelled one under its image allowlist (a paste-flow deploy).
type Discovered struct {
	Name    string    `json:"name"`
	Image   string    `json:"image"`
	Running bool      `json:"running"`
	Managed bool      `json:"managed"`
	Ports   []PortMap `json:"ports"`
}

func (c *Client) Discover(ctx context.Context) ([]Discovered, error) {
	var res struct {
		Servers []Discovered `json:"servers"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/discover", nil, &res, 30*time.Second); err != nil {
		return nil, err
	}
	return res.Servers, nil
}

// Adopted is a recovered registration: the container plus its environment,
// filtered by Ilmari to this console's registered namespace (WKAGENT_*).
type Adopted struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Running bool              `json:"running"`
	Ports   []PortMap         `json:"ports"`
	Env     map[string]string `json:"env"`
}

func (c *Client) Adopt(ctx context.Context, container string) (*Adopted, error) {
	var res Adopted
	body := map[string]string{"container": container}
	if err := c.do(ctx, http.MethodPost, "/v1/adopt", body, &res, 30*time.Second); err != nil {
		return nil, err
	}
	return &res, nil
}
