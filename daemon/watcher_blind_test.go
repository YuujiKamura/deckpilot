package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// screenClient is a pipeClient whose screen the test controls.
type screenClient struct {
	mu     sync.Mutex
	screen string
	block  chan struct{} // non-nil: Tail/History wait on it (a wedged pipe)
}

func (c *screenClient) set(s string) {
	c.mu.Lock()
	c.screen = s
	c.mu.Unlock()
}

func (c *screenClient) read() (string, error) {
	c.mu.Lock()
	block, s := c.block, c.screen
	c.mu.Unlock()
	if block != nil {
		<-block
	}
	return s, nil
}

func (c *screenClient) SendKeys(string) (uint32, error) { return 0, nil }
func (c *screenClient) SendRaw([]byte) (uint32, error)  { return 0, nil }
func (c *screenClient) WaitForAck(uint32) error         { return nil }
func (c *screenClient) Tail(int) (string, error)        { return c.read() }
func (c *screenClient) History() (string, error)        { return c.read() }
func (c *screenClient) Ping() error                     { return nil }
func (c *screenClient) Close() error                    { return nil }

const (
	shellScreen = "Microsoft Windows\nC:\\work>"
	agentScreen = "Update available\n> 1. Update now\n  2. Skip"
)

func useTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	previous := daemonUserHome
	daemonUserHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { daemonUserHome = previous })
	return home
}

