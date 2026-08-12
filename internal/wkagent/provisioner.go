package wkagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/safwyls/wildskeeper/internal/dockerctl"
)

// Provisioner mode (docs/sidecar-agent.md phase 5): the one component in
// the system allowed to hold docker create rights, exposing exactly one
// verb — instantiate the locked Dragonwilds supervisor template. The
// template lives here in code: a compromised wildskeeper (or leaked provisioner
// token) can stamp out more Dragonwilds servers under the configured data
// root, and nothing else — no arbitrary images, mounts, or privileges are
// expressible through this API.

// defaultContainerGamePort is the port the game binds *inside* every
// provisioned container. Fixed on purpose: only the host side of the
// mapping varies, so the template stays one shape and the agent's
// -Port= argument never has to be threaded through provisioning.
const defaultContainerGamePort = DefaultGamePort

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
var provRunAsPattern = regexp.MustCompile(`^\d{1,7}:\d{1,7}$`)

// tagPattern is docker's tag grammar. The image repository is hardcoded,
// so a hostile tag can't leave this repo's images regardless — but the
// code in front of a root socket accepts no loose input on principle.
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// ProvisionRequest instantiates the template. Every field is data for
// the fixed template — none of it changes what kind of container is made.
type ProvisionRequest struct {
	// Slug names the container (wkagent-<slug>) and the data directory
	// (<data root>/<slug>).
	Slug string `json:"slug"`
	// ImageTag selects the wkagent channel for the new server.
	ImageTag string `json:"imageTag"`
	// Token is the new agent's bearer token (wildskeeper generated it).
	Token string `json:"token"`
	// AdminPassword becomes the in-game admin password
	// (WKAGENT_ADMIN_PASSWORD), enforced into DedicatedServer.ini.
	AdminPassword string `json:"adminPassword"`
	// OwnerID is the Player ID that owns the server. Required: the game
	// refuses to start without one, so a deploy that omitted it would
	// produce a container that can only ever fail.
	OwnerID    string `json:"ownerId"`
	ServerName string `json:"serverName"`
	WorldName  string `json:"worldName"`
	// RunAs is uid:gid for the container ("" = the image's own user,
	// wkagent/uid 1000 — never root, which the game refuses to boot as).
	RunAs string `json:"runAs"`
	// GamePort is the published UDP port. The port above it is published
	// too — sources say the server uses both; testing saw only this one
	// plus an ephemeral port, so the neighbour is reserved defensively.
	GamePort  int `json:"gamePort"`
	AgentPort int `json:"agentPort"`
}

