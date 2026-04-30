package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCleanup_SweepsBothDirectoriesByAge pins the 3-day retention
// policy across hang-dumps and launch-meta. Files older than the
// threshold are removed; recent files survive.
func TestCleanup_SweepsBothDirectoriesByAge(t *testing.T) {
	tempHome := t.TempDir()
	oldHome := deckpilotUserHome
	oldNow := deckpilotNow
	t.Cleanup(func() {
		deckpilotUserHome = oldHome
		deckpilotNow = oldNow
	})
	deckpilotUserHome = func() (string, error) { return tempHome, nil }
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	deckpilotNow = func() time.Time { return now }

	// Lay down 4 files: stale + fresh in each of the two dirs.
	for _, dir := range []string{hangDumpsDir(), launchMetaDir()} {
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
		// 5 days old vs 1 hour old.
		fiveDaysAgo := now.Add(-5 * 24 * time.Hour)
		oneHourAgo := now.Add(-1 * time.Hour)
		if err := os.Chtimes(stale, fiveDaysAgo, fiveDaysAgo); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(fresh, oneHourAgo, oneHourAgo); err != nil {
			t.Fatal(err)
		}
	}

	Cleanup(nil) // default --days 3

	for _, dir := range []string{hangDumpsDir(), launchMetaDir()} {
		if _, err := os.Stat(filepath.Join(dir, "old.log")); !os.IsNotExist(err) {
			t.Errorf("stale file in %s should have been deleted, err=%v", dir, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "new.log")); err != nil {
			t.Errorf("fresh file in %s must survive, err=%v", dir, err)
		}
	}
}

// TestCleanup_DryRunDeletesNothing pins that --dry-run never touches
// the filesystem — the policy is "always reportable, never silently
// destructive when the operator asked to see first".
func TestCleanup_DryRunDeletesNothing(t *testing.T) {
	tempHome := t.TempDir()
	oldHome := deckpilotUserHome
	oldNow := deckpilotNow
	t.Cleanup(func() {
		deckpilotUserHome = oldHome
		deckpilotNow = oldNow
	})
	deckpilotUserHome = func() (string, error) { return tempHome, nil }
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	deckpilotNow = func() time.Time { return now }

	dir := hangDumpsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "old.log")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stale, now.Add(-30*24*time.Hour), now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	Cleanup([]string{"--dry-run"})

	if _, err := os.Stat(stale); err != nil {
		t.Errorf("--dry-run must not delete: %v", err)
	}
}

// TestCleanup_MissingDirIsNoError pins that the command never errors
// out just because nothing has been written yet — `deckpilot cleanup`
// on a brand-new install is the most common first call.
func TestCleanup_MissingDirIsNoError(t *testing.T) {
	tempHome := t.TempDir()
	oldHome := deckpilotUserHome
	t.Cleanup(func() { deckpilotUserHome = oldHome })
	deckpilotUserHome = func() (string, error) { return tempHome, nil }

	// No directories created. Should print "No ... directory" and
	// return cleanly.
	Cleanup(nil)
}
