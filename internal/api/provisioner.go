package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/safwyls/wildskeeper/internal/agentctl"
	"github.com/safwyls/wildskeeper/internal/ilmari"
	"github.com/safwyls/wildskeeper/internal/wkagent"
)

// Provisioner is what the API layer needs from whatever places containers
// on the host. Two implementations exist during the migration
// (docs in the ilmari repo, docs/migration.md): the legacy provisioner-mode
// wkagent (agentctl.Client, which satisfies this as-is) and Ilmari, the
// shared host service. The interface speaks the legacy shapes on purpose —
// they are what the wizard and its frontend already consume — and the
// Ilmari implementation translates at the boundary. When the legacy path
// is retired, the shapes can migrate toward Ilmari's; until then, one
// vocabulary.
type Provisioner interface {
	BaseURL() string
	Health(ctx context.Context) (*agentctl.Health, error)
	Provision(ctx context.Context, req wkagent.ProvisionRequest) (*agentctl.ProvisionResult, error)
	Discover(ctx context.Context) ([]agentctl.DiscoveredServer, error)
	Adopt(ctx context.Context, container string) (*agentctl.AdoptResult, error)
	RecreateAgent(ctx context.Context, container, imageTag string) (*wkagent.RecreateResult, error)
	Destroy(ctx context.Context, container string) (*agentctl.DestroyResult, error)
}

// Interface satisfaction is a compile-time fact, not a hope.
var (
	_ Provisioner = (*agentctl.Client)(nil)
	_ Provisioner = (*IlmariProvisioner)(nil)
)

// IlmariProvisioner adapts the Ilmari host service to the Provisioner
// interface.
//
// This adapter is where the game knowledge that used to live in wkagent's
// provisioner mode now lives: which env vars configure a Dragonwilds
// sidecar, that the game publishes a UDP port pair, that the agent listens
// on 8811, which image family to deploy. Ilmari itself knows none of it —
// that is the contract — so anything Dragonwilds-shaped in a provisioning
// flow belongs in this file and nowhere further down.
type IlmariProvisioner struct {
	c *ilmari.Client
}

func NewIlmariProvisioner(c *ilmari.Client) *IlmariProvisioner {
	return &IlmariProvisioner{c: c}
}

func (p *IlmariProvisioner) BaseURL() string { return p.c.BaseURL() }

// The game's container-side port facts, in exactly one place.
const (
	gameContainerPort  = wkagent.DefaultGamePort // 7777/udp, and the pair partner above it
	agentContainerPort = 8811
	agentImage         = "ghcr.io/safwyls/wkagent"
	dataMount          = "/dragonwilds"
)

// Health synthesizes the legacy health shape from Ilmari's. The wizard
// reads Provision.DataRoot to place data directories and PublicHost to
// prefill the join address; both come straight from this console's Ilmari
// registration. ImageTag has no Ilmari-side counterpart — the channel is
// the console's own default.
func (p *IlmariProvisioner) Health(ctx context.Context) (*agentctl.Health, error) {
	h, err := p.c.Health(ctx)
	if err != nil {
		return nil, err
	}
	return &agentctl.Health{
		Agent:      "ilmari",
		Version:    h.Version,
		APIVersion: wkagent.APIVersion,
		Mode:       "provisioner",
		Provision: &wkagent.ProvisionDefaults{
			DataRoot:   h.DataRoot,
			PublicHost: h.PublicHost,
			RunAs:      h.RunAs,
			ImageTag:   "latest",
		},
	}, nil
}