func (a *Agent) handleProvision(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Mode != "provisioner" {
		writeError(w, http.StatusBadRequest, "agent is not a provisioner")
		return
	}
	var req ProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch {
	case !slugPattern.MatchString(req.Slug):
		writeError(w, http.StatusBadRequest, "slug must be lowercase letters, digits and dashes")
		return
	case len(req.Token) < minTokenLen:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("token must be at least %d characters", minTokenLen))
		return
	case req.AdminPassword == "":
		writeError(w, http.StatusBadRequest, "admin password is required")
		return
	case strings.TrimSpace(req.OwnerID) == "":
		writeError(w, http.StatusBadRequest, "owner id is required — the game will not start without one")
		return
	case req.RunAs != "" && !provRunAsPattern.MatchString(req.RunAs):
		writeError(w, http.StatusBadRequest, "runAs must be numeric uid:gid")
		return
	}
	if req.ImageTag == "" {
		req.ImageTag = "latest"
	}
	if !tagPattern.MatchString(req.ImageTag) {
		writeError(w, http.StatusBadRequest, "image tag must match docker tag grammar")
		return
	}
	// The game binds GamePort and GamePort+1, so the pair must fit and
	// must not collide with the agent's port.
	if req.GamePort < 1 || req.GamePort > 65534 {
		writeError(w, http.StatusBadRequest, "game port must be in 1-65534 (the game also uses the port above it)")
		return
	}
	if req.AgentPort < 1 || req.AgentPort > 65535 {
		writeError(w, http.StatusBadRequest, "agent port must be in 1-65535")
		return
	}
	if req.AgentPort == req.GamePort || req.AgentPort == req.GamePort+1 {
		writeError(w, http.StatusBadRequest, "agent port collides with the game's port pair")
		return
	}

	name := "wkagent-" + req.Slug
	// A name already in use can only fail at create — after the mkdir, the
	// chown and a multi-hundred-MB image pull have all reported progress.
	// Check it first, and refuse in the one status the caller can read as
	// "the provisioner made nothing" rather than "something went wrong
	// partway through".
	if containers, err := a.docker.ContainerList(r.Context()); err == nil {
		for _, c := range containers {
			if c.Name == name {
				writeError(w, http.StatusConflict, "a container named "+name+" already exists on this host")
				return
			}
		}
	}

	// From here the work is game-agnostic: everything specific to
	// Dragonwilds has been turned into env, ports and an image, which is
	// exactly the boundary a host provisioner should sit on. The same
	// routine serves a spec posted directly by any console.
	env := map[string]string{
		"HOME":                  "/tmp",
		"WKAGENT_MODE":          "supervisor",
		"WKAGENT_TOKEN":         req.Token,
		"WKAGENT_ADMIN_PASSWORD": req.AdminPassword,
		"WKAGENT_OWNER_ID":      strings.TrimSpace(req.OwnerID),
	}
	if req.ServerName != "" {
		env["WKAGENT_SERVER_NAME"] = req.ServerName
	}
	if req.WorldName != "" {
		env["WKAGENT_WORLD_NAME"] = req.WorldName
	}
	spec := ProvisionSpec{
		Name:  name,
		Slug:  req.Slug,
		Image: "ghcr.io/safwyls/wkagent:" + req.ImageTag,
		User:  req.RunAs,
		Env:   env,
		// The container-side ports are fixed; only the host side varies.
		// The game has no RCON or REST interface to publish — everything
		// the dashboard reads comes through the agent's own port.
		Ports: []PortMap{
			{Host: req.GamePort, Container: defaultContainerGamePort, Proto: "udp"},
			{Host: req.GamePort + 1, Container: defaultContainerGamePort + 1, Proto: "udp"},
			{Host: req.AgentPort, Container: 8811, Proto: "tcp"},
		},
		DataMount: "/dragonwilds",
	}
	dataDir, image, err := a.place(r.Context(), spec)
	if err != nil {
		writePlaceError(w, err)
		return
	}
	a.cfg.Logger.Info("provisioned server", "container", name, "dataDir", dataDir, "image", image)
	writeJSON(w, http.StatusCreated, map[string]any{
		"container": name,
		"dataDir":   dataDir,
	})
}

// ProvisionDefaults is what the wizard can infer instead of asking: the
// provisioner's own configuration is the source of truth for where and
// how servers land on this host.
type ProvisionDefaults struct {
	DataRoot string `json:"dataRoot"`
	// PublicHost is the address wildskeeper (and players) reach this host on —
	// WKAGENT_PUBLIC_HOST. Inside containers "localhost" means the
	// container itself, so this must be declared, not guessed.
	PublicHost string `json:"publicHost,omitempty"`
	RunAs      string `json:"runAs"`
	ImageTag   string `json:"imageTag"`
}

// DiscoveredServer is one wkagent-shaped container found on the host.
// Deliberately free of environment values: a container's env carries its
// token and admin password, and those never leave the provisioner.
type DiscoveredServer struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	Mode    string `json:"mode"` // supervisor | companion | "" (unknown)
	Running bool   `json:"running"`
	// Published host ports for the well-known container ports.
	GamePort  int `json:"gamePort,omitempty"`
	AgentPort int `json:"agentPort,omitempty"`
}

