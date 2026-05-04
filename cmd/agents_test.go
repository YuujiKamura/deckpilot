package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeAgentsBypassPermissionsInIsolatedWorktrees(t *testing.T) {
	for _, name := range []string{"claude", "sonnet", "haiku"} {
		agent := agents[name]
		if !hasArg(agent.Args, "--dangerously-skip-permissions") {
			t.Fatalf("%s must launch with permission bypass; isolation is provided by cwd/worktree separation: %v", name, agent.Args)
		}
	}
}

func TestClaudeAgentsAvoidCommandPatternPolicy(t *testing.T) {
	for _, name := range []string{"claude", "sonnet", "haiku"} {
		agent := agents[name]
		if hasArg(agent.Args, "--disallowed-tools") {
			t.Fatalf("%s should not rely on command-pattern deny lists: %v", name, agent.Args)
		}
	}
}

func TestQuoteShellArgsPreservesSpacedArguments(t *testing.T) {
	got := quoteShellArgs([]string{"--name", "agent with spaces", ""})
	want := `--name "agent with spaces" ""`
	if got != want {
		t.Fatalf("quoted args mismatch:\nwant: %s\n got: %s", want, got)
	}
}

func TestQuoteShellArg(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"", `""`},
		{"has space", `"has space"`},
		{"has&meta", `"has&meta"`},
	}
	for _, c := range cases {
		if got := quoteShellArg(c.in); got != c.want {
			t.Fatalf("quoteShellArg(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestMaybeCreateLaunchWorktreeKeepsNonGitCwd(t *testing.T) {
	cwd := t.TempDir()
	got, isolated, err := maybeCreateLaunchWorktree(cwd, "claude")
	if err != nil {
		t.Fatalf("maybeCreateLaunchWorktree returned error for non-git cwd: %v", err)
	}
	if isolated {
		t.Fatalf("non-git cwd must not be isolated")
	}
	if got != cwd {
		t.Fatalf("cwd mismatch: got %q want %q", got, cwd)
	}
}

func TestMaybeCreateLaunchWorktreeCreatesDetachedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tempHome := t.TempDir()
	withCleanupSeams(t, tempHome, time.Now(), func() (string, error) { return "[]", nil })

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "deckpilot-test@example.invalid")
	runGit(t, repo, "config", "user.name", "Deckpilot Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "subdir", "file.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "initial")

	got, isolated, err := maybeCreateLaunchWorktree(filepath.Join(repo, "subdir"), "claude")
	if err != nil {
		t.Fatalf("maybeCreateLaunchWorktree: %v", err)
	}
	if !isolated {
		t.Fatal("expected git cwd to be isolated")
	}
	if !strings.HasPrefix(got, managedWorktreesDir()+string(filepath.Separator)) {
		t.Fatalf("isolated cwd %q is outside managed root %q", got, managedWorktreesDir())
	}
	if filepath.Base(got) != "subdir" {
		t.Fatalf("expected launch cwd to preserve repo prefix, got %q", got)
	}
}

func TestMaybeCreateLaunchWorktreeKeepsManagedWorktreeCwd(t *testing.T) {
	tempHome := t.TempDir()
	withCleanupSeams(t, tempHome, time.Now(), func() (string, error) { return "[]", nil })

	cwd := filepath.Join(managedWorktreesDir(), "repo-claude-123")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if !isDeckpilotManagedWorktree(cwd) {
		t.Fatalf("expected %q to be recognized as managed", cwd)
	}
}

func TestGitOutputReturnsTrimmedOutput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init")
	got, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("gitOutput: %v", err)
	}
	if strings.Contains(got, "\n") || got == "" {
		t.Fatalf("gitOutput should return non-empty trimmed output, got %q", got)
	}
}

func runGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
