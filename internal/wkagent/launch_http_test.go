package wkagent_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/safwyls/wildskeeper/internal/wkagent"
)

// authed issues a request with the agent's bearer token.
func authed(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

type launchStatus struct {
	Profile         string   `json:"profile"`
	Mods            bool     `json:"mods"`
	Installed       bool     `json:"installed"`
	Available       []string `json:"available"`
	PendingRestart  bool     `json:"pendingRestart"`
	ConfigPath      string   `json:"configPath"`
	BridgeKit       bool     `json:"bridgeKit"`
	BridgeInstalled bool     `json:"bridgeInstalled"`
}

func healthLaunch(t *testing.T, srv string) launchStatus {
	t.Helper()
	_, body := authed(t, http.MethodGet, srv+"/v1/health", nil)
	var h struct {
		Launch launchStatus `json:"launch"`
	}
	if err := json.Unmarshal(body, &h); err != nil {
		t.Fatalf("health: %v (%s)", err, body)
	}
	return h.Launch
}

// The console has to be able to see, and change, which build runs — and the
// answer has to survive the agent being recreated, or a redeploy would
// silently put a modded server back on the vanilla build.
func TestLaunchProfileIsSelectableAndPersists(t *testing.T) {
	srv, _, install := newSupervisorAgent(t, steadyGame)

	if got := healthLaunch(t, srv.URL); got.Profile != wkagent.ProfileNative {
		t.Fatalf("profile = %q, want native by default", got.Profile)
	}

	resp, body := authed(t, http.MethodPut, srv.URL+"/v1/launch", map[string]string{"profile": wkagent.ProfileWine})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("selecting wine: %d %s", resp.StatusCode, body)
	}

	got := healthLaunch(t, srv.URL)
	if got.Profile != wkagent.ProfileWine {
		t.Errorf("profile = %q, want wine", got.Profile)
	}
	if !got.Mods {
		t.Error("the wine profile is the one that carries the mod")
	}
	// The Windows build is a different depot, so an install that only has
	// the Linux files must report itself as not installed rather than
	// looking ready and failing at exec.
	if got.Installed {
		t.Error("wine reported installed on a native-only install dir")
	}
	if !strings.Contains(got.ConfigPath, "WindowsServer") {
		t.Errorf("config path = %q; the ini editor would edit a file the game never reads", got.ConfigPath)
	}

	// A recreated agent over the same volume — a redeploy — keeps the choice.
	second, err := wkagent.New(wkagent.Config{
		Token: testToken, InstallDir: install, SteamCmd: "steamcmd", Version: "test",
		Mode: "supervisor", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv2 := httptest.NewServer(second.Handler())
	t.Cleanup(srv2.Close)
	if got := healthLaunch(t, srv2.URL); got.Profile != wkagent.ProfileWine {
		t.Errorf("a recreated agent forgot the selection: %q", got.Profile)
	}
}

func TestUnknownLaunchProfileIsRefused(t *testing.T) {
	srv, _, _ := newSupervisorAgent(t, steadyGame)
	resp, _ := authed(t, http.MethodPut, srv.URL+"/v1/launch", map[string]string{"profile": "proton"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown profile", resp.StatusCode)
	}
}

// Selecting a profile must not disturb a running game — the switch needs a
// re-install, so bringing the server down mid-"settings change" would be
// the worst possible surprise. It is reported as pending instead.
func TestSelectingAProfileLeavesTheRunningGameAloneAndReportsPending(t *testing.T) {
	srv, _, _ := newSupervisorAgent(t, steadyGame)

	if resp, body := authed(t, http.MethodPost, srv.URL+"/v1/power/start", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("start: %d %s", resp.StatusCode, body)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if healthLaunch(t, srv.URL).Profile != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	authed(t, http.MethodPut, srv.URL+"/v1/launch", map[string]string{"profile": wkagent.ProfileWine})

	got := healthLaunch(t, srv.URL)
	if !got.PendingRestart {
		t.Error("a selection made while running should report as pending, not as in effect")
	}
	// The game itself is untouched.
	_, body := authed(t, http.MethodGet, srv.URL+"/v1/health", nil)
	var h struct {
		Game struct {
			State string `json:"state"`
		} `json:"game"`
	}
	json.Unmarshal(body, &h)
	if h.Game.State != "running" {
		t.Errorf("game state = %q; selecting a profile stopped the server", h.Game.State)
	}
}

// The launch path end to end, without needing real Wine: a stub on PATH
// stands in for the wine binary and records what it was handed. This is the
// only place the profile's environment is proven to actually reach the
// process — every piece of it fails silently in production if it doesn't.
func TestWineProfileLaunchesThroughPathWithTheModEnvironment(t *testing.T) {
	srv, _, install := newSupervisorAgent(t, steadyGame)

	// A stub "wine" that dumps its arguments and the variables the mod
	// stack depends on, then behaves like the game.
	binDir := t.TempDir()
	// printf, not echo: sh's echo expands backslash escapes, and the whole
	// point of DWBRIDGE_DIR is that it is a backslash-separated Windows
	// path — echo would turn the \t of Z:\tmp into a tab and the assertion
	// would fail on the stub's mangling rather than on anything real.
	stub := "#!/bin/sh\n" +
		"{ printf '%s\\n' \"argv: $*\" \"WINEDLLOVERRIDES=$WINEDLLOVERRIDES\" \"DWBRIDGE_DIR=$DWBRIDGE_DIR\" \"PWD=$(pwd)\"; } > " +
		filepath.Join(binDir, "launched") + "\n" +
		"trap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"
	if err := os.WriteFile(filepath.Join(binDir, "wine"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The Windows build's files, so the profile counts as installed.
	exe := filepath.Join(install, "RSDragonwilds", "Binaries", "Win64", "RSDragonwildsServer-Win64-Shipping.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}

	authed(t, http.MethodPut, srv.URL+"/v1/launch", map[string]string{"profile": wkagent.ProfileWine})
	if got := healthLaunch(t, srv.URL); !got.Installed {
		t.Fatal("the wine profile should be installed once the exe exists")
	}
	if resp, body := authed(t, http.MethodPost, srv.URL+"/v1/power/start", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("start under wine: %d %s", resp.StatusCode, body)
	}

	var out []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		// The stub's redirect creates the file before printf fills it, so
		// existence alone can hand back a torn read — wait for the last
		// line it writes.
		if data, err := os.ReadFile(filepath.Join(binDir, "launched")); err == nil && strings.Contains(string(data), "PWD=") {
			out = data
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if out == nil {
		t.Fatal("the wine stub was never executed — the profile's command did not reach exec")
	}
	got := string(out)
	if !strings.Contains(got, "RSDragonwildsServer-Win64-Shipping.exe") {
		t.Errorf("wine was not handed the server exe: %s", got)
	}
	if !strings.Contains(got, "-Port=") {
		t.Errorf("the port never reached the game: %s", got)
	}
	if !strings.Contains(got, "WINEDLLOVERRIDES=version=n,b") {
		t.Errorf("the version.dll override is missing, so UE4SS would never inject: %s", got)
	}
	if !strings.Contains(got, `DWBRIDGE_DIR=Z:\`) {
		t.Errorf("the mod would not find the bridge directory: %s", got)
	}
	// The rendezvous directory must exist by the time the game is up:
	// neither the mod nor the bridge reader creates it, so a start on a
	// modded profile that skips the mkdir leaves the mod heartbeating
	// into the void (first real Wine deployment found exactly this).
	if fi, err := os.Stat(filepath.Join(install, "dwbridge")); err != nil || !fi.IsDir() {
		t.Errorf("starting the wine profile did not create the bridge directory: %v", err)
	}
}

// Switching build must not cost the operator their settings or their world.
// The two are different: the world is shared by both builds (saves live in
// Saved/SaveGames, which is not platform-suffixed), but the config
// directory *is* per platform, so the settings have to be carried across
// deliberately.
func TestSwitchingBuildCarriesSettingsAndLeavesTheWorldAlone(t *testing.T) {
	srv, _, install := newSupervisorAgent(t, steadyGame)

	linuxIni := filepath.Join(install, "RSDragonwilds", "Saved", "Config", "LinuxServer", "DedicatedServer.ini")
	if err := os.MkdirAll(filepath.Dir(linuxIni), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := "[/Script/Dominion.DedicatedServerSettings]\nServerName=Ashenfall\nOwnerId=abc\n"
	if err := os.WriteFile(linuxIni, []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	// A world, to prove it is untouched by the switch.
	save := filepath.Join(install, "RSDragonwilds", "Saved", "SaveGames", "Ashenfall.sav")
	if err := os.MkdirAll(filepath.Dir(save), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(save, []byte("SAVE-world-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	authed(t, http.MethodPut, srv.URL+"/v1/launch", map[string]string{"profile": wkagent.ProfileWine})

	windowsIni := filepath.Join(install, "RSDragonwilds", "Saved", "Config", "WindowsServer", "DedicatedServer.ini")
	got, err := os.ReadFile(windowsIni)
	if err != nil {
		t.Fatalf("settings were not carried to the Windows build: %v", err)
	}
	if string(got) != settings {
		t.Errorf("carried settings = %q, want them intact", got)
	}
	// Saves are shared, so switching must not have touched the world.
	if world, err := os.ReadFile(save); err != nil || string(world) != "SAVE-world-bytes" {
		t.Errorf("the world changed across a build switch: %v %q", err, world)
	}

	// And the settings editor now serves the file the game will actually
	// read — otherwise edits would appear to save and change nothing.
	resp, body := authed(t, http.MethodGet, srv.URL+"/v1/files/config", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("config after switch: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Ashenfall") {
		t.Errorf("the config verb served something unexpected: %s", body)
	}

	// Switching back must not clobber the config that is already there.
	if err := os.WriteFile(windowsIni, []byte(settings+"Edited=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	authed(t, http.MethodPut, srv.URL+"/v1/launch", map[string]string{"profile": wkagent.ProfileNative})
	authed(t, http.MethodPut, srv.URL+"/v1/launch", map[string]string{"profile": wkagent.ProfileWine})
	if back, _ := os.ReadFile(windowsIni); !strings.Contains(string(back), "Edited=1") {
		t.Error("switching back overwrote a config that was already there")
	}
}
