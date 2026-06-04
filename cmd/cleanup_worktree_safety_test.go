package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// makeRegisteredWorktree creates a real git repository and adds a
// detached worktree under managedWorktreesDir(), returning the worktree
// path. When dirty is true, an uncommitted file is left inside it. This
// models the production reality the plain-os.MkdirAll fixtures in
// cleanup_test.go do NOT: an orphan-swept directory is a *registered*
// git worktree, and git's own remove safety is the only thing standing
// between the sweep and a live worker's uncommitted work.
func makeRegisteredWorktree(t *testing.T, dirty bool) string {
	t.Helper()
	repo := t.TempDir()
	mustGit(t, repo, "init", "-q")
	mustGit(t, repo, "config", "user.email", "t@e")
	mustGit(t, repo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "seed")

	wtRoot := managedWorktreesDir()
	if err := os.MkdirAll(wtRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(wtRoot, "repo-claude-real")
	mustGit(t, repo, "worktree", "add", "--detach", wt, "HEAD")

	if dirty {
		// Uncommitted, untracked work — exactly what a live worker
		// would have on disk. git worktree remove (no --force) must
		// refuse to destroy this.
		if err := os.WriteFile(filepath.Join(wt, "WORK_IN_PROGRESS.txt"),
			[]byte("uncommitted worker output\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return wt
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// TestRemoveManagedWorktree_RegisteredDirtyWorktreePreserved is the
// direct regression for the 2026-06-04 incident: the orphan sweep
// force-deleted a *live* worker's registered worktree (deckpilot-claude-*)
// because its session had no resolvable launch-meta, and
// removeManagedWorktree used `git worktree remove --force` + an
// os.RemoveAll fallback that defeated every git safety mechanism. A
// registered worktree carrying uncommitted work must NEVER be removed by
// this path — git refuses it without --force, and we must honor that.
func TestRemoveManagedWorktree_RegisteredDirtyWorktreePreserved(t *testing.T) {
	tempHome := t.TempDir()
	withCleanupSeams(t, tempHome, time.Now(), func() (string, error) { return "[]", nil })

	wt := makeRegisteredWorktree(t, true /* dirty */)

	err := removeManagedWorktree(wt)
	if err == nil {
		t.Fatal("removeManagedWorktree must REFUSE a registered worktree with uncommitted work")
	}
	if _, statErr := os.Stat(wt); os.IsNotExist(statErr) {
		t.Fatal("dirty registered worktree was destroyed — uncommitted work lost (the 2026-06-04 bug)")
	}
	if _, statErr := os.Stat(filepath.Join(wt, "WORK_IN_PROGRESS.txt")); statErr != nil {
		t.Fatalf("uncommitted file must survive: %v", statErr)
	}
}

// TestRemoveManagedWorktree_RegisteredCleanWorktreeRemoved pins that the
// fix does not over-correct: a *clean* registered worktree (a genuinely
// dead session that committed or had nothing to lose) is still removed,
// so the orphan sweep keeps its legitimate purpose.
func TestRemoveManagedWorktree_RegisteredCleanWorktreeRemoved(t *testing.T) {
	tempHome := t.TempDir()
	withCleanupSeams(t, tempHome, time.Now(), func() (string, error) { return "[]", nil })

	wt := makeRegisteredWorktree(t, false /* clean */)

	if err := removeManagedWorktree(wt); err != nil {
		t.Fatalf("clean registered worktree should remove cleanly: %v", err)
	}
	if _, statErr := os.Stat(wt); !os.IsNotExist(statErr) {
		t.Fatalf("clean worktree should be gone after removal")
	}
}

// TestRemoveManagedWorktree_DanglingDirRemoved pins that a non-git
// leftover directory (no worktree registration, so no git-tracked work
// to lose) is still cleaned — this is what the plain-MkdirAll fixtures in
// cleanup_test.go exercise and must keep working.
func TestRemoveManagedWorktree_DanglingDirRemoved(t *testing.T) {
	tempHome := t.TempDir()
	withCleanupSeams(t, tempHome, time.Now(), func() (string, error) { return "[]", nil })

	wtRoot := managedWorktreesDir()
	dangling := filepath.Join(wtRoot, "repo-claude-dangling")
	if err := os.MkdirAll(filepath.Join(dangling, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedWorktree(dangling); err != nil {
		t.Fatalf("dangling non-worktree dir should be removable: %v", err)
	}
	if _, statErr := os.Stat(dangling); !os.IsNotExist(statErr) {
		t.Fatalf("dangling dir should be gone after removal")
	}
}
