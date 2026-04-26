package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Cleanup sweeps deckpilot's per-session artefact directories and
// removes files older than --days (default 3, matching the project's
// 3-day-retention convention shared with the original Gemini-driven
// cleanup design).
//
// Two directories are swept in one pass:
//   - ~/.deckpilot/hang-dumps/         (snapshot dumps from hang-detect)
//   - ~/.deckpilot/launch-meta/        (per-session launch metadata)
//
// Both contain only sanitized session-name-derived filenames, so
// blanket age-based GC is safe; nothing in either tree is intended to
// be edited by hand.
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
	totals := []struct {
		label string
		dir   string
	}{
		{"hang dumps", hangDumpsDir()},
		{"launch-meta", launchMetaDir()},
	}

	for _, t := range totals {
		removed, missing := sweepDir(t.dir, maxAge, now, dryRun)
		switch {
		case missing:
			fmt.Printf("No %s directory at %s. Nothing to clean.\n", t.label, t.dir)
		case dryRun:
			// sweepDir already printed per-file lines for dry-run.
		default:
			fmt.Printf("Cleaned up %d old %s from %s\n", removed, t.label, t.dir)
		}
	}
}

// sweepDir deletes files in dir whose mtime is older than maxAge from
// now. Returns the number of files removed and a flag indicating the
// directory itself was missing (a clean-no-op, not an error). Errors
// reading individual entries are logged and skipped — one corrupt
// file must not block the rest of the sweep.
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
