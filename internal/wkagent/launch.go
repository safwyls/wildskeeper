package wkagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Launch profiles: how the agent starts the game.
//
// Dragonwilds ships two builds from one app id, and choosing between them
// is not cosmetic. The native Linux build is what this agent has always
// run: simple, no Wine, and **no way to reach the game with a command** —
// UE4SS is Windows-only, so a Linux server can never carry the dwbridge mod
// and its save/kick/ban verbs stay 501 forever. The Windows build under
// Wine is slower to set up and heavier to run, but it can load UE4SS, which
// is the entire reason Phase 4 exists.
//
// The difference reaches further than the command line — the two builds
// write their config to different directories, are installed from different
// depots, and only one of them has a mod directory — so a profile carries
// all of it rather than leaving callers to remember which parts change.
//
// Verified pieces (docs/dragonwilds-recon.md "Phase 4 unblocked" and
// tools/ue4ss-wine-shim/README.md): the Windows server runs headless under
// plain Wine, UE4SS injects through a version.dll shim, and
// WINEDLLOVERRIDES="version=n,b" is *required* — without it Wine prefers
// its builtin version.dll and the shim never loads, so the mod silently
// never starts.

const (
	// ProfileNative is the native Linux dedicated server. No mod support.
	ProfileNative = "native"
	// ProfileWine is the Windows dedicated server under Wine, which is the
	// only build UE4SS — and therefore dwbridge — can attach to.
	ProfileWine = "wine"
	// ProfileCustom is what a hand-configured GAME_CMD/GAME_ARGS becomes. It
	// is not selectable from the console: the operator has already said
	// exactly what to run, and silently replacing that would be rude.
	ProfileCustom = "custom"
)

// Default locations inside the install root. Both are overridable because
// a game update could rename either, and an agent that can't be pointed at
// the new name is an agent that needs a redeploy to survive a patch.
const (
	defaultNativeScript = "RSDragonwildsServer.sh"
	defaultWindowsExe   = "RSDragonwilds/Binaries/Win64/RSDragonwildsServer-Win64-Shipping.exe"
)

// Profile is one way of starting the game, with everything that differs
// between builds gathered in a single place.
type Profile struct {
	// Name is the stable id: native | wine | custom.
	Name string `json:"name"`
	// Label is how the console names it.
	Label string `json:"label"`
	// Command is the executable. A bare name (no separator) is looked up on
	// PATH — that's how "wine" works; anything else resolves inside the
	// install dir, which is how the native launcher has always worked.
	Command string   `json:"command"`
	Args    []string `json:"args"`
	// Env is added to the agent's own environment for the game process.
	Env []string `json:"-"`
	// Dir is the working directory, relative to the install root. Empty
	// means the install root itself.
	Dir string `json:"-"`
	// Probe is the file whose presence means "this build is installed",
	// relative to the install root. The two builds share an install
	// directory but not a single file, so "installed" is per profile.
	Probe string `json:"-"`
	// ConfigRel is where this build writes DedicatedServer.ini. UE names the
	// directory after the platform, so the Windows build under Wine writes
	// WindowsServer/ — pointing the ini editor at LinuxServer/ for a Wine
	// server would edit a file the game never reads.
	ConfigRel string `json:"configPath"`
	// SteamPlatform is the depot to install; empty means the host's own.
	// Switching profiles therefore means re-installing the game, which the
	// console has to say out loud.
	SteamPlatform string `json:"steamPlatform,omitempty"`
	// Mods reports whether this profile can carry the dwbridge mod, and so
	// whether commands can ever reach the game.
	Mods bool `json:"mods"`
}

// LaunchConfig is the per-profile tuning the environment can supply.
type LaunchConfig struct {
	// Profile is the selected profile name; empty means native.
	Profile string
	// WineBin is the wine executable (default "wine").
	WineBin string
	// WinePrefix is WINEPREFIX for the game. Empty leaves Wine's default
	// (~/.wine), which is fine for a single-purpose container.
	WinePrefix string
	// GameExe overrides the Windows server exe path, relative to install.
	GameExe string
	// NativeScript overrides the Linux launcher, relative to install.
	NativeScript string
}

