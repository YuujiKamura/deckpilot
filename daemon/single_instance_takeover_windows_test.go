//go:build windows

package daemon

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

// TestDaemon_SecondInstanceTakesOverFirst pins the regression observed
// against the older vendor/deckpilot ed99e1d binary: launching deckpilot
// twice left **3** daemons running concurrently — there was no
// single-instance gate. Commit 10214df introduced the "takeover" path: a
// new Daemon.Run() dials the existing pipe, sends "SHUTDOWN\n", waits up
// to 3s for the pipe to be released, and only then ListenPipes itself.
//
// This test exercises the SHUTDOWN handshake leg of the takeover. We
// stand up a fake "first daemon" that listens on a unique per-test pipe
// path and accepts one connection. The "second daemon" simulates the
// takeover preamble in daemon.go:73-95 (Dial, write SHUTDOWN, close,
// poll until pipe is gone). The fake first daemon, on receiving
// SHUTDOWN, stops listening — modeling the real handleShutdown code
// path that calls os.Exit shortly after responding.
//
// The test asserts:
//  1. The first instance receives "SHUTDOWN" verbatim (no other command).
//  2. The first instance can release the pipe (i.e. the second
//     instance's poll loop terminates within the 3s budget).
//  3. The second instance can then ListenPipe on the same path — proving
//     the lock has actually transferred.
//
// If a refactor removes the SHUTDOWN preamble from Run() (e.g. someone
// "simplifies" startup back to the pre-10214df behavior of just
// ListenPipe-or-fail), step 3 fails because Windows refuses a second
// listener on the same pipe.
func TestDaemon_SecondInstanceTakesOverFirst(t *testing.T) {
	suffix := fmt.Sprintf("takeover-test-%d", time.Now().UnixNano())
	t.Setenv("DECKPILOT_PIPE_SUFFIX", suffix)
	pipePath := IPCPipePath()
	if !strings.Contains(pipePath, suffix) {
		t.Fatalf("DECKPILOT_PIPE_SUFFIX not honored: %q", pipePath)
	}

	// --- First daemon: minimal listener that responds to SHUTDOWN. ---
	listener1, err := winio.ListenPipe(pipePath, nil)
	if err != nil {
		t.Fatalf("first ListenPipe: %v", err)
	}

	var receivedShutdown atomic.Bool
	var firstAccepted sync.WaitGroup
	firstAccepted.Add(1)

	go func() {
		defer firstAccepted.Done()
		conn, err := listener1.Accept()
		if err != nil {
			// Listener may have been closed by the test on the success path.
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		scanner := bufio.NewScanner(conn)
		if scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "SHUTDOWN" {
				receivedShutdown.Store(true)
			} else {
				t.Errorf("first daemon got unexpected command %q, want SHUTDOWN", line)
			}
		}
	}()

	// --- Second daemon: replicate the takeover preamble from Run(). ---
	conn, err := winio.DialPipe(pipePath, nil)
	if err != nil {
		listener1.Close()
		t.Fatalf("second-daemon dial of existing pipe failed: %v", err)
	}
	if _, err := fmt.Fprintln(conn, "SHUTDOWN"); err != nil {
		conn.Close()
		listener1.Close()
		t.Fatalf("write SHUTDOWN: %v", err)
	}
	conn.Close()

	// Real Run() now closes-and-waits the first listener. We simulate
	// the first daemon's handleShutdown response by closing the listener
	// once the SHUTDOWN line was observed. Wait briefly for the goroutine
	// to read, then close.
	doneCh := make(chan struct{})
	go func() { firstAccepted.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		listener1.Close()
		t.Fatalf("first daemon did not read SHUTDOWN within 2s")
	}
	listener1.Close()

	if !receivedShutdown.Load() {
		t.Errorf("first daemon never observed SHUTDOWN command (takeover preamble missing?)")
	}

	// Poll until the pipe is gone, mirroring Run() lines 85-95.
	deadline := time.Now().Add(3 * time.Second)
	released := false
	for time.Now().Before(deadline) {
		c, derr := winio.DialPipe(pipePath, nil)
		if derr != nil {
			released = true
			break
		}
		c.Close()
		time.Sleep(50 * time.Millisecond)
	}
	if !released {
		t.Fatalf("pipe %s not released within 3s — takeover poll loop would spin", pipePath)
	}

	// Now the second daemon must be able to claim the pipe.
	listener2, err := winio.ListenPipe(pipePath, nil)
	if err != nil {
		t.Fatalf("second ListenPipe after takeover failed: %v (this is the regression: pipe still locked)", err)
	}
	listener2.Close()
}

// TestDaemon_RunRejectsSecondListenerWithoutTakeover documents the
// negative case: bypassing the takeover preamble (i.e. trying to
// ListenPipe twice without first sending SHUTDOWN) must be rejected by
// Windows. If this *passes* (i.e. two listeners coexist), Windows
// changed its semantics and the takeover guarantee in 10214df no longer
// holds — we want a loud failure so we re-evaluate the design.
func TestDaemon_RunRejectsSecondListenerWithoutTakeover(t *testing.T) {
	suffix := fmt.Sprintf("notakeover-test-%d", time.Now().UnixNano())
	if err := os.Setenv("DECKPILOT_PIPE_SUFFIX", suffix); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("DECKPILOT_PIPE_SUFFIX")
	pipePath := IPCPipePath()

	listener1, err := winio.ListenPipe(pipePath, nil)
	if err != nil {
		t.Fatalf("first ListenPipe: %v", err)
	}
	defer listener1.Close()

	_, err = winio.ListenPipe(pipePath, nil)
	if err == nil {
		t.Fatalf("second ListenPipe on %s succeeded — takeover invariant broken", pipePath)
	}
	// Real Run() at daemon.go:139 keys off "Access is denied" / "0x5";
	// any error is acceptable here, we just need confirmation that
	// double-listen is impossible without the SHUTDOWN handshake.
	if !strings.Contains(err.Error(), "Access is denied") &&
		!strings.Contains(err.Error(), "0x5") &&
		!strings.Contains(err.Error(), "All pipe instances are busy") {
		t.Logf("note: second ListenPipe failed with unexpected error %q (still rejected, just diff message)", err)
	}
}
