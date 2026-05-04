package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// cleanupListSessions is the daemon LIST RPC seam. Tests swap this to
// inject "alive session" sets or simulate daemon-down conditions for
// the launch-meta liveness GC. This is intentionally a daemon-RPC
// seam, separate from the paths.go filesystem/clock seams: paths.go
// owns *where* artefacts live, this owns *who is alive*.
var cleanupListSessions = daemon.DaemonList

// Cleanup sweeps deckpilot's per-session artefact directories.
//
// Two directories are swept in one pass, with deliberately different
// retention models:
//
//   - ~/.deckpilot/hang-dumps/   : age-based GC (default --days 3).
//     These are operational evidence of past hangs; the value is the
//     audit trail, not session liveness, so a fixed retention window
//     is the right policy.
//
//   - ~/.deckpilot/launch-meta/  : liveness-based GC. A launch-meta
//     file's lifetime is the *session*'s lifetime — if `deckpilot
//     resume` or `hang-detect snapshot` can still target the session,
//     the meta record must still exist. Age is irrelevant: a
//     long-running session legitimately keeps its meta for weeks.
//     Issue #29: the previous age-based sweep silently broke resume
//     for any session older than 3 days. We now ask the daemon for
//     the live session list and only delete meta files whose session
//     name is NOT in that list. If the daemon LIST call fails for any
//     reason (daemon down, IPC error, malformed JSON), we delete
//     nothing — a conservative no-op is always safer than wrongly
//     evicting a live session's resume metadata.
//
// The 3-day retention policy in the project memory file applies to
// hang-dumps (and any future age-based artefact dir). launch-meta is
// a separate concept with a separate, liveness-based policy.
func Cleanup(args []string) {
	maxAge := 3 * 24 * time.Hour
	dryRun := false

	for i := 0; i < len(args); i++ {
		if args[i] == "--days" && i+1 < len(args) {
			var days int
			fmt.Sscanf(args[i+1], "%d", &days)
			maxAge = time.Duration(days) * 24 * time.Hour
			i++
		} else if args[i] == "--dry-run" {
			dryRun = true
		}
	}

	now := deckpilotNow()

	// 1. hang-dumps: age-based sweep (unchanged policy).
	hdDir := hangDumpsDir()
	removed, missing := sweepDir(hdDir, maxAge, now, dryRun)
	switch {
	case missing:
		fmt.Printf("No hang dumps directory at %s. Nothing to clean.\n", hdDir)
	case dryRun:
		// sweepDir already printed per-file lines.
	default:
		fmt.Printf("Cleaned up %d old hang dumps from %s\n", removed, hdDir)
	}

	// 2. launch-meta: liveness-based sweep.
	lmDir := launchMetaDir()
	removed, missing, abort := sweepLaunchMetaByLiveness(lmDir, dryRun)
	switch {
	case missing:
		fmt.Printf("No launch-meta directory at %s. Nothing to clean.\n", lmDir)
	case abort:
		fmt.Printf("Skipped launch-meta sweep at %s: could not query daemon for live sessions (conservative no-op).\n", lmDir)
	case dryRun:
		// sweepLaunchMetaByLiveness already printed per-file lines.
	default:
		fmt.Printf("Cleaned up %d dead launch-meta entries from %s\n", removed, lmDir)
	}
}

// sweepDir deletes files in dir whose mtime is older than maxAge from
// now. Used by the hang-dumps age-based sweep. Returns the number of
// files removed and a flag indicating the directory itself was
// missing (a clean no-op, not an error). Errors reading individual
// entries are logged and skipped — one corrupt file must not block
// the rest of the sweep.
func sweepDir(dir string, maxAge time.Duration, now time.Time, dryRun bool) (removed int, missing bool) {
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, true
		}
		fmt.Fprintf(os.Stderr, "cleanup: read %s: %v\n", dir, err)
		return 0, false
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		path := filepath.Join(dir, f.Name())
		if dryRun {
			fmt.Printf("[dry-run] would delete: %s (%s old)\n",
				path, now.Sub(info.ModTime()).Round(time.Hour))
			continue
		}
		if err := os.Remove(path); err != nil {
			fmt.Fprintf(os.Stderr, "failed to delete %s: %v\n", path, err)
			continue
		}
		removed++
	}
	return removed, false
}