// winePath converts a Linux absolute path to the Windows path the game sees.
// Wine maps Z: to /, so /dragonwilds/dwbridge is Z:\dragonwilds\dwbridge.
// The mod reads DWBRIDGE_DIR with Windows semantics, so handing it a Linux
// path is the difference between a working bridge and a silently idle one.
func winePath(p string) string {
	return "Z:" + strings.ReplaceAll(p, "/", `\`)
}

// buildProfile assembles the profile for name, given where the game lives
// and which port it should bind.
//
// gameArgs, when set, replaces the arguments entirely — an operator override
// that has always existed and must keep working.
func buildProfile(name string, cfg LaunchConfig, installDir string, port int, gameCommand string, gameArgs []string) Profile {
	portArgs := []string{"-log", fmt.Sprintf("-Port=%d", port)}
	if len(gameArgs) > 0 {
		portArgs = gameArgs
	}

	// An explicit command is the operator saying exactly what to run. Honour
	// it verbatim under its own name rather than pretending it is one of the
	// known profiles.
	if gameCommand != "" {
		return Profile{
			Name: ProfileCustom, Label: "Custom command",
			Command: gameCommand, Args: portArgs,
			Probe: gameCommand, ConfigRel: linuxConfigRel,
		}
	}

	switch name {
	case ProfileWine:
		exe := cfg.GameExe
		if exe == "" {
			exe = defaultWindowsExe
		}
		wine := cfg.WineBin
		if wine == "" {
			wine = "wine"
		}
		env := []string{
			// Required, not optional: without it Wine loads its builtin
			// version.dll and the UE4SS shim beside the exe is ignored.
			"WINEDLLOVERRIDES=version=n,b",
			// The mod finds the shared directory here, in Windows form.
			"DWBRIDGE_DIR=" + winePath(filepath.Join(installDir, bridgeDirName)),
			// A headless server has no use for Wine's chatter, and it would
			// otherwise interleave with the game log the player list is
			// parsed from.
			"WINEDEBUG=-all",
		}
		if cfg.WinePrefix != "" {
			env = append(env, "WINEPREFIX="+cfg.WinePrefix)
		}
		return Profile{
			Name: ProfileWine, Label: "Windows build under Wine (mods)",
			Command: wine,
			// The exe is passed absolute so the working directory is free to
			// be the one the feasibility run used.
			Args:          append([]string{filepath.Join(installDir, exe)}, portArgs...),
			Env:           env,
			Dir:           filepath.Dir(exe),
			Probe:         exe,
			ConfigRel:     windowsConfigRel,
			SteamPlatform: "windows",
			Mods:          true,
		}
	default:
		script := cfg.NativeScript
		if script == "" {
			script = defaultNativeScript
		}
		return Profile{
			Name: ProfileNative, Label: "Native Linux build",
			Command: "./" + strings.TrimPrefix(script, "./"),
			Args:    portArgs,
			Probe:   script,
			// The Linux build writes here; see docs/dragonwilds-recon.md.
			ConfigRel: linuxConfigRel,
		}
	}
}

// Where each build keeps DedicatedServer.ini, relative to the install root.
var (
	linuxConfigRel   = filepath.Join("RSDragonwilds", "Saved", "Config", "LinuxServer", "DedicatedServer.ini")
	windowsConfigRel = filepath.Join("RSDragonwilds", "Saved", "Config", "WindowsServer", "DedicatedServer.ini")
)

// resolveCommand turns a profile's Command into something exec can run: a
// bare name goes to PATH, anything else is relative to the install dir.
func (p Profile) resolveCommand(installDir string) string {
	if !strings.ContainsAny(p.Command, `/\`) {
		return p.Command
	}
	if filepath.IsAbs(p.Command) {
		return p.Command
	}
	return filepath.Join(installDir, p.Command)
}

// installed reports whether this build's files are present.
func (p Profile) installed(installDir string) bool {
	_, err := os.Stat(filepath.Join(installDir, p.Probe))
	return err == nil
}

// SelectableProfiles are the profiles the console may switch between. Custom
// is deliberately absent — see ProfileCustom.
var SelectableProfiles = []string{ProfileNative, ProfileWine}

// validProfile reports whether name is one the console may select.
func validProfile(name string) bool {
	for _, p := range SelectableProfiles {
		if p == name {
			return true
		}
	}
	return false
}

// runnable reports whether the profile's command can actually be executed
// here — for the Wine profile, whether this image has Wine in it at all.
//
// It is not the same question as "is the game installed": an agent running
// the plain image can be *set* to the Wine profile and will then fail at
// exec with nothing useful to say. Answering it up front lets the console
// explain the real fix (move this agent to the Wine image) instead of
// showing a start button that cannot work.
func (p Profile) runnable(installDir string) bool {
	if !strings.ContainsAny(p.Command, `/\`) {
		_, err := exec.LookPath(p.Command)
		return err == nil
	}
	_, err := os.Stat(p.resolveCommand(installDir))
	return err == nil
}
