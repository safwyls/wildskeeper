package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/safwyls/wildskeeper/internal/store"
)

// A Dragonwilds server end to end through the API: the config editor speaks
// dwconfig, rotate-admin-password works, and the command tier answers 501
// with the capability truth rather than 502.

const dwIni = `;METADATA=(Diff=true, UseCommands=true)
[/Script/Dominion.DedicatedServerSettings]
ServerName=Grimwood Bastion
DefaultWorldName=Ashenfall-Prime
OwnerId=owner-abc
AdminPassword=old-password
`

func newDragonwildsServer(t *testing.T, app *testApp, configPath string) int64 {
	t.Helper()
	id, err := app.store.CreateServer(context.Background(), &store.Server{
		Name: "Grimwood", Game: "dragonwilds", Host: "127.0.0.1",
		Enabled: true, ConfigPath: configPath,
	})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	return id
}

func TestDragonwildsConfigEditor(t *testing.T) {
	app, admin := newTestAppWithAdmin(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "DedicatedServer.ini")
	if err := os.WriteFile(file, []byte(dwIni), 0o644); err != nil {
		t.Fatal(err)
	}
	id := newDragonwildsServer(t, app, file)

	rec := app.do(t, http.MethodGet, fmt.Sprintf("/api/servers/%d/config", id), nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("get config: %d %s", rec.Code, rec.Body)
	}
	var res struct {
		Settings []struct {
			Key, Value, Type string
		} `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range res.Settings {
		if s.Key == "OwnerId" && s.Value == "owner-abc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("OwnerId not served: %+v", res.Settings)
	}

	rec = app.do(t, http.MethodPut, fmt.Sprintf("/api/servers/%d/config", id),
		map[string]any{"changes": map[string]string{"ServerName": "Renamed Keep"}}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("put config: %d %s", rec.Code, rec.Body)
	}
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), "ServerName=Renamed Keep") {
		t.Fatalf("edit not written:\n%s", data)
	}
	if !strings.Contains(string(data), ";METADATA=(Diff=true, UseCommands=true)") {
		t.Fatalf("metadata comment lost:\n%s", data)
	}
}

func TestDragonwildsRotateAdminPassword(t *testing.T) {
	app, admin := newTestAppWithAdmin(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "DedicatedServer.ini")
	if err := os.WriteFile(file, []byte(dwIni), 0o644); err != nil {
		t.Fatal(err)
	}
	id := newDragonwildsServer(t, app, file)

	rec := app.do(t, http.MethodPost, fmt.Sprintf("/api/servers/%d/config/rotate-admin-password", id), nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Body)
	}
	var res struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Password) != 24 {
		t.Fatalf("password = %q, want 24 hex chars", res.Password)
	}
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), "AdminPassword="+res.Password) {
		t.Fatal("new password not on disk")
	}
	if strings.Contains(string(data), "old-password") {
		t.Fatal("old password still on disk")
	}
}

func TestRotateAdminPasswordNeedsAConfigPath(t *testing.T) {
	app, admin := newTestAppWithAdmin(t)
	id := newDragonwildsServer(t, app, "")
	rec := app.do(t, http.MethodPost, fmt.Sprintf("/api/servers/%d/config/rotate-admin-password", id), nil, admin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rotate without a config path: %d, want the setup-guidance 400", rec.Code)
	}
}

func TestDragonwildsCommandsAnswer501(t *testing.T) {
	app, admin := newTestAppWithAdmin(t)
	id := newDragonwildsServer(t, app, "")

	cases := []struct {
		path string
		body any
	}{
		{"broadcast", map[string]string{"message": "hi"}},
		{"kick", map[string]string{"playerUid": "u"}},
		{"ban", map[string]string{"playerUid": "u"}},
		{"unban", map[string]string{"playerUid": "u"}},
		{"save", nil},
		{"shutdown", map[string]any{"waitSeconds": 5, "message": "bye"}},
	}
	for _, tc := range cases {
		rec := app.do(t, http.MethodPost, fmt.Sprintf("/api/servers/%d/%s", id, tc.path), tc.body, admin)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: %d %s, want 501", tc.path, rec.Code, rec.Body)
		}
	}
}

func TestDragonwildsFeaturesOnServerPayload(t *testing.T) {
	app, admin := newTestAppWithAdmin(t)
	id := newDragonwildsServer(t, app, "")
	rec := app.do(t, http.MethodGet, fmt.Sprintf("/api/servers/%d", id), nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("get server: %d", rec.Code)
	}
	var res struct {
		Game     string   `json:"game"`
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Game != "dragonwilds" {
		t.Fatalf("game = %q", res.Game)
	}
	want := []string{"pals", "saves", "logs"}
	if len(res.Features) != len(want) {
		t.Fatalf("features = %v, want %v", res.Features, want)
	}
	for i := range want {
		if res.Features[i] != want[i] {
			t.Fatalf("features = %v, want %v", res.Features, want)
		}
	}
}

// The capabilities endpoint is what lets the console stop guessing. A
// Dragonwilds server with no agent — so no bridge — must report its
// commands as unavailable *and* say why, because that reason is the only
// thing telling an operator what would light them up.
func TestDragonwildsCapabilitiesReportTheMissingBridge(t *testing.T) {
	app, admin := newTestAppWithAdmin(t)
	id := newDragonwildsServer(t, app, "")

	rec := app.do(t, http.MethodGet, fmt.Sprintf("/api/servers/%d/capabilities", id), nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		Probed   bool `json:"probed"`
		Commands map[string]struct {
			Supported bool   `json:"supported"`
			Reason    string `json:"reason"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Probed {
		t.Error("dragonwilds should be probeable; the UI falls back to optimism otherwise")
	}
	save, ok := got.Commands["save"]
	if !ok {
		t.Fatal("no answer for save")
	}
	if save.Supported {
		t.Error("save reported supported with no agent, and so no bridge")
	}
	if !strings.Contains(save.Reason, "dwbridge") {
		t.Errorf("reason should name what's missing, got %q", save.Reason)
	}
	// The endpoint answers for every command the console might offer, so a
	// caller never has to special-case a missing key.
	for _, op := range []string{"broadcast", "kick", "ban", "unban", "shutdown"} {
		if _, ok := got.Commands[op]; !ok {
			t.Errorf("no answer for %s", op)
		}
	}
}

// A game whose client can't be probed must not be reported as incapable —
// that would hide working controls. Absent knowledge, the answer is the
// optimism every caller had before probing existed.
func TestCapabilitiesDefaultToSupportedForAnUnprobeableGame(t *testing.T) {
	app, admin := newTestAppWithAdmin(t)
	_, addr := newFakeGame(t)
	id := newServerPointingAt(t, app, addr, nil)

	rec := app.do(t, http.MethodGet, fmt.Sprintf("/api/servers/%d/capabilities", id), nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		Probed   bool                      `json:"probed"`
		Commands map[string]map[string]any `json:"commands"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Probed {
		t.Error("the test game has no prober, so probed should be false")
	}
	if supported, _ := got.Commands["save"]["supported"].(bool); !supported {
		t.Error("an unprobeable game must not have its commands hidden")
	}
}
