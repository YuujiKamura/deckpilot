//go:build windows

// stressghostty — minimal CP stress repro (issue #26 / ghostty #211 /
// ghostty #212 / zig-control-plane#2).
//
// Purpose: answer "does Ghostty hang when its Control Plane pipe is
// hit with high-rate INPUT commands, independent of deckpilot's
// send/approve path?" Without a deterministic repro we can't tell
// whether Phase 0/0.5 mutex + try-lock are sufficient or whether
// Ghostty itself collapses under any sufficiently aggressive client.
//
// This tool deliberately does NOT route through daemon.DaemonSend.
// It opens the session's CP pipe directly and writes INPUT commands
// at the configured rate. No mutex, no try-lock, no post-verify, no
// adapter — the raw hostile-client behavior we want Ghostty to
// survive.
//
// Usage:
//
//	stressghostty <session> [--rate 200] [--duration 15s] [--size 4]
//	              [--poll 250ms] [--quiet]
//
// Output: CSV-like log on stderr, plus a final summary line on stdout
// that a shell can grep.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/YuujiKamura/deckpilot/daemon"
)

var (
	procIsHungAppWindow = syscall.NewLazyDLL("user32.dll").NewProc("IsHungAppWindow")
)

func isHungAppWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	ret, _, _ := procIsHungAppWindow.Call(hwnd)
	return ret != 0
}

type sessionEntry struct {
	Name     string `json:"name"`
	PID      int    `json:"pid"`
	PipePath string `json:"pipe_path"`
	Status   string `json:"status"`
}

func resolveSession(name string) (pipePath string, pid uint32, err error) {
	raw, err := daemon.DaemonList()
	if err != nil {
		return "", 0, fmt.Errorf("daemon list: %w", err)
	}
	var entries []sessionEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return "", 0, fmt.Errorf("parse list: %w", err)
	}
	for _, e := range entries {
		if e.Name == name {
			if e.PipePath == "" {
				return "", 0, fmt.Errorf("session %q has no pipe path", name)
			}
			if e.PID <= 0 {
				return "", 0, fmt.Errorf("session %q has no PID", name)
			}
			return e.PipePath, uint32(e.PID), nil
		}
	}
	return "", 0, fmt.Errorf("session %q not found", name)
}

// hwndOfPID queries the Windows process for its MainWindowHandle
// equivalent. We use EnumWindows + GetWindowThreadProcessId rather
// than Process.MainWindowHandle (which isn't available in syscall
// directly) — but for a quick repro we can actually read hwnd from
// the session file the daemon already knows about.
func hwndOfSession(sessionName string) (uintptr, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	// Session file path used by ghostty-winui3 CP.
	sessionFile := fmt.Sprintf(
		`%s\AppData\Local\ghostty\control-plane\winui3\sessions\%s-%s.session`,
		home, sessionName, strings.TrimPrefix(sessionName, "ghostty-"))
	f, err := os.Open(sessionFile)
	if err != nil {
		return 0, fmt.Errorf("session file: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "hwnd=") {
			hex := strings.TrimPrefix(line, "hwnd=")
			hex = strings.TrimPrefix(hex, "0x")
			var v uint64
			if _, err := fmt.Sscanf(hex, "%x", &v); err != nil {
				return 0, fmt.Errorf("parse hwnd %q: %w", hex, err)
			}
			return uintptr(v), nil
		}
	}
	return 0, fmt.Errorf("hwnd not found in %s", sessionFile)
}

// sendInputRaw writes a single INPUT command to the CP pipe. It
// reopens the pipe every call, mirroring the one-shot mode of most
// CP clients. Returns the round-trip time from open to ack read.
func sendInputRaw(pipePath, data string) (time.Duration, string, error) {
	start := time.Now()
	conn, err := winio.DialPipe(pipePath, nil)
	if err != nil {
		return time.Since(start), "", fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	encoded := base64.StdEncoding.EncodeToString([]byte(data))
	cmd := fmt.Sprintf("INPUT|stress|%s\n", encoded)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return time.Since(start), "", fmt.Errorf("write: %w", err)
	}
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return time.Since(start), "", fmt.Errorf("no ack")
	}
	return time.Since(start), scanner.Text(), nil
}

