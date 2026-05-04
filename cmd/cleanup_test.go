package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withCleanupSeams installs all four seams (home, clock, daemon LIST)
// and restores them on test cleanup. Centralizes the boilerplate so
// individual tests stay focused on the policy assertion.
func withCleanupSeams(t *testing.T, tempHome string, now time.Time, listFn func() (string, error)) {
	t.Helper()
	oldHome := deckpilotUserHome
	oldNow := deckpilotNow
	oldList := cleanupListSessions
	t.Cleanup(func() {
		deckpilotUserHome = oldHome
		deckpilotNow = oldNow
		cleanupListSessions = oldList
	})
	deckpilotUserHome = func() (string, error) { return tempHome, nil }
	deckpilotNow = func() time.Time { return now }
	cleanupListSessions = listFn
}

// TestCleanup_HangDumpsSweptByAge pins the 3-day age-based retention
// for hang-dumps. This is the operational-evidence directory and its
// retention policy is age, not liveness.
func TestCleanup_HangDumpsSweptByAge(t *testing.T) {
	tempHome := t.TempDir()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	// Daemon returns empty session list — irrelevant to hang-dumps.
	withCleanupSeams(t, tempHome, now, func() (string, error) { return "[]", nil })

	dir := hangDumpsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	stale := filepath.Join(dir, "old.log")
	fresh := filepath.Join(dir, "new.log")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fiveDaysAgo := now.Add(-5 * 24 * time.Hour)
	oneHourAgo := now.Add(-1 * time.Hour)
	if err := os.Chtimes(stale, fiveDaysAgo, fiveDaysAgo); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, oneHourAgo, oneHourAgo); err != nil {
		t.Fatal(err)
	}

	Cleanup(nil)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale hang-dump should have been deleted, err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh hang-dump must survive, err=%v", err)
	}
}

// TestCleanup_LaunchMetaLiveSessionSurvivesAge pins the core fix for
// issue #29: a launch-meta file for a session that the daemon
// reports as live must NOT be deleted, regardless of how old its
// mtime is. A 30-day-old long-running session is fully legitimate.
func TestCleanup_LaunchMetaLiveSessionSurvivesAge(t *testing.T) {
	tempHome := t.TempDir()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	// Daemon reports ghostty-X as alive.
	withCleanupSeams(t, tempHome, now, func() (string, error) {
		return `[{"name":"ghostty-X","pid":1234,"app_runtime":"win32","status":"running","uptime":"30d"}]`, nil
	})

	dir := launchMetaDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(dir, "ghostty-X.json")
	if err := os.WriteFile(live, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	thirtyDaysAgo := now.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(live, thirtyDaysAgo, thirtyDaysAgo); err != nil {
		t.Fatal(err)
	}

	Cleanup(nil)

	if _, err := os.Stat(live); err != nil {
		t.Errorf("live session's launch-meta must survive regardless of age, err=%v", err)
	}
}

// TestCleanup_LaunchMetaDeadSessionDeletedRegardlessOfAge pins the
// other half of the liveness policy: a session not in the daemon's
// LIST must be evicted, even if its mtime is fresh.
func TestCleanup_LaunchMetaDeadSessionDeletedRegardlessOfAge(t *testing.T) {
	tempHome := t.TempDir()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	// Daemon reports only ghostty-X as alive; ghostty-DEAD is not.
	withCleanupSeams(t, tempHome, now, func() (string, error) {
		return `[{"name":"ghostty-X","pid":1,"app_runtime":"win32","status":"running","uptime":"1m"}]`, nil
	})

	dir := launchMetaDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(dir, "ghostty-DEAD.json")
	if err := os.WriteFile(dead, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Even fresh files get evicted if the session is dead.
	oneMinAgo := now.Add(-1 * time.Minute)
	if err := os.Chtimes(dead, oneMinAgo, oneMinAgo); err != nil {
		t.Fatal(err)
	}

	Cleanup(nil)

	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("dead session's launch-meta must be deleted, err=%v", err)
	}
}

// TestCleanup_LaunchMetaDaemonDownIsNoOp pins the conservative
// behavior: if we cannot determine liveness, we delete nothing. A
// transient daemon-down condition must never wipe resume metadata
// for every live session.
func TestCleanup_LaunchMetaDaemonDownIsNoOp(t *testing.T) {
	tempHome := t.TempDir()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	withCleanupSeams(t, tempHome, now, func() (string, error) {
		return "", fmt.Errorf("daemon: connection refused")
	})

	dir := launchMetaDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Make every file ancient — under the old age-based policy they
	// would all be wiped. Under the new policy with daemon down, all
	// must survive.
	for _, name := range []string{"ghostty-A.json", "ghostty-B.json", "ghostty-C.json"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		ancient := now.Add(-30 * 24 * time.Hour)
		if err := os.Chtimes(path, ancient, ancient); err != nil {
			t.Fatal(err)
		}
	}

	Cleanup(nil)

	for _, name := range []string{"ghostty-A.json", "ghostty-B.json", "ghostty-C.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("daemon-down must not delete %s, err=%v", name, err)
		}
	}
}

