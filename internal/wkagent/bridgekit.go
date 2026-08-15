package wkagent

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Bridge kit install: the one-click path from "the Wine build runs" to "the
// dwbridge mod runs".
//
// The Wine image ships the pinned-by-proof UE4SS bundle at
// Config.BridgeKitDir (tools/dwbridge-kit), laid out exactly as it must land
// next to the server exe. This verb copies it there. It exists because the
// manual version of this step — scp a tarball, extract it in the right
// directory, chown it — was the single worst part of the first real Wine
// deployment, and because the files themselves cannot live in the image:
// the install directory is a volume the game and Steam own.
//
// Deliberately an explicit operator act with only-when-absent semantics:
// an existing ue4ss/ directory, whatever its version, is the operator's and
// is never overwritten. Removing it (and the game's own files, via Steam's
// validate) is how you get back to a state this verb will touch.

// bridgeModDirName is the directory whose presence means "a UE4SS install
// exists here" — both for refusing to overwrite and for the status the
// console builds its button from.
const bridgeModDirName = "ue4ss"

// gameBinDir is where the kit lands: the directory holding the Windows
// server exe, since UE4SS injects from beside the binary it hooks.
func gameBinDir(installDir string) string {
	return filepath.Join(installDir, filepath.FromSlash(filepath.Dir(defaultWindowsExe)))
}

// bridgeKitPresent reports whether this image carries a kit at all —
// false on the plain image, where the console must not offer the button.
func (a *Agent) bridgeKitPresent() bool {
	if a.cfg.BridgeKitDir == "" {
		return false
	}
	fi, err := os.Stat(a.cfg.BridgeKitDir)
	return err == nil && fi.IsDir()
}

// bridgeInstalled reports whether a UE4SS install already sits next to the
// exe. Version-blind on purpose: whatever is there is the operator's.
func bridgeInstalled(installDir string) bool {
	fi, err := os.Stat(filepath.Join(gameBinDir(installDir), bridgeModDirName))
	return err == nil && fi.IsDir()
}

// handleBridgeInstall copies the image's kit next to the server exe.
func (a *Agent) handleBridgeInstall(w http.ResponseWriter, r *http.Request) {
	if a.game == nil {
		writeError(w, http.StatusBadRequest, "agent is not supervising a game — mod install is supervisor mode only")
		return
	}
	if !a.bridgeKitPresent() {
		writeError(w, http.StatusNotImplemented,
			"this agent's image carries no mod kit — mods need the Wine image (wkagent:*-wine)")
		return
	}
	p := a.game.Profile()
	if !p.Mods {
		writeError(w, http.StatusBadRequest,
			"the selected build cannot load mods — switch to the Windows build first")
		return
	}
	if !p.installed(a.cfg.InstallDir) {
		writeError(w, http.StatusBadRequest,
			"the Windows build is not installed yet — run an update first, so the kit has an exe to sit beside")
		return
	}
	if bridgeInstalled(a.cfg.InstallDir) {
		writeError(w, http.StatusConflict,
			"a ue4ss/ directory already exists next to the exe — not overwriting it; remove it first if you want this kit")
		return
	}

	if err := copyTree(a.cfg.BridgeKitDir, gameBinDir(a.cfg.InstallDir)); err != nil {
		writeError(w, http.StatusInternalServerError, "copying the kit: "+err.Error())
		return
	}
	// The file-IPC rendezvous, so the mod has somewhere to heartbeat the
	// moment it loads — same mkdir prepareRuntime does at start.
	if err := os.MkdirAll(filepath.Join(a.cfg.InstallDir, bridgeDirName), 0o755); err != nil {
		a.cfg.Logger.Warn("could not create the bridge directory", "error", err)
	}
	a.cfg.Logger.Info("bridge kit installed", "from", a.cfg.BridgeKitDir, "to", gameBinDir(a.cfg.InstallDir))

	// The mod only loads when the game process starts, so a running server
	// stays unmodded until its next restart — the console says so.
	writeJSON(w, http.StatusOK, map[string]any{
		"installed":       true,
		"restartRequired": a.game.Running(),
	})
}

// copyTree copies src into dst (which must exist), merging directories and
// never following symlinks — the kit is plain files and directories.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