// handleDiscover lists wkagent-shaped containers on the host so the add
// dialog can offer existing installs for adoption.
func (a *Agent) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Mode != "provisioner" {
		writeError(w, http.StatusBadRequest, "agent is not a provisioner")
		return
	}
	containers, err := a.docker.ContainerList(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var out []DiscoveredServer
	for _, c := range containers {
		if !strings.Contains(c.Image, "wkagent") && c.Labels["wildskeeper.provisioned"] != "true" {
			continue
		}
		mode := ""
		if env, err := a.docker.InspectEnv(r.Context(), c.ID); err == nil {
			for _, e := range env {
				// Only the mode crosses the boundary — never other env.
				if v, ok := strings.CutPrefix(e, "WKAGENT_MODE="); ok {
					mode = v
				}
			}
		}
		if mode == "provisioner" {
			continue // that's us (or a peer), not a game server
		}
		if mode == "" {
			mode = "companion" // wkagent's default mode
		}
		out = append(out, DiscoveredServer{
			Name:      c.Name,
			Image:     c.Image,
			Mode:      mode,
			Running:   c.State == "running",
			GamePort:  c.Ports[fmt.Sprintf("%d/udp", defaultContainerGamePort)],
			AgentPort: c.Ports["8811/tcp"],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out})
}

// AdoptResult carries everything wildskeeper needs to re-register an existing
// wkagent container — including its secrets. Deliberately: the
// provisioner created these containers and injected those secrets in the
// first place, so returning them to the (token-authenticated) control
// plane stays within the same trust boundary. It is still restricted to
// wkagent containers — never arbitrary ones.
type AdoptResult struct {
	Name          string `json:"name"`
	Mode          string `json:"mode"`
	ServerName    string `json:"serverName,omitempty"`
	Token         string `json:"token"`
	AdminPassword string `json:"adminPassword"`
	OwnerID       string `json:"ownerId,omitempty"`
	GamePort      int    `json:"gamePort,omitempty"`
	AgentPort     int    `json:"agentPort,omitempty"`
}

// handleAdopt recovers a discovered container's registration data —
// the answer to "I deleted the server row; its container is still
// running and I no longer have the token".
func (a *Agent) handleAdopt(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Mode != "provisioner" {
		writeError(w, http.StatusBadRequest, "agent is not a provisioner")
		return
	}
	var req struct {
		Container string `json:"container"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Container == "" {
		writeError(w, http.StatusBadRequest, "container name is required")
		return
	}
	containers, err := a.docker.ContainerList(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	for _, c := range containers {
		if c.Name != req.Container {
			continue
		}
		if !strings.Contains(c.Image, "wkagent") && c.Labels["wildskeeper.provisioned"] != "true" {
			writeError(w, http.StatusBadRequest, "not a wkagent container")
			return
		}
		env, err := a.docker.InspectEnv(r.Context(), c.ID)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		res := AdoptResult{
			Name:      c.Name,
			Mode:      "companion",
			GamePort:  c.Ports[fmt.Sprintf("%d/udp", defaultContainerGamePort)],
			AgentPort: c.Ports["8811/tcp"],
		}
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, "WKAGENT_MODE="); ok {
				res.Mode = v
			}
			if v, ok := strings.CutPrefix(e, "WKAGENT_TOKEN="); ok {
				res.Token = v
			}
			if v, ok := strings.CutPrefix(e, "WKAGENT_ADMIN_PASSWORD="); ok {
				res.AdminPassword = v
			}
			if v, ok := strings.CutPrefix(e, "WKAGENT_SERVER_NAME="); ok {
				res.ServerName = v
			}
		}
		if res.Mode == "provisioner" {
			writeError(w, http.StatusBadRequest, "that container is a provisioner, not a game server")
			return
		}
		a.cfg.Logger.Info("adoption data served", "container", c.Name)
		writeJSON(w, http.StatusOK, res)
		return
	}
	writeError(w, http.StatusNotFound, "no container with that name")
}

// DestroyResult reports what the destroy verb unmade. DataDir comes back
// so the operator learns where the world still is — destroying a
// container is not consent to delete a save.
type DestroyResult struct {
	Container string `json:"container"`
	DataDir   string `json:"dataDir,omitempty"`
}

// handleDestroy removes a container this provisioner created.
//
// The label gate is the whole security argument. `wildskeeper.provisioned=true`
// is written in exactly one place — handleProvision — so destroy can only
// ever unmake what provision made. That is deliberately narrower than
// discover/adopt, which also match on the wkagent image name: a
// hand-deployed wkagent (a TrueNAS app, a pasted stack) is something the
// operator manages elsewhere, and this verb must not reach into it. So a
// leaked provisioner token buys the ability to delete containers that
// same token's provisioner created, and nothing else on the host.
//
// The container's volume is never removed — the world lives in a host
// bind mount under the data root and outlives the container it was
// created for.
func (a *Agent) handleDestroy(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Mode != "provisioner" {
		writeError(w, http.StatusBadRequest, "agent is not a provisioner")
		return
	}
	var req struct {
		Container string `json:"container"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Container) == "" {
		writeError(w, http.StatusBadRequest, "container name is required")
		return
	}
	name := strings.TrimSpace(req.Container)

	containers, err := a.docker.ContainerList(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	for _, c := range containers {
		if c.Name != name {
			continue
		}
		if c.Labels["wildskeeper.provisioned"] != "true" {
			writeError(w, http.StatusBadRequest,
				"that container was not created by this provisioner — remove it wherever it was deployed")
			return
		}
		// Stop first so the supervisor gets its grace period and the game
		// flushes the world: the save this leaves behind is the whole
		// reason the data dir is kept. A stop that fails is not fatal on
		// its own — docker refuses to remove a running container, and its
		// 409 says so more usefully than anything invented here.
		if err := a.docker.Stop(r.Context(), c.ID); err != nil {
			a.cfg.Logger.Warn("stop before destroy failed; attempting remove anyway",
				"container", name, "error", err)
		}
		if err := a.docker.ContainerRemove(r.Context(), c.ID); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		dataDir := ""
		if slug := c.Labels["wildskeeper.slug"]; slug != "" {
			dataDir = filepath.Join(a.cfg.DataRoot, slug)
		}
		a.cfg.Logger.Info("destroyed server", "container", name, "dataKept", dataDir)
		writeJSON(w, http.StatusOK, DestroyResult{Container: name, DataDir: dataDir})
		return
	}
	writeError(w, http.StatusNotFound, "no container with that name")
}

// validateProvisionerConfig is called from New for mode=provisioner.
func validateProvisionerConfig(cfg *Config) (*dockerctl.Client, error) {
	if cfg.DataRoot == "" {
		return nil, errors.New("provisioner mode requires a data root")
	}
	if !filepath.IsAbs(cfg.DataRoot) {
		return nil, errors.New("data root must be absolute")
	}
	if cfg.DockerHost == "" {
		cfg.DockerHost = "unix:///var/run/docker.sock"
	}
	if cfg.DefaultRunAs == "" {
		cfg.DefaultRunAs = "568:568"
	}
	if cfg.DefaultImageTag == "" {
		cfg.DefaultImageTag = "latest"
	}
	return dockerctl.New(cfg.DockerHost)
}

// RecreateRequest asks for a provisioned agent container to be rebuilt on a
// different image.
type RecreateRequest struct {
	// Container is the existing container's name.
	Container string `json:"container"`
	// ImageTag is the wkagent channel to move to, e.g. "latest-wine".
	ImageTag string `json:"imageTag"`
}

type RecreateResult struct {
	Container string `json:"container"`
	Image     string `json:"image"`
	Previous  string `json:"previousImage"`
}

// handleRecreate rebuilds a provisioned agent container on a new image,
// keeping everything else exactly as it was.
//
// Docker has no "change the image" operation — only create — so swapping
// one means removing and rebuilding the container. That is easy to do
// destructively and hard to do faithfully, which is why it lives here
// rather than in a runbook: this provisioner created these containers, can
// read their configuration back, and can put every part of it back.
// Without it, moving a server to the Wine image means hand-writing a
// docker run on a host whose orchestrator does not manage these containers
// at all.
//
// The world is not at risk: it lives in a host bind mount under the data
// root, which is captured and re-attached, and ContainerRemove never takes
// volumes with it.
func (a *Agent) handleRecreate(w http.ResponseWriter, r *http.Request) {
	var req RecreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Container == "" {
		writeError(w, http.StatusBadRequest, "container is required")
		return
	}
	if !tagPattern.MatchString(req.ImageTag) {
		writeError(w, http.StatusBadRequest, "invalid image tag")
		return
	}

	// Same ownership gate as destroy: this provisioner rebuilds what it
	// made, and nothing else on the host.
	containers, err := a.docker.ContainerList(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var found *dockerctl.ContainerSummary
	for i := range containers {
		if containers[i].Name == req.Container {
			found = &containers[i]
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "no container with that name")
		return
	}
	if found.Labels["wildskeeper.provisioned"] != "true" {
		writeError(w, http.StatusBadRequest,
			"that container was not created by this provisioner — change its image wherever it was deployed")
		return
	}

	spec, err := a.docker.InspectSpec(r.Context(), found.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "reading the container's configuration: "+err.Error())
		return
	}
	previous := spec.Image
	image := "ghcr.io/safwyls/wkagent:" + req.ImageTag
	if image == previous {
		writeJSON(w, http.StatusOK, RecreateResult{Container: req.Container, Image: image, Previous: previous})
		return
	}

	// Pull before removing anything. A tag that doesn't exist must fail
	// while the old container is still there, not after it's gone.
	if err := a.docker.ImagePull(r.Context(), image); err != nil {
		writeError(w, http.StatusBadGateway, "pulling "+image+": "+err.Error())
		return
	}
	spec.Image = image

	// Stop first so the supervisor gets its grace period and the game
	// flushes the world — the same courtesy destroy pays, for the same
	// reason.
	if err := a.docker.Stop(r.Context(), found.ID); err != nil {
		a.cfg.Logger.Warn("stop before recreate failed; attempting remove anyway", "container", req.Container, "error", err)
	}
	if err := a.docker.ContainerRemove(r.Context(), found.ID); err != nil {
		writeError(w, http.StatusBadGateway, "removing the old container: "+err.Error())
		return
	}
	if _, err := a.docker.ContainerCreate(r.Context(), *spec); err != nil {
		// The old container is already gone, so say plainly what state the
		// host is in rather than leaving it to be discovered.
		a.cfg.Logger.Error("recreate failed after removing the old container", "container", req.Container, "error", err)
		writeError(w, http.StatusBadGateway,
			"the old container was removed but the new one could not be created ("+err.Error()+"); the world data is safe in the data directory")
		return
	}
	if err := a.docker.Start(r.Context(), req.Container); err != nil {
		writeError(w, http.StatusBadGateway, "recreated but failed to start: "+err.Error())
		return
	}
	a.cfg.Logger.Info("recreated server agent", "container", req.Container, "from", previous, "to", image)
	writeJSON(w, http.StatusOK, RecreateResult{Container: req.Container, Image: image, Previous: previous})
}

// placeError carries the status a failed placement should answer with, so
// the shared routine can be used by handlers that must report precisely
// which step failed rather than collapsing everything to 502.
type placeError struct {
	status int
	err    error
}

func (e *placeError) Error() string { return e.err.Error() }

func writePlaceError(w http.ResponseWriter, err error) {
	var pe *placeError
	if errors.As(err, &pe) {
		writeError(w, pe.status, pe.err.Error())
		return
	}
	writeError(w, http.StatusBadGateway, err.Error())
}

// place creates and starts one container from a spec, and is the single
// path every provision takes — the game-shaped handler builds a spec and
// comes through here, exactly as a spec posted by any other console would.
// Having one implementation is the point: a second path would be a second
// set of ownership labels, chown rules and failure semantics to keep in
// step.
//
// It returns the data directory and the image actually used.
func (a *Agent) place(ctx context.Context, spec ProvisionSpec) (string, string, error) {
	if err := spec.Validate(a.cfg.AllowedImagePrefixes); err != nil {
		return "", "", &placeError{http.StatusBadRequest, err}
	}

	// The data directory is always DataRoot/<slug> — the slug pattern
	// forbids traversal, and nothing else about the location is
	// caller-controlled.
	dataDir := filepath.Join(a.cfg.DataRoot, spec.Slug)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", "", &placeError{http.StatusInternalServerError, fmt.Errorf("creating data dir: %w", err)}
	}
	if spec.User != "" {
		parts := strings.SplitN(spec.User, ":", 2)
		uid, _ := strconv.Atoi(parts[0])
		gid := uid
		if len(parts) == 2 {
			gid, _ = strconv.Atoi(parts[1])
		}
		if err := os.Chown(dataDir, uid, gid); err != nil {
			// Non-fatal only if the dir is already writable by the user;
			// SteamCMD will tell on it loudly otherwise.
			a.cfg.Logger.Warn("could not chown data dir", "dir", dataDir, "error", err)
		}
	}

	if err := a.docker.ImagePull(ctx, spec.Image); err != nil {
		return "", "", &placeError{http.StatusBadGateway, err}
	}

	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env) // stable ordering, so a recreate diffs cleanly
	ports := map[int]string{}
	for _, p := range spec.Ports {
		proto := p.Proto
		if proto == "" {
			proto = "tcp"
		}
		ports[p.Host] = fmt.Sprintf("%d/%s", p.Container, proto)
	}
	mount := spec.DataMount
	if mount == "" {
		mount = "/data"
	}
	// Ownership labels are applied last so a caller cannot overwrite them:
	// they are what every later destroy and recreate checks before touching
	// anything, and a forged one would let a console claim a container this
	// provisioner never made.
	labels := map[string]string{}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels["wildskeeper.provisioned"] = "true"
	labels["wildskeeper.slug"] = spec.Slug

	if _, err := a.docker.ContainerCreate(ctx, dockerctl.ContainerSpec{
		Name:                 spec.Name,
		Image:                spec.Image,
		User:                 spec.User,
		Env:                  env,
		Binds:                []string{dataDir + ":" + mount},
		Ports:                ports,
		Labels:               labels,
		RestartUnlessStopped: true,
	}); err != nil {
		return "", "", &placeError{http.StatusBadGateway, err}
	}
	if err := a.docker.Start(ctx, spec.Name); err != nil {
		return "", "", &placeError{http.StatusBadGateway, fmt.Errorf("created but failed to start: %w", err)}
	}
	return dataDir, spec.Image, nil
}

// handleProvisionSpec places a container from a game-agnostic spec.
//
// This is the contract a second console speaks: it sends what its game
// needs as data, and this provisioner places it without knowing what any of
// it means. The game-shaped /v1/provision above is now just one caller of
// the same routine — which is the evidence that the boundary is real rather
// than aspirational.
func (a *Agent) handleProvisionSpec(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Mode != "provisioner" {
		writeError(w, http.StatusBadRequest, "agent is not a provisioner")
		return
	}
	var spec ProvisionSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if containers, err := a.docker.ContainerList(r.Context()); err == nil {
		for _, c := range containers {
			if c.Name == spec.Name {
				writeError(w, http.StatusConflict, "a container named "+spec.Name+" already exists on this host")
				return
			}
		}
	}
	dataDir, image, err := a.place(r.Context(), spec)
	if err != nil {
		writePlaceError(w, err)
		return
	}
	a.cfg.Logger.Info("provisioned from spec", "container", spec.Name, "dataDir", dataDir, "image", image)
	writeJSON(w, http.StatusCreated, map[string]any{"container": spec.Name, "dataDir": dataDir, "image": image})
}