// TestCleanup_DryRunDeletesNothing pins that --dry-run never touches
// the filesystem — neither the age-based hang-dumps sweep nor the
// liveness-based launch-meta sweep.
func TestCleanup_DryRunDeletesNothing(t *testing.T) {
	tempHome := t.TempDir()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	// Daemon says "no live sessions" — would normally evict
	// everything in launch-meta, but --dry-run must hold.
	withCleanupSeams(t, tempHome, now, func() (string, error) { return "[]", nil })

	hdDir := hangDumpsDir()
	lmDir := launchMetaDir()
	if err := os.MkdirAll(hdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hdStale := filepath.Join(hdDir, "old.log")
	lmDead := filepath.Join(lmDir, "ghostty-DEAD.json")
	for _, p := range []string{hdStale, lmDead} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		ancient := now.Add(-30 * 24 * time.Hour)
		if err := os.Chtimes(p, ancient, ancient); err != nil {
			t.Fatal(err)
		}
	}

	Cleanup([]string{"--dry-run"})

	if _, err := os.Stat(hdStale); err != nil {
		t.Errorf("--dry-run must not delete hang-dump: %v", err)
	}
	if _, err := os.Stat(lmDead); err != nil {
		t.Errorf("--dry-run must not delete dead launch-meta: %v", err)
	}
}

// TestCleanup_MissingDirIsNoError pins that the command never errors
// out just because nothing has been written yet — `deckpilot cleanup`
// on a brand-new install is the most common first call.
func TestCleanup_MissingDirIsNoError(t *testing.T) {
	tempHome := t.TempDir()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	withCleanupSeams(t, tempHome, now, func() (string, error) { return "[]", nil })

	// No directories created. Should print "No ... directory" and
	// return cleanly.
	Cleanup(nil)
}

func TestManagedWorktreeRootForPath(t *testing.T) {
	tempHome := t.TempDir()
	withCleanupSeams(t, tempHome, time.Now(), func() (string, error) { return "[]", nil })

	root := managedWorktreesDir()
	inside := filepath.Join(root, "repo-claude-123", "cmd")
	got, ok := managedWorktreeRootForPath(inside)
	if !ok {
		t.Fatalf("expected managed worktree root for %s", inside)
	}
	want := filepath.Join(root, "repo-claude-123")
	if got != want {
		t.Fatalf("root mismatch: got %q want %q", got, want)
	}

	if got, ok := managedWorktreeRootForPath(filepath.Join(tempHome, "repo")); ok {
		t.Fatalf("unmanaged path returned root %q", got)
	}
}

func TestReadLaunchMetaFile(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(valid, []byte(`{"session_name":"s","agent":"claude","cwd":"/w","prompt":"p"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := readLaunchMetaFile(valid)
	if !ok {
		t.Fatal("expected valid launch meta to parse")
	}
	if got.SessionName != "s" || got.Cwd != "/w" {
		t.Fatalf("unexpected meta: %+v", got)
	}

	invalid := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte(`not-json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readLaunchMetaFile(invalid); ok {
		t.Fatal("invalid launch meta should return ok=false")
	}
	if _, ok := readLaunchMetaFile(filepath.Join(dir, "missing.json")); ok {
		t.Fatal("missing launch meta should return ok=false")
	}
}

func TestRemoveManagedWorktreeRefusesUnmanagedPath(t *testing.T) {
	tempHome := t.TempDir()
	withCleanupSeams(t, tempHome, time.Now(), func() (string, error) { return "[]", nil })

	if err := removeManagedWorktree(filepath.Join(tempHome, "outside")); err == nil {
		t.Fatal("expected unmanaged path removal to be refused")
	}
}

func TestRemoveManagedWorktreeMissingManagedPathIsNoop(t *testing.T) {
	tempHome := t.TempDir()
	withCleanupSeams(t, tempHome, time.Now(), func() (string, error) { return "[]", nil })

	missing := filepath.Join(managedWorktreesDir(), "missing-worktree")
	if err := removeManagedWorktree(missing); err != nil {
		t.Fatalf("missing managed path should be noop: %v", err)
	}
}
