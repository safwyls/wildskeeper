package wkagent

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The file verbs serve exactly two things, both at fixed locations under
// the install root: the world save directory (as a tar bundle) and
// DedicatedServer.ini. No client-supplied paths, ever — same stance as
// the steam verbs.

// configRelPath is where the *native Linux* dedicated server keeps its
// settings. UE names the config directory after the platform, so the
// Windows build under Wine uses WindowsServer/ instead — supervisor mode
// resolves the path from the active launch profile rather than this
// constant. See Agent.configPath.
const configRelPath = "RSDragonwilds/Saved/Config/LinuxServer/DedicatedServer.ini"

// maxConfigBytes caps a config upload; a real DedicatedServer.ini is a
// few KB.
const maxConfigBytes = 1 << 20

// findSaveDir locates the world save directory: the folder holding the
// *.sav world files under RSDragonwilds/Saved/SaveGames. The casing of the
// last segment differs between sources (SaveGames vs Savegames) and Linux
// is case-sensitive, so both spellings are tried — recon doc, "Saves". A
// fresh install (or one that hasn't booted yet) legitimately has none.
func (a *Agent) findSaveDir() (string, error) {
	for _, dir := range []string{"SaveGames", "Savegames"} {
		full := filepath.Join(a.cfg.InstallDir, "RSDragonwilds", "Saved", dir)
		matches, err := filepath.Glob(filepath.Join(full, "*.sav"))
		if err == nil && len(matches) > 0 {
			return full, nil
		}
	}
	return "", errors.New("no world save found under the install dir (has the server run yet?)")
}

// saveEntry is one file in the bundle.
type saveEntry struct {
	rel  string
	size int64
	mod  int64
}

// listSaveFiles walks the save directory for .sav files, skipping the
// rolling backup folders some server images keep next to the save — the
// same filter the backup archiver applies (internal/backup.writeArchive),
// so an agent-synced backup archives the same set a mounted one would.
func listSaveFiles(saveDir string) ([]saveEntry, error) {
	var out []saveEntry
	err := filepath.WalkDir(saveDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.EqualFold(d.Name(), "backup") || strings.EqualFold(d.Name(), "backups") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".sav") {
			return nil
		}
		rel, err := filepath.Rel(saveDir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, saveEntry{rel: filepath.ToSlash(rel), size: info.Size(), mod: info.ModTime().UnixNano()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, err
}

// bundleETag fingerprints the file set: any added, removed, resized or
// rewritten .sav changes it. Content is not hashed — modtime+size is how
// the rest of wildskeeper detects save changes too.
func bundleETag(entries []saveEntry) string {
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s|%d|%d\n", e.rel, e.size, e.mod)
	}
	return `"` + hex.EncodeToString(h.Sum(nil)) + `"`
}

// handleGetSave streams the world save directory as a tar bundle, with an
// ETag so the poller's unchanged checks cost a directory walk and no
// transfer. No compression: .sav files are already compressed containers.
func (a *Agent) handleGetSave(w http.ResponseWriter, r *http.Request) {
	saveDir, err := a.findSaveDir()
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	entries, err := listSaveFiles(saveDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(entries) == 0 {
		writeError(w, http.StatusNotFound, "world save directory holds no .sav files")
		return
	}
	etag := bundleETag(entries)
	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("ETag", etag)
	tw := tar.NewWriter(w)
	for _, e := range entries {
		f, err := os.Open(filepath.Join(saveDir, filepath.FromSlash(e.rel)))
		if err != nil {
			// Mid-stream now; the client sees a truncated tar and retries.
			a.cfg.Logger.Warn("save bundle: file vanished mid-stream", "file", e.rel, "error", err)
			return
		}
		// Header sizes come from the listing; a file rewritten mid-stream
		// is copied at exactly the promised length so the archive stays
		// well-formed (the ETag the client stored still tells on it).
		// ModTime rides along so the mirror can keep the save's true write
		// time — it is what the console's save cache keys on, and what the
		// world panel reports as "last written". PAX, because USTAR rounds
		// times to whole seconds.
		hdr := &tar.Header{Name: e.rel, Mode: 0o644, Size: e.size, ModTime: time.Unix(0, e.mod), Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			f.Close()
			return
		}
		if _, err := io.CopyN(tw, f, e.size); err != nil {
			f.Close()
			return
		}
		f.Close()
	}
	_ = tw.Close()
}

func (a *Agent) configPath() string {
	// Follow the launch profile wherever this agent runs the game. Serving
	// LinuxServer/ to a console whose server is on the Wine profile would
	// hand the settings editor a file the game never reads — edits would
	// appear to save and change nothing, which is worse than an error.
	// Companion mode launches nothing, so the Linux default stands.
	if a.game != nil {
		return filepath.Join(a.cfg.InstallDir, a.game.Profile().ConfigRel)
	}
	return filepath.Join(a.cfg.InstallDir, filepath.FromSlash(configRelPath))
}

func (a *Agent) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	data, err := os.ReadFile(a.configPath())
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "DedicatedServer.ini not found under the install dir")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

// handlePutConfig replaces DedicatedServer.ini atomically (tmp + rename),
// so the game can never boot on a half-written file.
func (a *Agent) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConfigBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "config too large or unreadable")
		return
	}
	path := a.configPath()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		// Refuse to conjure a config where none exists — that means the
		// install dir is wrong or the server never booted, and a stray
		// file here would mask it.
		writeError(w, http.StatusNotFound, "DedicatedServer.ini not found under the install dir")
		return
	}
	tmp := path + ".wkagent-tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.cfg.Logger.Info("config written", "bytes", len(data))
	w.WriteHeader(http.StatusNoContent)
}