// Provision translates the game-shaped request into Ilmari's spec — the
// same assembly wkagent's own handler performs, kept in step with it until
// that handler is retired.
func (p *IlmariProvisioner) Provision(ctx context.Context, req wkagent.ProvisionRequest) (*agentctl.ProvisionResult, error) {
	env := map[string]string{
		"HOME":                   "/tmp",
		"WKAGENT_MODE":           "supervisor",
		"WKAGENT_TOKEN":          req.Token,
		"WKAGENT_ADMIN_PASSWORD": req.AdminPassword,
		"WKAGENT_OWNER_ID":       strings.TrimSpace(req.OwnerID),
	}
	if req.ServerName != "" {
		env["WKAGENT_SERVER_NAME"] = req.ServerName
	}
	if req.WorldName != "" {
		env["WKAGENT_WORLD_NAME"] = req.WorldName
	}
	tag := req.ImageTag
	if tag == "" {
		tag = "latest"
	}
	res, err := p.c.Provision(ctx, ilmari.Spec{
		Name:  "wkagent-" + req.Slug,
		Slug:  req.Slug,
		Image: agentImage + ":" + tag,
		User:  req.RunAs,
		Env:   env,
		Ports: []ilmari.PortMap{
			{Host: req.GamePort, Container: gameContainerPort, Proto: "udp"},
			{Host: req.GamePort + 1, Container: gameContainerPort + 1, Proto: "udp"},
			{Host: req.AgentPort, Container: agentContainerPort, Proto: "tcp"},
		},
		DataMount: dataMount,
	})
	if err != nil {
		return nil, err
	}
	return &agentctl.ProvisionResult{Container: res.Container, DataDir: res.DataDir}, nil
}

// Discover maps Ilmari's candidates into the legacy shape. Mode is honest
// emptiness: Ilmari does not read container environments for discovery, so
// unlike the old provisioner it cannot say supervisor/companion — and it
// cannot filter out a still-running legacy provisioner container either,
// which will appear in the list until Phase 4 removes it. Adopt refuses it
// with a clear message if selected.
func (p *IlmariProvisioner) Discover(ctx context.Context) ([]agentctl.DiscoveredServer, error) {
	found, err := p.c.Discover(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]agentctl.DiscoveredServer, 0, len(found))
	for _, f := range found {
		out = append(out, agentctl.DiscoveredServer{
			Name:      f.Name,
			Image:     f.Image,
			Running:   f.Running,
			GamePort:  hostPortFor(f.Ports, gameContainerPort, "udp"),
			AgentPort: hostPortFor(f.Ports, agentContainerPort, "tcp"),
		})
	}
	return out, nil
}

// Adopt recovers a registration from the env Ilmari returns — already
// filtered to WKAGENT_* by this console's registration, so reading it here
// is reading our own writes back.
func (p *IlmariProvisioner) Adopt(ctx context.Context, container string) (*agentctl.AdoptResult, error) {
	a, err := p.c.Adopt(ctx, container)
	if err != nil {
		return nil, err
	}
	mode := a.Env["WKAGENT_MODE"]
	if mode == "" {
		mode = "companion" // wkagent's default when unset
	}
	if mode == "provisioner" {
		// The legacy provisioner container is discoverable (it runs the
		// wkagent image, unlabelled) but is not a game server. The old
		// provisioner filtered it out of discovery; Ilmari cannot, so the
		// refusal lands here instead.
		return nil, fmt.Errorf("%s is a provisioner, not a game server — it is retired in the last step of the Ilmari migration", container)
	}
	return &agentctl.AdoptResult{
		Name:          a.Name,
		Mode:          mode,
		ServerName:    a.Env["WKAGENT_SERVER_NAME"],
		Token:         a.Env["WKAGENT_TOKEN"],
		AdminPassword: a.Env["WKAGENT_ADMIN_PASSWORD"],
		OwnerID:       a.Env["WKAGENT_OWNER_ID"],
		GamePort:      hostPortFor(a.Ports, gameContainerPort, "udp"),
		AgentPort:     hostPortFor(a.Ports, agentContainerPort, "tcp"),
	}, nil
}

func (p *IlmariProvisioner) RecreateAgent(ctx context.Context, container, imageTag string) (*wkagent.RecreateResult, error) {
	res, err := p.c.Recreate(ctx, container, agentImage+":"+imageTag)
	if err != nil {
		return nil, err
	}
	return &wkagent.RecreateResult{Container: res.Container, Image: res.Image, Previous: res.Previous}, nil
}

func (p *IlmariProvisioner) Destroy(ctx context.Context, container string) (*agentctl.DestroyResult, error) {
	res, err := p.c.Destroy(ctx, container)
	if err != nil {
		return nil, err
	}
	return &agentctl.DestroyResult{Container: res.Container, DataDir: res.DataDir}, nil
}

// hostPortFor finds the published host port for a container-side port, the
// way the old provisioner read its well-known ports.
func hostPortFor(ports []ilmari.PortMap, containerPort int, proto string) int {
	for _, p := range ports {
		if p.Container == containerPort && (p.Proto == proto || p.Proto == "") {
			return p.Host
		}
	}
	return 0
}
