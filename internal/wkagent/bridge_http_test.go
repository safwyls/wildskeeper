package wkagent_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// bridgeAgent builds a supervisor-mode agent (so the bridge exists) and
// returns the server plus the dwbridge dir the mod and agent share. It
// reuses the supervisor helper (which returns the install dir) and never
// starts the game — the bridge works off files, not the process.
func bridgeAgent(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv, _, install := newSupervisorAgent(t, steadyGame)
	bridgeDir := filepath.Join(install, "dwbridge")
	if err := os.MkdirAll(bridgeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return srv, bridgeDir
}

func writeHeartbeatFile(t *testing.T, dir string, commands ...string) {
	t.Helper()
	hb := map[string]any{"ts": time.Now().Unix(), "version": "dwbridge/test", "protocol": 1, "commands": commands}
	data, _ := json.Marshal(hb)
	if err := os.WriteFile(filepath.Join(dir, "heartbeat.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// answerNextRequest plays the mod for exactly one request: it waits for
// request.json, writes response.json echoing the id, and removes the request.
func answerNextRequest(t *testing.T, dir string, ok bool, errMsg string, data map[string]any) {
	t.Helper()
	reqPath := filepath.Join(dir, "request.json")
	respPath := filepath.Join(dir, "response.json")
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			raw, err := os.ReadFile(reqPath)
			if err != nil {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			os.Remove(reqPath)
			var req struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(raw, &req)
			resp := map[string]any{"id": req.ID, "ok": ok}
			if errMsg != "" {
				resp["error"] = errMsg
			}
			if data != nil {
				resp["data"] = data
			}
			body, _ := json.Marshal(resp)
			_ = os.WriteFile(respPath, body, 0o644)
			return
		}
	}()
}

func TestHealthReportsBridge(t *testing.T) {
	srv, dir := bridgeAgent(t)
	writeHeartbeatFile(t, dir, "ping", "save")

	resp, body := do(t, srv, "GET", "/v1/health", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: %d", resp.StatusCode)
	}
	bridge, ok := body["bridge"].(map[string]any)
	if !ok {
		t.Fatalf("health has no bridge object: %v", body)
	}
	if bridge["available"] != true {
		t.Errorf("bridge.available = %v, want true", bridge["available"])
	}
	cmds, _ := bridge["commands"].([]any)
	if len(cmds) != 2 {
		t.Errorf("bridge.commands = %v", bridge["commands"])
	}
}

func TestBridgeCommandEndpoint(t *testing.T) {
	srv, dir := bridgeAgent(t)
	writeHeartbeatFile(t, dir, "save")
	answerNextRequest(t, dir, true, "", map[string]any{"world": true})

	resp, body := do(t, srv, "POST", "/v1/bridge/command", testToken, map[string]any{"command": "save"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("command: got %d (%v)", resp.StatusCode, body)
	}
	if body["ok"] != true {
		t.Errorf("ok = %v", body["ok"])
	}
}

func TestBridgeCommandEndpointUnavailable(t *testing.T) {
	srv, _ := bridgeAgent(t)
	// No heartbeat written: the bridge is present but no mod is behind it.
	resp, _ := do(t, srv, "POST", "/v1/bridge/command", testToken, map[string]any{"command": "save"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503 for an absent mod", resp.StatusCode)
	}
}

func TestBridgeCommandEndpointCompanionMode(t *testing.T) {
	// A companion-mode agent has no bridge at all.
	srv, _ := newTestAgent(t, "exit 0")
	resp, _ := do(t, srv, "POST", "/v1/bridge/command", testToken, map[string]any{"command": "save"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400 in companion mode", resp.StatusCode)
	}
}

// The telemetry channel: fresh state.json is served with its players, a
// stale one reads as unavailable (a dead mod's roster would show ghosts),
// and no file at all is the calm empty answer every modless server gives.
func TestBridgeStateEndpoint(t *testing.T) {
	srv, dir := bridgeAgent(t)

	get := func() map[string]any {
		t.Helper()
		_, body := authed(t, http.MethodGet, srv.URL+"/v1/bridge/state", nil)
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("state: %v (%s)", err, body)
		}
		return out
	}

	if st := get(); st["available"] != false {
		t.Fatalf("no state file should read as unavailable: %v", st)
	}

	state := map[string]any{
		"ts": time.Now().Unix(),
		"players": []map[string]any{
			{"name": "Vexmarrow", "x": 12345.67, "y": -890.12, "z": 3.0},
		},
		"world": map[string]any{"day": 47},
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	st := get()
	if st["available"] != true {
		t.Fatalf("fresh state should be available: %v", st)
	}
	players, _ := st["players"].([]any)
	if len(players) != 1 {
		t.Fatalf("players = %v", st["players"])
	}
	p := players[0].(map[string]any)
	if p["name"] != "Vexmarrow" || p["x"] != 12345.67 {
		t.Errorf("player payload mangled: %v", p)
	}
	world, _ := st["world"].(map[string]any)
	if world["day"] != 47.0 {
		t.Errorf("world clock lost: %v", st["world"])
	}

	// Stale by timestamp: same file, old ts.
	state["ts"] = time.Now().Add(-time.Minute).Unix()
	data, _ = json.Marshal(state)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if st := get(); st["available"] != false || st["players"] != nil {
		t.Fatalf("stale state must not serve a roster: %v", st)
	}
}
