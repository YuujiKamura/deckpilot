package pipe

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"
)

// Session represents a discovered Ghostty control plane session.
type Session struct {
	Name        string
	PipePath    string
	WsURL       string
	PID         int
	HWND        string
	LogFile     string
	SessionFile string
	AppRuntime  string
}

// Discover finds active Ghostty CP sessions.
// PRIMARY: Process scan (reliable for running ghostty.exe)
// FALLBACK: Session files (for persisted metadata)
func Discover() ([]Session, error) {
	sessions, _ := discoverFromProcesses()
	if len(sessions) > 0 {
		return sessions, nil
	}
	return discoverFromSessionFiles()
}

func discoverFromSessionFiles() ([]Session, error) {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return nil, fmt.Errorf("LOCALAPPDATA not set")
	}

	searchDirs := []string{
		filepath.Join(localAppData, "ghostty", "control-plane", "winui3", "sessions"),
		filepath.Join(localAppData, "ghostty", "control-plane", "win32", "sessions"),
		filepath.Join(localAppData, "ghostty", "control-plane", "web", "sessions"),
		filepath.Join(localAppData, "WindowsTerminal", "control-plane", "winui3", "sessions"),
		filepath.Join(localAppData, "Packages", "WindowsTerminalDev_8wekyb3d8bbwe", "LocalCache", "Local", "WindowsTerminal", "control-plane", "winui3", "sessions"),
		filepath.Join(localAppData, "Packages", "Microsoft.WindowsTerminal_8wekyb3d8bbwe", "LocalCache", "Local", "WindowsTerminal", "control-plane", "winui3", "sessions"),
	}

	var sessions []Session
	var cleaned int
	for _, sessDir := range searchDirs {
		matches, err := filepath.Glob(filepath.Join(sessDir, "*.session"))
		if err != nil {
			continue
		}
		for _, path := range matches {
			s, err := parseSessionFile(path)
			if err != nil {
				continue
			}

			// --- THE FIX: Introduce 10s Grace Period ---
			info, err := os.Stat(path)
			if err == nil && time.Since(info.ModTime()) < 10*time.Second {
				if strings.Contains(sessDir, "WindowsTerminal") {
					s.AppRuntime = "wt"
				} else if strings.Contains(sessDir, "win32") {
					s.AppRuntime = "win32"
				} else if strings.Contains(sessDir, "web") {
					s.AppRuntime = "web"
				} else {
					s.AppRuntime = "winui3"
				}
				sessions = append(sessions, s)
				continue
			}
			// -------------------------------------------

			// DISABLED: Don't delete session files based on process alive check
			// This prevents daemon restarts from removing valid session files
			// for processes that are still running.
			//
			// if !isProcessAlive(s.PID) {
			// 	if err := os.Remove(path); err == nil {
			// 		cleaned++
			// 	}
			// 	continue
			// }
			if s.WsURL != "" {
				// Web session: validate via WebSocket ping
				if err := PingWS(s.WsURL); err != nil {
					continue
				}
			} else {
				// Named Pipe session: validate via pipe ping
				if err := Ping(s.PipePath); err != nil {
					continue
				}
			}
			if strings.Contains(sessDir, "WindowsTerminal") {
				s.AppRuntime = "wt"
			} else if strings.Contains(sessDir, "win32") {
				s.AppRuntime = "win32"
			} else if strings.Contains(sessDir, "web") {
				s.AppRuntime = "web"
			} else {
				s.AppRuntime = "winui3"
			}
			sessions = append(sessions, s)
		}
	}
	if cleaned > 0 {
		log.Printf("discovery: cleaned %d stale session files", cleaned)
	}
	return sessions, nil
}

func discoverFromProcesses() ([]Session, error) {
	pids, err := findGhosttyPIDs()
	if err != nil {
		return nil, err
	}
	return probePIDsConcurrent(pids, probePID), nil
}

// probePIDsConcurrent runs probe against every PID in parallel and
// returns the successful Sessions in PID-input order.
//
// Ping uses dialTimeout (2s) + readDeadline (5s), so a serial loop over
// N hung processes can stall pipe.Discover for ~7s × N — long enough to
// blow past the 10s daemon IPC handler deadline and the 15s launch
// waitForNewSession budget. Running probes in parallel collapses
// worst-case latency to one Ping budget regardless of N.
func probePIDsConcurrent(pids []int, probe func(int) (Session, bool)) []Session {
	if len(pids) == 0 {
		return nil
	}
	type result struct {
		idx     int
		session Session
		ok      bool
	}
	results := make([]result, len(pids))
	var wg sync.WaitGroup
	for i, pid := range pids {
		wg.Add(1)
		go func(i, pid int) {
			defer wg.Done()
			s, ok := probe(pid)
			results[i] = result{idx: i, session: s, ok: ok}
		}(i, pid)
	}
	wg.Wait()

	// Preserve PID-input order so callers / tests get deterministic output.
	sort.SliceStable(results, func(i, j int) bool { return results[i].idx < results[j].idx })

	var sessions []Session
	for _, r := range results {
		if r.ok {
			sessions = append(sessions, r.session)
		}
	}
	return sessions
}

