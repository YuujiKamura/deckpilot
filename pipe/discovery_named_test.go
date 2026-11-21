package pipe

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A Ghostty process started with GHOSTTY_SESSION_NAME serves a pipe named
// after that session. sessionFromPIDFile finds it from the session file,
// across: session name (custom / default form) x runtime folder x the file
// belongs to this PID or not x the pipe answers or not.
func TestSessionFromPIDFile(t *testing.T) {
	const pid = 4242
	n := 0
	for _, name := range []string{"ghostty-canary-232336", fmt.Sprintf("ghostty-%d", pid)} {
		for _, runtime := range []string{"winui3", "win32"} {
			for _, filePID := range []int{pid, pid + 1} {
				for _, alive := range []bool{true, false} {
					n++
					t.Run(fmt.Sprintf("%s/%s/pid=%d/alive=%v", name, runtime, filePID, alive), func(t *testing.T) {
						root := t.TempDir()
						prev := ghosttySessionsRoot
						ghosttySessionsRoot = func() string { return root }
						t.Cleanup(func() { ghosttySessionsRoot = prev })

						pipePath := fmt.Sprintf(`\\.\pipe\deckpilot-test-named-%d-%d`, fakeSeq.Add(1), filePID)
						if alive {
							pipePath = fakeSession(t, func(req string) string { return "PONG|" + name + "\n" })
						}
						dir := filepath.Join(root, runtime, "sessions")
						if err := os.MkdirAll(dir, 0o700); err != nil {
							t.Fatal(err)
						}
						body := fmt.Sprintf("session_name=%s\nsafe_session_name=%s\npid=%d\nhwnd=0x1234\npipe_path=%s\n", name, name, filePID, pipePath)
						file := filepath.Join(dir, fmt.Sprintf("%s-%d.session", name, filePID))
						if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
							t.Fatal(err)
						}

						s, ok := sessionFromPIDFile(pid)
						want := filePID == pid && alive
						if ok != want {
							t.Fatalf("ok=%v, want %v (session %+v)", ok, want, s)
						}
						if ok && (s.Name != name || s.PipePath != pipePath || s.PID != pid || s.AppRuntime != runtime || s.HWND != "0x1234" || s.SessionFile != file) {
							t.Fatalf("session %+v", s)
						}
					})
				}
			}
		}
	}
	if n != 16 {
		t.Fatalf("generated %d cases", n)
	}
}

func TestSessionFromPIDFileWithoutRoot(t *testing.T) {
	prev := ghosttySessionsRoot
	ghosttySessionsRoot = func() string { return "" }
	t.Cleanup(func() { ghosttySessionsRoot = prev })
	if _, ok := sessionFromPIDFile(1); ok {
		t.Fatal("no sessions root must find nothing")
	}
}
