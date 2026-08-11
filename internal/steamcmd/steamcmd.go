// Package steamcmd holds the SteamCMD-adjacent file operations shared by
// wildskeeper (running them against a local bind mount) and wkagent (running
// them next to the game server). One implementation, two executors, so the
// behavior can't drift between deployment styles.
//
// Nothing here is game-specific: SteamCMD lays out every dedicated server the
// same way, so the app id is a parameter and the cache directories are the
// same for all of them. Each game's id comes from its game.Definition.
package steamcmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// cacheDirs are the directories, relative to a server's install root,
// whose contents SteamCMD rebuilds from scratch: appmanifest files and
// partial downloads under steamapps/, and cached package payloads under
// steam/packages/. A game update sometimes leaves both corrupted, after
// which the updater fails on every start until they're wiped.
var cacheDirs = []string{"steamapps", filepath.Join("steam", "packages")}

// ErrNotInstallRoot means neither cache directory exists under the given
// path — it isn't a server install root (or isn't mounted), and reporting
// a no-op success would leave the user's real cache corrupted.
var ErrNotInstallRoot = errors.New("neither steamapps/ nor steam/packages/ exists — check the install path")

// ClearCache empties the SteamCMD cache directories under the install root
// — the equivalent of `rm -rf ./steamapps/* ./steam/packages/*`.
// Deliberately scoped: only the contents of the two well-known cache
// directories are removed, never the directories themselves (they can be
// mount points) and never game or save files. Returns how many top-level
// entries were deleted.
func ClearCache(installRoot string) (int, error) {
	removed := 0
	found := false
	for _, rel := range cacheDirs {
		dir := filepath.Join(installRoot, rel)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue // absent cache dir is fine; the other may still exist
		}
		if err != nil {
			return removed, fmt.Errorf("reading %s: %w", rel, err)
		}
		found = true
		for _, e := range entries {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return removed, fmt.Errorf("deleting %s: %w", filepath.Join(rel, e.Name()), err)
			}
			removed++
		}
	}
	if !found {
		return removed, ErrNotInstallRoot
	}
	return removed, nil
}

// UpdateArgs builds the SteamCMD argument list for updating (and optionally
// validating) the server install. Kept as data so the agent's job runner
// and its tests agree on exactly what gets executed. SteamCMD is exec'd
// directly (no shell), so each token is its own argv element.
func UpdateArgs(installRoot string, appID int, validate bool) []string {
	return UpdateArgsFor(installRoot, appID, validate, "")
}

// UpdateArgsFor is UpdateArgs for a specific platform's depot. Dragonwilds
// ships its Linux and Windows dedicated servers under one app id, and only
// the Windows one can carry UE4SS — so an agent set to the Wine launch
// profile has to ask Steam for a build its host would never choose.
//
// The override has to come before +login: SteamCMD applies
// @sSteamCmdForcePlatformType when it resolves the app, and a later flag is
// simply ignored, which fails by quietly installing the host platform's
// build instead of erroring.
func UpdateArgsFor(installRoot string, appID int, validate bool, platform string) []string {
	var args []string
	if platform != "" {
		args = append(args, "+@sSteamCmdForcePlatformType", platform)
	}
	args = append(args,
		"+force_install_dir", installRoot,
		"+login", "anonymous",
		"+app_update", strconv.Itoa(appID),
	)
	if validate {
		args = append(args, "validate")
	}
	return append(args, "+quit")
}