// writeMeta writes launch metadata; started nil means "field absent".
func writeMeta(t *testing.T, home, session string, started *bool) {
	t.Helper()
	path := filepath.Join(home, ".deckpilot", "launch-meta", session+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"session_name": session, "agent": "codex"}
	if started != nil {
		meta["agent_started"] = *started
	}
	body, _ := json.Marshal(meta)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func shortTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	previous := requestTimeout
	requestTimeout = d
	t.Cleanup(func() { requestTimeout = previous })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A bare shell prompt means "the agent exited" only once the agent command
// was sent. Every launch starts on a bare prompt, so anything else kills the
// watcher one second into the launch (2026-11-14, ghostty-6520).
func TestShellPromptIsAnExitOnlyAfterAgentStarted(t *testing.T) {
	yes, no := true, false
	metas := []struct {
		name    string
		write   bool
		started *bool
	}{
		{"no-meta", false, nil},
		{"meta-without-flag", true, nil},
		{"meta-not-started", true, &no},
		{"meta-started", true, &yes},
	}
	screens := []struct{ name, text string }{
		{"shell-prompt", shellScreen},
		{"agent-screen", agentScreen},
		{"empty", ""},
	}
	for _, m := range metas {
		for _, s := range screens {
			t.Run(m.name+"/"+s.name, func(t *testing.T) {
				home := useTempHome(t)
				if m.write {
					writeMeta(t, home, "ghostty-7", m.started)
				}
				w := newWatcherForTest("ghostty-7", &screenClient{})
				w.updateContent(s.text)
				wantDead := m.started != nil && *m.started && s.name == "shell-prompt"
				if got := w.Status() == "dead"; got != wantDead {
					t.Fatalf("dead = %v, want %v (status %q)", got, wantDead, w.Status())
				}
			})
		}
	}
}

// Replays the incident: the runner exits on a dead verdict, the session is
// revived, and every kind of request must still be answered from the live
// screen. Before the fix the revived watcher had no runner and the request
// waited forever ("no response from daemon").
func TestRevivedWatcherAnswersRequests(t *testing.T) {
	for _, kind := range []string{"tail", "history", "sendkeys"} {
		for _, reviver := range []string{"Revive", "request-only"} {
			t.Run(kind+"/"+reviver, func(t *testing.T) {
				home := useTempHome(t)
				started := true
				writeMeta(t, home, "ghostty-8", &started)
				shortTimeout(t, 3*time.Second)

				fc := &screenClient{screen: shellScreen}
				w := newWatcherForTest("ghostty-8", fc)
				done := make(chan struct{})
				go func() { w.Run(context.Background()); close(done) }()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("runner did not exit on the dead verdict")
				}
				if w.running.Load() {
					t.Fatal("running flag still set after the runner exited")
				}

				fc.set(agentScreen)
				if reviver == "Revive" {
					if !w.Revive() {
						t.Fatal("Revive failed although the pipe answers")
					}
					waitFor(t, "runner restart", w.running.Load)
				} else {
					// Status flipped to active without a runner: the exact
					// zombie state. A request must restart the runner itself.
					w.mu.Lock()
					w.status = "active"
					w.mu.Unlock()
				}

				type answer struct {
					content string
					err     error
				}
				got := make(chan answer, 1)
				go func() {
					var a answer
					switch kind {
					case "tail":
						a.content, a.err = w.FreshTail(50)
					case "history":
						a.content, a.err = w.FreshHistory()
					case "sendkeys":
						a.content, a.err = w.Send("hello")
					}
					got <- a
				}()
				select {
				case a := <-got:
					if a.err != nil {
						t.Fatalf("%s failed on a revived watcher: %v", kind, a.err)
					}
					if kind != "sendkeys" && a.content != agentScreen {
						t.Fatalf("%s returned %q, want the live screen", kind, a.content)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("%s blocked on a revived watcher", kind)
				}
				w.Stop()
			})
		}
	}
}

// A watcher that cannot read the screen must say so within requestTimeout.
// It must never block without bound and never hand back the cached screen as
// if it were live.
func TestBlindWatcherReportsBlindness(t *testing.T) {
	states := []struct {
		name  string
		setup func(t *testing.T, w *Watcher, fc *screenClient)
		want  error
	}{
		{"stopped", func(t *testing.T, w *Watcher, fc *screenClient) { w.Stop() }, ErrWatcherNotRunning},
		{"runner-wedged-in-pipe", func(t *testing.T, w *Watcher, fc *screenClient) {
			block := make(chan struct{})
			fc.mu.Lock()
			fc.block = block
			fc.mu.Unlock()
			go w.Run(context.Background())
			waitFor(t, "runner start", w.running.Load)
			t.Cleanup(func() { close(block); w.Stop() })
		}, ErrWatcherNoAnswer},
	}
	for _, st := range states {
		for _, kind := range []string{"tail", "history", "sendkeys"} {
			t.Run(st.name+"/"+kind, func(t *testing.T) {
				useTempHome(t)
				shortTimeout(t, 300*time.Millisecond)
				fc := &screenClient{screen: agentScreen}
				w := newWatcherForTest("ghostty-9", fc)
				w.mu.Lock()
				w.lastContent = "CACHED SCREEN"
				w.mu.Unlock()
				st.setup(t, w, fc)

				errCh := make(chan error, 1)
				contentCh := make(chan string, 1)
				go func() {
					var c string
					var err error
					switch kind {
					case "tail":
						c, err = w.FreshTail(50)
					case "history":
						c, err = w.FreshHistory()
					case "sendkeys":
						c, err = w.Send("hello")
					}
					contentCh <- c
					errCh <- err
				}()
				select {
				case c := <-contentCh:
					err := <-errCh
					if !errors.Is(err, st.want) {
						t.Fatalf("err = %v, want %v", err, st.want)
					}
					if c != "" {
						t.Fatalf("blind %s returned content %q", kind, c)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("blind %s blocked instead of reporting", kind)
				}
			})
		}
	}
}

// A full queue is a failed read, not the cached screen.
func TestFullQueueIsAnErrorNotACachedScreen(t *testing.T) {
	shortTimeout(t, 300*time.Millisecond)
	block := make(chan struct{})
	fc := &screenClient{screen: agentScreen, block: block}
	w := newWatcherForTest("ghostty-10", fc)
	w.mu.Lock()
	w.lastContent = "CACHED SCREEN"
	w.mu.Unlock()
	go w.Run(context.Background())
	waitFor(t, "runner start", w.running.Load)
	t.Cleanup(func() { close(block); w.Stop() })

	for i := 0; i < cap(w.reqCh)+4; i++ {
		go w.FreshTail(50)
	}
	waitFor(t, "queue to fill", func() bool { return len(w.reqCh) == cap(w.reqCh) })
	content, err := w.FreshTail(50)
	if err == nil {
		t.Fatalf("full queue returned %q with no error", content)
	}
	if content != "" {
		t.Fatalf("full queue returned content %q", content)
	}
}

// Only one runner may own the pipe, however many times Run is started.
func TestRunIsSingleOwner(t *testing.T) {
	useTempHome(t)
	fc := &screenClient{screen: agentScreen}
	w := newWatcherForTest("ghostty-11", fc)
	first := make(chan struct{})
	go func() { w.Run(context.Background()); close(first) }()
	waitFor(t, "runner start", w.running.Load)
	for i := 0; i < 5; i++ {
		returned := make(chan struct{})
		go func() { w.Run(context.Background()); close(returned) }()
		select {
		case <-returned:
		case <-time.After(2 * time.Second):
			t.Fatal(fmt.Sprintf("extra Run #%d did not return immediately", i))
		}
	}
	w.Stop()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop")
	}
}
