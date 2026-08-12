package wkagent

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// The game-agnostic provisioning contract.
//
// Placing a container on a host is not a game-specific act. What differs
// between one game console and another is only *data*: which image the
// sidecar runs, what environment configures it, which ports it publishes.
// Everything else — naming, the data directory under the data root, the
// ownership labels, the restart policy, the create/start ordering — is the
// same work regardless of what is being run.
//
// So a ProvisionSpec carries the data and the provisioner does the work.
// The console owns its game's knowledge and sends it; the provisioner never
// learns what a "world name" or an "owner id" is. That is what lets one
// host service serve several consoles without growing a case statement per
// game, and what keeps a game's changes from becoming a host-service
// deploy.
//
// # Why this is not "run any container for me"
//
// A generic spec is one careless step away from a remote-code-execution
// primitive: whoever holds the provisioner's token could otherwise name any
// image and any host path and have it run as a container on the NAS. Two
// deliberate constraints stop that, and they are the reason this type has
// the shape it does:
//
//   - Images are checked against an allowlist of prefixes. The default is
//     the project's own registry namespace, so a leaked token can deploy a
//     newer agent, not an arbitrary payload.
//   - Host paths are not caller-controlled at all. The caller names a slug;
//     the provisioner decides the host directory beneath its configured
//     data root. There is no field here for a bind mount, on purpose.
//
// Anything that widens either of those should be treated as a change to the
// host's security posture, not a feature.

// PortMap publishes one container port on the host.
type PortMap struct {
	Host      int    `json:"host"`
	Container int    `json:"container"`
	// Proto is tcp or udp; empty means tcp.
	Proto string `json:"proto,omitempty"`
}

// ProvisionSpec is a request to place one server container on this host.
type ProvisionSpec struct {
	// Name is the container name, and must be unique on the host.
	Name string `json:"name"`
	// Slug names the data directory under the provisioner's data root. It
	// is not a path: the provisioner joins it, and rejects anything that
	// could escape.
	Slug string `json:"slug"`
	// Image is the full image reference, subject to the allowlist.
	Image string `json:"image"`
	// User is uid:gid for the container; empty keeps the image default.
	User string `json:"user,omitempty"`
	// Env configures the sidecar. Entirely the console's business — this is
	// where a game's own settings ride.
	Env map[string]string `json:"env,omitempty"`
	// Ports are the host publishes.
	Ports []PortMap `json:"ports,omitempty"`
	// DataMount is where the data directory appears inside the container.
	// Absolute, and normalised; the host side is never the caller's choice.
	DataMount string `json:"dataMount,omitempty"`
	// Labels are merged over the provisioner's own ownership labels, which
	// always win — a caller must not be able to disguise a container as
	// something this provisioner did not create, or to forge one it did.
	Labels map[string]string `json:"labels,omitempty"`
}

var (
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	envKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// DefaultImagePrefixes is the allowlist when none is configured: this
// project's own registry namespace. Deliberately narrow — see the type
// comment.
var DefaultImagePrefixes = []string{"ghcr.io/safwyls/"}

// Validate checks a spec against everything the provisioner will not take
// on trust. allowedPrefixes constrains the image; an empty list means
// DefaultImagePrefixes.
func (s *ProvisionSpec) Validate(allowedPrefixes []string) error {
	if !namePattern.MatchString(s.Name) {
		return errors.New("container name must be 1-64 chars of letters, digits, dot, dash or underscore")
	}
	if !slugPattern.MatchString(s.Slug) {
		return errors.New("slug must be 1-64 lowercase letters, digits or dashes")
	}
	// Belt and braces over the pattern: the slug becomes a directory under
	// the data root, and nothing that could climb out of it may pass.
	if s.Slug != path.Base(path.Clean("/"+s.Slug)) {
		return errors.New("slug must not contain path separators")
	}
	if err := checkImage(s.Image, allowedPrefixes); err != nil {
		return err
	}
	if s.DataMount != "" && (!path.IsAbs(s.DataMount) || s.DataMount != path.Clean(s.DataMount)) {
		return errors.New("data mount must be an absolute, already-clean path")
	}
	for k := range s.Env {
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("invalid environment variable name %q", k)
		}
	}
	seen := map[int]bool{}
	for _, p := range s.Ports {
		if p.Host < 1 || p.Host > 65535 || p.Container < 1 || p.Container > 65535 {
			return errors.New("ports must be in 1-65535")
		}
		if seen[p.Host] {
			return fmt.Errorf("host port %d is published twice", p.Host)
		}
		seen[p.Host] = true
		switch p.Proto {
		case "", "tcp", "udp":
		default:
			return fmt.Errorf("unknown protocol %q", p.Proto)
		}
	}
	return nil
}

// checkImage enforces the allowlist. The point is not to validate a
// reference's syntax — docker will do that — but to bound what this host
// can be told to run.
func checkImage(image string, allowed []string) error {
	if image == "" {
		return errors.New("image is required")
	}
	if strings.ContainsAny(image, " \t\n") {
		return errors.New("image reference must not contain whitespace")
	}
	if len(allowed) == 0 {
		allowed = DefaultImagePrefixes
	}
	for _, prefix := range allowed {
		if strings.HasPrefix(image, prefix) {
			return nil
		}
	}
	return fmt.Errorf("image %q is not in this provisioner's allowlist (%s)", image, strings.Join(allowed, ", "))
}
