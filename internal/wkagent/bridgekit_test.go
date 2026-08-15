package wkagent_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/safwyls/wildskeeper/internal/wkagent"
)

// newKitAgent is newSupervisorAgent with a bridge kit baked in, the way the
// Wine image ships one.
func newKitAgent(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	install := t.TempDir()
	writeGame(t, install, steadyGame)
	kit := t.TempDir()
	for _, f := range []string{"version.dll", "ue4ss/UE4SS.dll", "ue4ss/Mods/dwbridge/Scripts/main.lua", "ue4ss/Mods/mods.txt"} {
		p := filepath.Join(kit, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	steamcmd := filepath.Join(t.TempDir(), "steamcmd")
	if err := os.WriteFile(steamcmd, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	agent, err := wkagent.New(wkagent.Config{
		Token: testToken, InstallDir: install, SteamCmd: steamcmd, Version: "test",
		Mode:         "supervisor",
		BridgeKitDir: kit,
		StopGrace:    500 * time.Millisecond,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(agent.Handler())
	t.Cleanup(srv.Close)
	return srv, install, kit
}

// The whole point of the verb: from "the Windows build is installed" to "the
// mod stack sits next to the exe" in one authenticated call — with the
// preconditions and the never-overwrite rule enforced by the agent, since
// the console builds its button purely from what the agent reports.
func TestBridgeInstallLaysTheKitNextToTheExe(t *testing.T) {
	srv, install, _ := newKitAgent(t)

	// The Windows build's exe, so the wine profile counts as installed.
	exe := filepath.Join(install, "RSDragonwilds", "Binaries", "Win64", "RSDragonwildsServer-Win64-Shipping.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}

	// On the native profile the verb must refuse: the Linux build can never
	// load what it would install.
	if resp, body := authed(t, http.MethodPost, srv.URL+"/v1/bridge/install", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("install on the native profile: got %d %s, want 400", resp.StatusCode, body)
	}

	authed(t, http.MethodPut, srv.URL+"/v1/launch", map[string]string{"profile": wkagent.ProfileWine})
	if got := healthLaunch(t, srv.URL); !got.BridgeKit || got.BridgeInstalled {
		t.Fatalf("before install: bridgeKit=%v bridgeInstalled=%v, want true/false", got.BridgeKit, got.BridgeInstalled)
	}

	if resp, body := authed(t, http.MethodPost, srv.URL+"/v1/bridge/install", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("install: %d %s", resp.StatusCode, body)
	}
	for _, f := range []string{"version.dll", "ue4ss/UE4SS.dll", "ue4ss/Mods/dwbridge/Scripts/main.lua"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe), filepath.FromSlash(f))); err != nil {
			t.Errorf("kit file %s did not land next to the exe: %v", f, err)
		}
	}
	// The file-IPC rendezvous exists immediately, not only at next start.
	if fi, err := os.Stat(filepath.Join(install, "dwbridge")); err != nil || !fi.IsDir() {
		t.Errorf("install did not create the bridge directory: %v", err)
	}
	if got := healthLaunch(t, srv.URL); !got.BridgeInstalled {
		t.Error("after install the launch status should report bridgeInstalled")
	}

	// Whatever sits there now is the operator's: a second install must
	// refuse rather than overwrite.
	if resp, body := authed(t, http.MethodPost, srv.URL+"/v1/bridge/install", nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("re-install: got %d %s, want 409", resp.StatusCode, body)
	}
}

// The plain image carries no kit, and the honest answer is "this image can't",
// not a copy error halfway through.
func TestBridgeInstallWithoutAKitAnswers501(t *testing.T) {
	srv, _, _ := newSupervisorAgent(t, steadyGame)
	if resp, body := authed(t, http.MethodPost, srv.URL+"/v1/bridge/install", nil); resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("install without a kit: got %d %s, want 501", resp.StatusCode, body)
	}
	if got := healthLaunch(t, srv.URL); got.BridgeKit {
		t.Error("an agent with no kit must not report bridgeKit")
	}
}