func main() {
	rate := flag.Int("rate", 200, "INPUT commands per second per worker")
	workers := flag.Int("workers", 1, "parallel sender goroutines (each dials its own pipe)")
	duration := flag.Duration("duration", 15*time.Second, "test duration")
	size := flag.Int("size", 4, "payload bytes per INPUT")
	pollInterval := flag.Duration("poll", 250*time.Millisecond, "IsHungAppWindow poll interval")
	quiet := flag.Bool("quiet", false, "suppress per-send log lines")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: stressghostty <session> [flags]")
		os.Exit(2)
	}
	sessionName := flag.Arg(0)

	pipePath, _, err := resolveSession(sessionName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve: %v\n", err)
		os.Exit(1)
	}
	hwnd, err := hwndOfSession(sessionName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hwnd: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr,
		"stressghostty: session=%s pipe=%s hwnd=0x%x rate=%d/sec duration=%s size=%d\n",
		sessionName, pipePath, hwnd, *rate, duration.String(), *size)

	var (
		sentCount     int64
		ackedCount    int64
		droppedCount  int64
		errorCount    int64
		firstHangAt   time.Time
		hangObserved  bool
	)

	stop := make(chan struct{})

	// Hang-observer goroutine.
	go func() {
		ticker := time.NewTicker(*pollInterval)
		defer ticker.Stop()
		prevHung := false
		startTime := time.Now()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				hung := isHungAppWindow(hwnd)
				if hung != prevHung {
					if hung {
						fmt.Fprintf(os.Stderr, "[%s] HANG DETECTED at t=%s (sent=%d acked=%d dropped=%d)\n",
							time.Now().Format("15:04:05.000"),
							time.Since(startTime).Round(time.Millisecond),
							atomic.LoadInt64(&sentCount),
							atomic.LoadInt64(&ackedCount),
							atomic.LoadInt64(&droppedCount))
						if firstHangAt.IsZero() {
							firstHangAt = time.Now()
							hangObserved = true
						}
					} else {
						fmt.Fprintf(os.Stderr, "[%s] hang CLEARED at t=%s\n",
							time.Now().Format("15:04:05.000"),
							time.Since(startTime).Round(time.Millisecond))
					}
					prevHung = hung
				}
			}
		}
	}()

	// Sender worker — one per goroutine, each with its own ticker.
	// Each worker dials a fresh pipe per INPUT (1-shot CP mode). This
	// mirrors how most clients actually talk to the CP server.
	deadline := time.Now().Add(*duration)
	payload := strings.Repeat("x", *size)
	interval := time.Second / time.Duration(*rate)
	if interval <= 0 {
		interval = 1 * time.Microsecond
	}

	var wg sync.WaitGroup
	wg.Add(*workers)
	for w := 0; w < *workers; w++ {
		go func(id int) {
			defer wg.Done()
			t := time.NewTicker(interval)
			defer t.Stop()
			for time.Now().Before(deadline) {
				<-t.C
				atomic.AddInt64(&sentCount, 1)
				rtt, resp, err := sendInputRaw(pipePath, payload)
				if err != nil {
					atomic.AddInt64(&errorCount, 1)
					if !*quiet {
						fmt.Fprintf(os.Stderr, "  w%d send error: %v (rtt=%s)\n",
							id, err, rtt.Round(time.Microsecond))
					}
					continue
				}
				switch {
				case strings.HasPrefix(resp, "ACK|INPUT"):
					atomic.AddInt64(&ackedCount, 1)
				case strings.Contains(resp, "dropped"):
					atomic.AddInt64(&droppedCount, 1)
					if !*quiet {
						fmt.Fprintf(os.Stderr, "  w%d DROPPED: %s (rtt=%s)\n",
							id, resp, rtt.Round(time.Microsecond))
					}
				default:
					atomic.AddInt64(&ackedCount, 1)
				}
			}
		}(w)
	}
	wg.Wait()
	close(stop)

	fmt.Fprintf(os.Stderr, "---\n")
	fmt.Fprintf(os.Stderr, "FINAL sent=%d acked=%d dropped=%d error=%d hang_observed=%t hang_first_at=%s\n",
		atomic.LoadInt64(&sentCount),
		atomic.LoadInt64(&ackedCount),
		atomic.LoadInt64(&droppedCount),
		atomic.LoadInt64(&errorCount),
		hangObserved,
		firstHangAt.Format("15:04:05.000"))
	// Machine-readable summary on stdout.
	fmt.Printf("session=%s rate=%d duration=%s sent=%d acked=%d dropped=%d error=%d hang=%t\n",
		sessionName, *rate, duration.String(),
		sentCount, ackedCount, droppedCount, errorCount, hangObserved)

	if hangObserved {
		os.Exit(3) // distinct exit for hang — CI can gate on this
	}
}