// sweepLaunchMetaByLiveness deletes launch-meta JSON files whose
// session name does NOT appear in the daemon's live session LIST.
// Files are stored as <sanitized-session-name>.json (see
// launchmeta.go), so we strip the .json suffix to derive the session
// key for comparison.
//
// If the daemon LIST call fails (daemon down, IPC error, malformed
// JSON), `abort` is set and no deletions happen. This conservative
// no-op prevents a transient IPC failure from wiping resume metadata
// for every live session.
//
// `missing` indicates the launch-meta directory itself does not yet
// exist (clean install case) and is not an error.
func sweepLaunchMetaByLiveness(dir string, dryRun bool) (removed int, missing bool, abort bool) {
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, true, false
		}
		fmt.Fprintf(os.Stderr, "cleanup: read %s: %v\n", dir, err)
		return 0, false, false
	}

	// Query the daemon for live sessions BEFORE touching anything.
	// Any failure here is fatal to this sweep — we will not guess.
	raw, err := cleanupListSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cleanup: launch-meta liveness query failed: %v\n", err)
		return 0, false, true
	}
	var sessions []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
		fmt.Fprintf(os.Stderr, "cleanup: launch-meta liveness query bad json: %v\n", err)
		return 0, false, true
	}
	live := make(map[string]struct{}, len(sessions))
	for _, s := range sessions {
		// Match the on-disk filename convention used by
		// launchmeta.go (sanitizeFilename of the session name).
		live[sanitizeFilename(s.Name)] = struct{}{}
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		if !strings.HasSuffix(name, ".json") {
			// Don't touch unexpected files — could be a future
			// artefact format the operator added by hand.
			continue
		}
		key := strings.TrimSuffix(name, ".json")
		if _, alive := live[key]; alive {
			continue
		}
		path := filepath.Join(dir, name)
		meta, hasMeta := readLaunchMetaFile(path)
		worktreePath, hasWorktree := "", false
		if hasMeta {
			worktreePath, hasWorktree = managedWorktreeRootForPath(meta.Cwd)
		}
		if dryRun {
			fmt.Printf("[dry-run] would delete dead launch-meta: %s\n", path)
			if hasWorktree {
				fmt.Printf("[dry-run] would remove managed worktree: %s\n", worktreePath)
			}
			continue
		}
		if hasWorktree {
			if err := removeManagedWorktree(worktreePath); err != nil {
				fmt.Fprintf(os.Stderr, "failed to remove managed worktree %s: %v\n", worktreePath, err)
				continue
			}
		}
		if err := os.Remove(path); err != nil {
			fmt.Fprintf(os.Stderr, "failed to delete %s: %v\n", path, err)
			continue
		}
		removed++
	}
	return removed, false, false
}

func readLaunchMetaFile(path string) (LaunchMeta, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return LaunchMeta{}, false
	}
	var meta LaunchMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return LaunchMeta{}, false
	}
	return meta, true
}

func managedWorktreeRootForPath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	rel, err := filepath.Rel(managedWorktreesDir(), path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "" {
		return "", false
	}
	return filepath.Join(managedWorktreesDir(), parts[0]), true
}

func removeManagedWorktree(path string) error {
	if _, ok := managedWorktreeRootForPath(path); !ok {
		return fmt.Errorf("refusing to remove unmanaged path")
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	cmd := exec.Command("git", "-C", path, "worktree", "remove", "--force", path)
	cmd.Dir = managedWorktreesDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		if rmErr := os.RemoveAll(path); rmErr != nil {
			return fmt.Errorf("git worktree remove: %w: %s; fallback remove: %w", err, strings.TrimSpace(string(out)), rmErr)
		}
	}
	return nil
}