// probePID resolves a single ghostty PID to a Session. Returns ok=false
// when neither the winui3 nor win32 control-plane pipe responds within
// the Ping budget. Safe for concurrent use.
func probePID(pid int) (Session, bool) {
	pipePath := fmt.Sprintf(`\\.\pipe\ghostty-winui3-ghostty-%d-%d`, pid, pid)
	appRuntime := "winui3"
	if err := Ping(pipePath); err != nil {
		pipePath = fmt.Sprintf(`\\.\pipe\ghostty-win32-ghostty-%d-%d`, pid, pid)
		appRuntime = "win32"
		if err := Ping(pipePath); err != nil {
			return Session{}, false
		}
	}
	s := Session{
		Name:       fmt.Sprintf("ghostty-%d", pid),
		PipePath:   pipePath,
		PID:        pid,
		AppRuntime: appRuntime,
	}
	// Issue #29 fix: process-based discovery previously returned
	// Session entries with HWND="" and SessionFile="", which meant
	// the Watcher never knew which window to check with
	// IsHungAppWindow — every session looked healthy even when the
	// OS had marked it "Not Responding". Enrich from the ghostty
	// session file (well-known path) so HWND / LogFile / etc. are
	// populated the same way discoverFromSessionFiles() would.
	if enriched, err := enrichFromSessionFile(s, appRuntime); err == nil {
		s = enriched
	}
	return s, true
}

func findGhosttyPIDs() ([]int, error) {
	const TH32CS_SNAPPROCESS = 0x00000002
	snap, err := syscall.CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(snap)

	var pe syscall.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))

	err = syscall.Process32First(snap, &pe)
	if err != nil {
		return nil, err
	}

	var pids []int
	for {
		name := syscall.UTF16ToString(pe.ExeFile[:])
		if strings.EqualFold(name, "ghostty.exe") {
			pids = append(pids, int(pe.ProcessID))
		}
		err = syscall.Process32Next(snap, &pe)
		if err != nil {
			break
		}
	}
	return pids, nil
}

// enrichFromSessionFile finds the Ghostty session file matching the
// given base Session and merges HWND / SessionFile / LogFile /
// SessionName into it. Fields already set on base (Name, PipePath,
// PID, AppRuntime) are preserved — only gaps are filled.
//
// Ghostty writes its session descriptor at
//
//	%LOCALAPPDATA%\ghostty\control-plane\<runtime>\sessions\<name>-<pid>.session
//
// On systems where LOCALAPPDATA is unset or the file is missing, this
// returns an error and the caller keeps the unenriched Session (HWND
// remains empty — hang detection won't work for that session, but the
// pipe-level flows still operate).
func enrichFromSessionFile(base Session, appRuntime string) (Session, error) {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return base, fmt.Errorf("LOCALAPPDATA not set")
	}
	path := fmt.Sprintf(
		`%s\ghostty\control-plane\%s\sessions\%s-%d.session`,
		localAppData, appRuntime, base.Name, base.PID,
	)
	parsed, err := parseSessionFile(path)
	if err != nil {
		return base, err
	}
	// Preserve base fields; pull in the ones discoverFromProcesses
	// could not know about.
	merged := base
	if merged.HWND == "" {
		merged.HWND = parsed.HWND
	}
	if merged.SessionFile == "" {
		merged.SessionFile = parsed.SessionFile
	}
	if merged.LogFile == "" {
		merged.LogFile = parsed.LogFile
	}
	if merged.WsURL == "" {
		merged.WsURL = parsed.WsURL
	}
	return merged, nil
}

func parseSessionFile(path string) (Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, err
	}
	defer f.Close()

	s := Session{SessionFile: path}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "session_name":
			s.Name = v
		case "pipe_path":
			s.PipePath = ensurePipePrefix(v)
		case "ws_url":
			s.WsURL = v
		case "pid":
			s.PID, _ = strconv.Atoi(v)
		case "hwnd":
			s.HWND = v
		case "log_file":
			s.LogFile = v
		}
	}
	if s.PipePath == "" && s.WsURL == "" {
		return Session{}, fmt.Errorf("no pipe_path or ws_url in %s", path)
	}
	return s, nil
}

func ensurePipePrefix(p string) string {
	if strings.HasPrefix(p, `\\.\pipe\`) {
		return p
	}
	return `\\.\pipe\` + strings.TrimPrefix(p, `\\.\pipe\`)
}

func IsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	h, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	return true
}

// PingWS sends a PING command over WebSocket and expects a PONG response.
func PingWS(wsURL string) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: dialTimeout,
	}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("ws dial %s: %w", wsURL, err)
	}
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(readDeadline))
	if err := conn.WriteMessage(websocket.TextMessage, []byte("PING")); err != nil {
		return fmt.Errorf("ws write: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(readDeadline))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("ws read: %w", err)
	}
	if !strings.HasPrefix(string(msg), PrefixPONG) {
		return fmt.Errorf("ws unexpected: %s", string(msg))
	}
	return nil
}
