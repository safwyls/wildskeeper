package wkagent_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/safwyls/wildskeeper/internal/wkagent"
)

// fakeDockerAPI records the provisioner's docker calls.
type fakeDockerAPI struct {
	calls  []string
	create map[string]any
}

func (f *fakeDockerAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/images/create":
			w.Write([]byte(`{"status":"done"}` + "\n"))
		case r.URL.Path == "/containers/create":
			json.NewDecoder(r.Body).Decode(&f.create)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"Id":"deadbeef"}`))
		case r.URL.Path == "/containers/json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[
			  {"Id":"c1","Names":["/wkagent-main"],"Image":"ghcr.io/safwyls/wkagent:beta","State":"running",
			   "Labels":{"wildskeeper.provisioned":"true","wildskeeper.slug":"main"},
			   "Ports":[{"PrivatePort":7777,"PublicPort":9211,"Type":"udp"},{"PrivatePort":8811,"PublicPort":9811,"Type":"tcp"}]},
			  {"Id":"c2","Names":["/wkprovisioner"],"Image":"ghcr.io/safwyls/wkagent:beta","State":"running","Ports":[]},
			  {"Id":"c3","Names":["/nginx"],"Image":"nginx:latest","State":"running","Ports":[]}
			]`))
		case r.URL.Path == "/containers/c1/json":
			w.Write([]byte(`{"Config":{"Env":["WKAGENT_MODE=supervisor","WKAGENT_TOKEN=secret-must-not-leak","WKAGENT_ADMIN_PASSWORD=recovered-pw","WKAGENT_SERVER_NAME=Main World"]}}`))
		case r.URL.Path == "/containers/c2/json":
			w.Write([]byte(`{"Config":{"Env":["WKAGENT_MODE=provisioner"]}}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func newProvisioner(t *testing.T) (*httptest.Server, *fakeDockerAPI, string) {
	t.Helper()
	fake := &fakeDockerAPI{}
	dockerSrv := httptest.NewServer(fake.handler())
	t.Cleanup(dockerSrv.Close)
	dataRoot := t.TempDir()
	agent, err := wkagent.New(wkagent.Config{
		Token: testToken, InstallDir: t.TempDir(), Version: "test",
		Mode: "provisioner", DockerHost: dockerSrv.URL, DataRoot: dataRoot,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(agent.Handler())
	t.Cleanup(srv.Close)
	return srv, fake, dataRoot
}

func TestProvisionerCreatesServer(t *testing.T) {
	srv, fake, dataRoot := newProvisioner(t)

	resp, m := do(t, srv, "POST", "/v1/provision", testToken, map[string]any{
		"slug": "palhalla-2", "imageTag": "beta",
		"token": "new-agent-token-0123456789abcdef", "adminPassword": "pw12345", "ownerId": "owner-abc",
		"serverName": "Palhalla II", "serverDesc": "chill server", "runAs": "568:568",
		"gamePort": 9211, "restPort": 9212, "rconPort": 9575, "agentPort": 9811,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("provision: %d %v", resp.StatusCode, m)
	}
	if m["container"] != "wkagent-palhalla-2" || m["dataDir"] != filepath.Join(dataRoot, "palhalla-2") {
		t.Errorf("response = %v", m)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "palhalla-2")); err != nil {
		t.Errorf("data dir not created: %v", err)
	}

	// pull → create → start, template locked to the wkagent image.
	joined := strings.Join(fake.calls, " | ")
	for _, want := range []string{"/images/create", "/containers/create", "/start"} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker calls missing %s: %s", want, joined)
		}
	}
	if fake.create["Image"] != "ghcr.io/safwyls/wkagent:beta" || fake.create["User"] != "568:568" {
		t.Errorf("create = image %v user %v", fake.create["Image"], fake.create["User"])
	}
	env := strings.Join(toStrings(fake.create["Env"].([]any)), " ")
	for _, want := range []string{"WKAGENT_MODE=supervisor", "WKAGENT_TOKEN=new-agent-token", "WKAGENT_ADMIN_PASSWORD=pw12345", "WKAGENT_SERVER_NAME=Palhalla II", "HOME=/tmp"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %s: %s", want, env)
		}
	}
}

// A slug whose container already exists is refused before the mkdir and
// the image pull, and with 409 — the status wildskeeper reads as "nothing was
// made", which is what keeps it from registering the server anyway.
func TestProvisionerRefusesNameInUse(t *testing.T) {
	srv, fake, dataRoot := newProvisioner(t)

	resp, m := do(t, srv, "POST", "/v1/provision", testToken, map[string]any{
		"slug": "main", "token": "new-agent-token-0123456789abcdef", "adminPassword": "pw12345", "ownerId": "owner-abc",
		"gamePort": 9211, "restPort": 9212, "rconPort": 9575, "agentPort": 9811,
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("provision onto an existing name: %d %v, want 409", resp.StatusCode, m)
	}
	if msg, _ := m["error"].(string); !strings.Contains(msg, "wkagent-main") {
		t.Errorf("error should name the container in the way: %v", m)
	}
	joined := strings.Join(fake.calls, " | ")
	if strings.Contains(joined, "/images/create") || strings.Contains(joined, "/containers/create") {
		t.Errorf("pulled or created despite the conflict: %s", joined)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "main")); !os.IsNotExist(err) {
		t.Errorf("data dir created for a refused provision (err %v)", err)
	}
}

func toStrings(in []any) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = v.(string)
	}
	return out
}

func TestProvisionerHealthDefaultsAndDiscover(t *testing.T) {
	srv, _, dataRoot := newProvisioner(t)

	// Health carries the wizard defaults from the provisioner's config.
	_, health := do(t, srv, "GET", "/v1/health", testToken, nil)
	prov, _ := health["provision"].(map[string]any)
	if prov == nil || prov["dataRoot"] != dataRoot || prov["runAs"] != "568:568" || prov["imageTag"] != "latest" {
		t.Fatalf("health provision block = %v", prov)
	}

	// Discover: the supervisor shows with its ports; the provisioner
	// itself and unrelated containers don't; env never leaks.
	resp, m := do(t, srv, "GET", "/v1/discover", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discover: %d %v", resp.StatusCode, m)
	}
	servers := m["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("discovered = %v, want exactly the supervisor", servers)
	}
	got := servers[0].(map[string]any)
	if got["name"] != "wkagent-main" || got["mode"] != "supervisor" || got["running"] != true ||
		got["gamePort"] != float64(9211) || got["agentPort"] != float64(9811) {
		t.Errorf("candidate = %v", got)
	}
	if strings.Contains(fmt.Sprint(m), "secret-must-not-leak") {
		t.Fatal("discovery leaked container env")
	}
}

// Adoption is the one deliberate secret-return path: the provisioner
// injected these values, so handing them back to the authenticated
// control plane recovers a lost registration. Restricted to wkagent
// containers.
func TestProvisionerAdopt(t *testing.T) {
	srv, _, _ := newProvisioner(t)

	resp, m := do(t, srv, "POST", "/v1/adopt", testToken, map[string]string{"container": "wkagent-main"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("adopt: %d %v", resp.StatusCode, m)
	}
	if m["token"] != "secret-must-not-leak" || m["adminPassword"] != "recovered-pw" ||
		m["serverName"] != "Main World" || m["mode"] != "supervisor" || m["agentPort"] != float64(9811) {
		t.Errorf("adopt result = %v", m)
	}

	// Not-a-wkagent and unknown containers refuse.
	if resp, _ := do(t, srv, "POST", "/v1/adopt", testToken, map[string]string{"container": "nginx"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("nginx adopt: %d, want 400", resp.StatusCode)
	}
	if resp, _ := do(t, srv, "POST", "/v1/adopt", testToken, map[string]string{"container": "ghost"}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("ghost adopt: %d, want 404", resp.StatusCode)
	}
	// The provisioner itself is not adoptable.
	if resp, _ := do(t, srv, "POST", "/v1/adopt", testToken, map[string]string{"container": "wkprovisioner"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("provisioner adopt: %d, want 400", resp.StatusCode)
	}
}

// Destroy stops before removing (so the world is flushed), keeps the
// volume, and reports where the data still is.
func TestProvisionerDestroy(t *testing.T) {
	srv, fake, dataRoot := newProvisioner(t)

	resp, m := do(t, srv, "POST", "/v1/destroy", testToken, map[string]string{"container": "wkagent-main"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("destroy: %d %v", resp.StatusCode, m)
	}
	if m["container"] != "wkagent-main" || m["dataDir"] != filepath.Join(dataRoot, "main") {
		t.Errorf("destroy result = %v", m)
	}

	joined := strings.Join(fake.calls, " | ")
	stop, remove := strings.Index(joined, "POST /containers/c1/stop"), strings.Index(joined, "DELETE /containers/c1")
	if stop < 0 || remove < 0 {
		t.Fatalf("want a stop then a remove, got: %s", joined)
	}
	if stop > remove {
		t.Errorf("removed before stopping — the world never got flushed: %s", joined)
	}
}

// The label gate, which is the whole security argument for this verb:
// only containers this provisioner created can be destroyed. A wkagent
// deployed by hand carries the image but not the label, and is refused —
// including the provisioner itself.
func TestProvisionerDestroyRefusesUnlabelled(t *testing.T) {
	srv, fake, _ := newProvisioner(t)

	for _, name := range []string{"wkprovisioner", "nginx"} {
		resp, m := do(t, srv, "POST", "/v1/destroy", testToken, map[string]string{"container": name})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("destroy %s: %d %v, want 400", name, resp.StatusCode, m)
		}
	}
	if resp, _ := do(t, srv, "POST", "/v1/destroy", testToken, map[string]string{"container": "ghost"}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("destroy ghost: %d, want 404", resp.StatusCode)
	}
	if resp, _ := do(t, srv, "POST", "/v1/destroy", testToken, map[string]string{"container": ""}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("destroy without a name: %d, want 400", resp.StatusCode)
	}

	// Nothing was stopped or removed on any refused path.
	if joined := strings.Join(fake.calls, " | "); strings.Contains(joined, "DELETE /containers") || strings.Contains(joined, "/stop") {
		t.Errorf("a refused destroy still touched docker: %s", joined)
	}
}

func TestNonProvisionerRefusesDestroy(t *testing.T) {
	srv, _ := newTestAgent(t, "exit 0") // companion
	if resp, _ := do(t, srv, "POST", "/v1/destroy", testToken, map[string]string{"container": "x"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("companion destroy: %d, want 400", resp.StatusCode)
	}
}

func TestProvisionerValidation(t *testing.T) {
	srv, _, _ := newProvisioner(t)
	const tok = "long-enough-token-123456"
	cases := []map[string]any{
		{"slug": "../evil", "token": tok, "adminPassword": "x", "ownerId": "o", "gamePort": 1, "agentPort": 4},
		{"slug": "ok", "token": "short", "adminPassword": "x", "ownerId": "o", "gamePort": 1, "agentPort": 4},
		{"slug": "ok", "token": tok, "adminPassword": "", "ownerId": "o", "gamePort": 1, "agentPort": 4},
		// The game will not start without an owner id, so a deploy that
		// omits one can only ever produce a broken container.
		{"slug": "ok", "token": tok, "adminPassword": "x", "gamePort": 1, "agentPort": 4},
		{"slug": "ok", "token": tok, "adminPassword": "x", "ownerId": "  ", "gamePort": 1, "agentPort": 4},
		{"slug": "ok", "token": tok, "adminPassword": "x", "ownerId": "o", "runAs": "steam", "gamePort": 1, "agentPort": 4},
		// The game binds gamePort and gamePort+1, so 65535 leaves no room
		// and an agent port inside the pair collides.
		{"slug": "ok", "token": tok, "adminPassword": "x", "ownerId": "o", "gamePort": 65535, "agentPort": 4},
		{"slug": "ok", "token": tok, "adminPassword": "x", "ownerId": "o", "gamePort": 5, "agentPort": 6},
		{"slug": "ok", "token": tok, "adminPassword": "x", "ownerId": "o", "imageTag": "beta@sha256:junk", "gamePort": 1, "agentPort": 4},
	}
	for i, body := range cases {
		if resp, m := do(t, srv, "POST", "/v1/provision", testToken, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("case %d: got %d %v, want 400", i, resp.StatusCode, m)
		}
	}
}

func TestNonProvisionerRefusesProvision(t *testing.T) {
	srv, _ := newTestAgent(t, "exit 0") // companion
	if resp, _ := do(t, srv, "POST", "/v1/provision", testToken, map[string]any{"slug": "x"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("companion provision: %d, want 400", resp.StatusCode)
	}
}

// The whole point of the contract: a console for a *different* game can
// place its server here, and this provisioner — which only knows
// Dragonwilds — carries it without understanding a single field.
//
// The spec below is Palworld-shaped on purpose: four distinct ports
// including RCON and REST, a server description, no owner id. If this
// provisioner had to know what any of that meant, the boundary would be
// fake and the extraction into a host service would not work.
func TestProvisionSpecPlacesAnotherGamesServer(t *testing.T) {
	srv, fake, dataRoot := newProvisioner(t)

	spec := map[string]any{
		"name":  "palagent-palhalla",
		"slug":  "palhalla",
		"image": "ghcr.io/safwyls/palagent:latest",
		"user":  "568:568",
		"env": map[string]string{
			"PALAGENT_MODE":        "supervisor",
			"PALAGENT_SERVER_DESC": "a description this provisioner never parses",
		},
		"ports": []map[string]any{
			{"host": 8211, "container": 8211, "proto": "udp"},
			{"host": 8212, "container": 8212, "proto": "tcp"},
			{"host": 25575, "container": 25575, "proto": "tcp"},
			{"host": 8811, "container": 8811, "proto": "tcp"},
		},
		"dataMount": "/palworld",
	}
	resp, m := do(t, srv, "POST", "/v1/provision/spec", testToken, spec)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("placing another game's server: %d %v", resp.StatusCode, m)
	}

	// The data directory is the provisioner's decision, not the caller's.
	if _, err := os.Stat(filepath.Join(dataRoot, "palhalla")); err != nil {
		t.Errorf("data dir was not created under the data root: %v", err)
	}

	created := fake.create
	if created["Image"] != "ghcr.io/safwyls/palagent:latest" {
		t.Errorf("image = %v", created["Image"])
	}
	env := fmt.Sprint(created["Env"])
	if !strings.Contains(env, "PALAGENT_SERVER_DESC=") {
		t.Errorf("the console's own env did not survive: %v", env)
	}
	// Ownership labels are the provisioner's, always — they are what every
	// later destroy and recreate checks.
	labels, _ := created["Labels"].(map[string]any)
	if labels["wildskeeper.provisioned"] != "true" || labels["wildskeeper.slug"] != "palhalla" {
		t.Errorf("ownership labels missing or wrong: %v", labels)
	}
	host, _ := created["HostConfig"].(map[string]any)
	binds := fmt.Sprint(host["Binds"])
	if !strings.Contains(binds, ":/palworld") {
		t.Errorf("the data mount the caller asked for was not used: %v", binds)
	}
}

// A caller must not be able to turn the provisioner into a way to run
// anything it likes on the host. This is the property that makes a generic
// spec endpoint safe enough to exist.
func TestProvisionSpecRefusesImagesOutsideTheAllowlist(t *testing.T) {
	srv, fake, _ := newProvisioner(t)

	resp, m := do(t, srv, "POST", "/v1/provision/spec", testToken, map[string]any{
		"name": "evil", "slug": "evil", "image": "docker.io/library/alpine:latest",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an image outside the allowlist: %v", resp.StatusCode, m)
	}
	if !strings.Contains(fmt.Sprint(m["error"]), "allowlist") {
		t.Errorf("the refusal should say why: %v", m)
	}
	if fake.create != nil {
		t.Error("a refused image still reached docker create")
	}
}

// The slug becomes a directory under the data root, so it must never be
// able to climb out of it.
func TestProvisionSpecRefusesSlugTraversal(t *testing.T) {
	srv, _, _ := newProvisioner(t)
	for _, slug := range []string{"../etc", "a/b", "/abs", ".."} {
		resp, _ := do(t, srv, "POST", "/v1/provision/spec", testToken, map[string]any{
			"name": "x", "slug": slug, "image": "ghcr.io/safwyls/wkagent:latest",
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("slug %q: status = %d, want 400", slug, resp.StatusCode)
		}
	}
}

// A caller must not be able to forge the labels the ownership gates read.
func TestProvisionSpecCannotForgeOwnershipLabels(t *testing.T) {
	srv, fake, _ := newProvisioner(t)

	do(t, srv, "POST", "/v1/provision/spec", testToken, map[string]any{
		"name": "wkagent-x", "slug": "x", "image": "ghcr.io/safwyls/wkagent:latest",
		"labels": map[string]string{"wildskeeper.slug": "someone-elses", "mine": "ok"},
	})
	labels, _ := fake.create["Labels"].(map[string]any)
	if labels["wildskeeper.slug"] != "x" {
		t.Errorf("a caller overwrote an ownership label: %v", labels)
	}
	if labels["mine"] != "ok" {
		t.Errorf("the caller's own labels should still be kept: %v", labels)
	}
}
