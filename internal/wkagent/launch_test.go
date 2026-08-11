package wkagent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func profileFor(name string) Profile {
	return buildProfile(name, LaunchConfig{}, "/dragonwilds", 7777, "", nil)
}

// The native profile is what every existing deployment runs; its shape must
// not move.
func TestNativeProfileRunsTheLinuxLauncher(t *testing.T) {
	p := profileFor(ProfileNative)

	if p.Command != "./RSDragonwildsServer.sh" {
		t.Errorf("command = %q", p.Command)
	}
	if got := p.resolveCommand("/dragonwilds"); got != "/dragonwilds/RSDragonwildsServer.sh" {
		t.Errorf("resolved = %q, want it inside the install dir", got)
	}
	if !slices.Contains(p.Args, "-log") || !slices.Contains(p.Args, "-Port=7777") {
		t.Errorf("args = %v; -log is load-bearing (the log is the only player-list source)", p.Args)
	}
	if p.Mods {
		t.Error("the Linux build cannot carry UE4SS, so it must not claim mod support")
	}
	if !strings.Contains(p.ConfigRel, "LinuxServer") {
		t.Errorf("config path = %q, want the Linux config dir", p.ConfigRel)
	}
	if p.SteamPlatform != "" {
		t.Errorf("steam platform = %q, want the host's own", p.SteamPlatform)
	}
}

// Everything in this test is a detail that fails silently if it's wrong:
// the game starts, and the mod simply never appears.
func TestWineProfileCarriesWhatUE4SSNeeds(t *testing.T) {
	p := profileFor(ProfileWine)

	if p.Command != "wine" {
		t.Errorf("command = %q, want the wine binary", p.Command)
	}
	// A bare name must stay bare so exec finds it on PATH; joining it to the
	// install dir would look for /dragonwilds/wine.
	if got := p.resolveCommand("/dragonwilds"); got != "wine" {
		t.Errorf("resolved = %q, want a PATH lookup", got)
	}
	if len(p.Args) == 0 || !strings.HasSuffix(p.Args[0], "RSDragonwildsServer-Win64-Shipping.exe") {
		t.Fatalf("args = %v, want the Windows server exe first", p.Args)
	}
	if !filepath.IsAbs(p.Args[0]) {
		t.Errorf("exe %q should be absolute, so the working directory is free", p.Args[0])
	}

	env := strings.Join(p.Env, " ")
	// Without this Wine prefers its builtin version.dll, the shim never
	// loads, and UE4SS never injects — the mod is just quietly absent.
	if !strings.Contains(env, "WINEDLLOVERRIDES=version=n,b") {
		t.Errorf("env = %v, missing the version.dll override the shim needs", p.Env)
	}
	// The mod reads this with Windows semantics; a Linux path leaves the
	// bridge idle with no error anywhere.
	if !strings.Contains(env, `DWBRIDGE_DIR=Z:\dragonwilds\dwbridge`) {
		t.Errorf("env = %v, want DWBRIDGE_DIR as a Z:-mapped Windows path", p.Env)
	}
	if !p.Mods {
		t.Error("the Wine profile is the only one that can carry the mod")
	}
	// UE names the config dir after the platform: a Wine server never reads
	// anything written to LinuxServer/.
	if !strings.Contains(p.ConfigRel, "WindowsServer") {
		t.Errorf("config path = %q, want the Windows config dir", p.ConfigRel)
	}
	if p.SteamPlatform != "windows" {
		t.Errorf("steam platform = %q, want windows", p.SteamPlatform)
	}
}

func TestWinePrefixIsOnlySetWhenConfigured(t *testing.T) {
	bare := buildProfile(ProfileWine, LaunchConfig{}, "/g", 1, "", nil)
	if strings.Contains(strings.Join(bare.Env, " "), "WINEPREFIX") {
		t.Error("an unset prefix should leave Wine's default alone, not export an empty one")
	}
	set := buildProfile(ProfileWine, LaunchConfig{WinePrefix: "/data/wine"}, "/g", 1, "", nil)
	if !slices.Contains(set.Env, "WINEPREFIX=/data/wine") {
		t.Errorf("env = %v, want the configured prefix", set.Env)
	}
}

// An explicit game command is the operator having already decided. Profiles
// must not quietly override it.
func TestExplicitCommandBecomesCustomAndIsNotSelectable(t *testing.T) {
	p := buildProfile(ProfileWine, LaunchConfig{}, "/g", 1, "./my-launcher.sh", []string{"-x"})

	if p.Name != ProfileCustom {
		t.Errorf("name = %q, want custom", p.Name)
	}
	if p.Command != "./my-launcher.sh" || !slices.Contains(p.Args, "-x") {
		t.Errorf("the operator's command was not honoured verbatim: %q %v", p.Command, p.Args)
	}
	if validProfile(ProfileCustom) {
		t.Error("custom must not be selectable from the console")
	}
}

// The two builds share a directory but not a file, so "installed" has to be
// asked per profile — otherwise switching to Wine on a Linux install looks
// ready and fails at exec.
func TestInstalledIsPerBuild(t *testing.T) {
	dir := t.TempDir()
	native, wine := profileFor(ProfileNative), profileFor(ProfileWine)
	// Rebuild against the temp dir so the probes point somewhere real.
	native = buildProfile(ProfileNative, LaunchConfig{}, dir, 7777, "", nil)
	wine = buildProfile(ProfileWine, LaunchConfig{}, dir, 7777, "", nil)

	if native.installed(dir) || wine.installed(dir) {
		t.Fatal("an empty directory has neither build installed")
	}
	if err := os.WriteFile(filepath.Join(dir, defaultNativeScript), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !native.installed(dir) {
		t.Error("the native build should be installed once its launcher exists")
	}
	if wine.installed(dir) {
		t.Error("a Linux install must not count as a Windows one — they are different depots")
	}
}
