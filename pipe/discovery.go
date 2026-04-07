package pipe

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Session represents a discovered Ghostty control plane session.
type Session struct {
	Name        string
	PipePath    string
	PID         int
	HWND        string
	LogFile     string
	SessionFile string
	AppRuntime  string
}

// Discover finds active Ghostty CP sessions.
func Discover() ([]Session, error) {
	sessions, _ := discoverFromSessionFiles()
	if len(sessions) > 0 {
		return sessions, nil
	}
	return discoverFromProcesses()
}

func discoverFromSessionFiles() ([]Session, error) {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return nil, fmt.Errorf("LOCALAPPDATA not set")
	}

	var sessions []Session
	var cleaned int
	for _, subdir := range []string{"winui3", "win32"} {
		sessDir := filepath.Join(localAppData, "ghostty", "control-plane", subdir, "sessions")
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
				s.AppRuntime = subdir
				sessions = append(sessions, s)
				continue
			}
			// -------------------------------------------

			if !isProcessAlive(s.PID) {
				if err := os.Remove(path); err == nil {
					cleaned++
				}
				continue
			}
			if err := Ping(s.PipePath); err != nil {
				continue
			}
			s.AppRuntime = subdir
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

	var sessions []Session
	for _, pid := range pids {
		pipePath := fmt.Sprintf(`\\.\pipe\ghostty-winui3-ghostty-%d-%d`, pid, pid)
		appRuntime := "winui3"
		if err := Ping(pipePath); err != nil {
			pipePath = fmt.Sprintf(`\\.\pipe\ghostty-win32-ghostty-%d-%d`, pid, pid)
			appRuntime = "win32"
			if err := Ping(pipePath); err != nil {
				continue
			}
		}
		sessions = append(sessions, Session{
			Name:       fmt.Sprintf("ghostty-%d", pid),
			PipePath:   pipePath,
			PID:        pid,
			AppRuntime: appRuntime,
		})
	}
	return sessions, nil
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
		case "pid":
			s.PID, _ = strconv.Atoi(v)
		case "hwnd":
			s.HWND = v
		case "log_file":
			s.LogFile = v
		}
	}
	if s.PipePath == "" {
		return Session{}, fmt.Errorf("no pipe_path in %s", path)
	}
	return s, nil
}

func ensurePipePrefix(p string) string {
	if strings.HasPrefix(p, `\\.\pipe\`) {
		return p
	}
	return `\\.\pipe\` + strings.TrimPrefix(p, `\\.\pipe\`)
}

func isProcessAlive(pid int) bool {
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
