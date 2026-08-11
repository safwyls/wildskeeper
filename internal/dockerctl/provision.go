package dockerctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file exists for the wkagent provisioner, not for wildskeeper: wildskeeper's
// proxy deliberately can't create containers (see the package comment).
// The provisioner is the one component allowed to hold create rights, and
// even it only ever instantiates the locked Palworld template
// (internal/wkagent/provisioner.go).

// pullTimeout bounds an image pull; the wkagent image is a few hundred
// MB and cached after the first provision.
const pullTimeout = 10 * time.Minute

// ImagePull pulls ref (e.g. ghcr.io/safwyls/wkagent:beta), consuming the
// progress stream until the daemon reports completion.
func (c *Client) ImagePull(ctx context.Context, ref string) error {
	name, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	ctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	path := "/images/create?fromImage=" + url.QueryEscape(name) + "&tag=" + url.QueryEscape(tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return dockerError("image pull", resp.StatusCode, body)
	}
	// The stream is JSON progress lines; an error mid-pull arrives as an
	// {"error": ...} line with a 200 status, so scan rather than discard.
	dec := json.NewDecoder(resp.Body)
	for {
		var line struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&line); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("reading pull stream: %w", err)
		}
		if line.Error != "" {
			return fmt.Errorf("pulling %s: %s", ref, line.Error)
		}
	}
}

// ContainerSpec is the subset of docker's create payload the provisioner
// template needs — nothing privileged is expressible here by design.
type ContainerSpec struct {
	Name  string
	Image string
	// User is uid:gid; empty keeps the image default.
	User string
	Env  []string
	// Binds are host:container volume pairs.
	Binds []string
	// Ports maps host port -> container "port/proto" (e.g. "8211/udp").
	Ports map[int]string
	// Labels tag the container (e.g. as wildskeeper-provisioned, for discovery).
	Labels map[string]string
	// RestartUnlessStopped applies docker's unless-stopped policy.
	RestartUnlessStopped bool
	// Networks are user-defined networks to attach at creation. Recreating
	// a container without them would silently cut it off from anything it
	// reached by service name, so InspectSpec captures them and
	// ContainerCreate puts them back.
	Networks []string
}

// ContainerCreate creates (but does not start) a container; pair with the
// existing Start.
func (c *Client) ContainerCreate(ctx context.Context, spec ContainerSpec) (string, error) {
	exposed := map[string]struct{}{}
	bindings := map[string][]map[string]string{}
	for host, cont := range spec.Ports {
		if !strings.Contains(cont, "/") {
			cont += "/tcp"
		}
		exposed[cont] = struct{}{}
		bindings[cont] = append(bindings[cont], map[string]string{"HostPort": strconv.Itoa(host)})
	}
	hostConfig := map[string]any{
		"Binds":        spec.Binds,
		"PortBindings": bindings,
	}
	if spec.RestartUnlessStopped {
		hostConfig["RestartPolicy"] = map[string]any{"Name": "unless-stopped"}
	}
	payload := map[string]any{
		"Image":        spec.Image,
		"Env":          spec.Env,
		"ExposedPorts": exposed,
		"HostConfig":   hostConfig,
	}
	if spec.User != "" {
		payload["User"] = spec.User
	}
	if len(spec.Labels) > 0 {
		payload["Labels"] = spec.Labels
	}
	if len(spec.Networks) > 0 {
		endpoints := map[string]any{}
		for _, n := range spec.Networks {
			endpoints[n] = map[string]any{}
		}
		payload["NetworkingConfig"] = map[string]any{"EndpointsConfig": endpoints}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/containers/create?name="+url.QueryEscape(spec.Name), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("docker endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return "", dockerError("container create", resp.StatusCode, respBody)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(respBody, &created); err != nil {
		return "", fmt.Errorf("parsing create response: %w", err)
	}
	return created.ID, nil
}

// ContainerRemove deletes a container.
//
// Volumes are deliberately left alone (v=0): a provisioned server's world
// lives in a host bind mount under the data root, and unmaking the
// container is not consent to delete the save. force=0 too — the caller
// stops the container first so the game gets its grace period to flush
// the world, rather than the SIGKILL a forced remove would deliver.
func (c *Client) ContainerRemove(ctx context.Context, id string) error {
	body, status, err := c.do(ctx, http.MethodDelete,
		"/containers/"+url.PathEscape(id)+"?v=0&force=0", 60*time.Second)
	if err != nil {
		return err
	}
	// 404 is the state the caller asked for, reached by someone else.
	if status == http.StatusNoContent || status == http.StatusNotFound {
		return nil
	}
	return dockerError("container remove", status, body)
}

// ContainerSummary is the discovery-relevant subset of a container.
type ContainerSummary struct {
	ID     string
	Name   string
	Image  string
	State  string // running, exited, ...
	Labels map[string]string
	// Ports maps "containerPort/proto" -> published host port (absent when
	// unpublished).
	Ports map[string]int
}

// ContainerList returns all containers (running or not).
func (c *Client) ContainerList(ctx context.Context) ([]ContainerSummary, error) {
	body, status, err := c.do(ctx, http.MethodGet, "/containers/json?all=1", 20*time.Second)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, dockerError("container list", status, body)
	}
	var raw []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Image  string            `json:"Image"`
		State  string            `json:"State"`
		Labels map[string]string `json:"Labels"`
		Ports  []struct {
			PrivatePort int    `json:"PrivatePort"`
			PublicPort  int    `json:"PublicPort"`
			Type        string `json:"Type"`
		} `json:"Ports"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parsing container list: %w", err)
	}
	out := make([]ContainerSummary, 0, len(raw))
	for _, r := range raw {
		name := ""
		if len(r.Names) > 0 {
			name = strings.TrimPrefix(r.Names[0], "/")
		}
		ports := map[string]int{}
		for _, p := range r.Ports {
			if p.PublicPort != 0 {
				ports[fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)] = p.PublicPort
			}
		}
		out = append(out, ContainerSummary{ID: r.ID, Name: name, Image: r.Image, State: r.State, Labels: r.Labels, Ports: ports})
	}
	return out, nil
}

// InspectEnv returns a container's environment. Discovery callers must
// filter it before letting values leave the trust boundary — it carries
// tokens and passwords.
func (c *Client) InspectEnv(ctx context.Context, id string) ([]string, error) {
	body, status, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", 15*time.Second)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, dockerError("inspect", status, body)
	}
	var payload struct {
		Config struct {
			Env []string `json:"Env"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parsing inspect response: %w", err)
	}
	return payload.Config.Env, nil
}

// InspectSpec reads a container's configuration back into the shape that
// created it, so a caller can recreate it with one field changed.
//
// This is how an image is swapped under a container that no orchestrator
// owns: docker has no "change the image" operation, only create, so the
// only faithful path is to read everything back and build it again. What
// is captured is what a provisioned agent depends on — image, user,
// environment, bind mounts, published ports, labels, restart policy and
// networks. Anything outside that set is not reproduced, which is why this
// is used for containers this provisioner created rather than arbitrary
// ones.
func (c *Client) InspectSpec(ctx context.Context, container string) (*ContainerSpec, error) {
	body, status, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(container)+"/json", 15*time.Second)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, dockerError("inspect", status, body)
	}
	var payload struct {
		Name   string `json:"Name"`
		Config struct {
			Image  string            `json:"Image"`
			User   string            `json:"User"`
			Env    []string          `json:"Env"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		HostConfig struct {
			Binds         []string `json:"Binds"`
			RestartPolicy struct {
				Name string `json:"Name"`
			} `json:"RestartPolicy"`
			PortBindings map[string][]struct {
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
		} `json:"HostConfig"`
		NetworkSettings struct {
			Networks map[string]struct{} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parsing inspect response: %w", err)
	}

	spec := &ContainerSpec{
		Name:                 strings.TrimPrefix(payload.Name, "/"),
		Image:                payload.Config.Image,
		User:                 payload.Config.User,
		Env:                  payload.Config.Env,
		Binds:                payload.HostConfig.Binds,
		Labels:               payload.Config.Labels,
		RestartUnlessStopped: payload.HostConfig.RestartPolicy.Name == "unless-stopped",
		Ports:                map[int]string{},
	}
	for containerPort, hosts := range payload.HostConfig.PortBindings {
		for _, h := range hosts {
			hostPort, err := strconv.Atoi(h.HostPort)
			if err != nil {
				// An unbound or dynamically-assigned publish can't be
				// reproduced faithfully; skipping it is better than
				// inventing a port the operator never chose.
				continue
			}
			spec.Ports[hostPort] = containerPort
		}
	}
	for name := range payload.NetworkSettings.Networks {
		// The default bridge is what a container gets with no networks
		// declared, so re-declaring it would be both redundant and, for
		// "bridge" specifically, rejected as a user-defined network.
		if name == "bridge" {
			continue
		}
		spec.Networks = append(spec.Networks, name)
	}
	sort.Strings(spec.Networks)
	return spec, nil
}
