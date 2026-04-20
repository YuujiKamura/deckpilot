//go:build windows

// Package daemon — Windows process tree walker for the hang detector
// (issue #26). Enumerates processes via CreateToolhelp32Snapshot and
// filters descendants of a given root PID.
//
// This is deliberately kept dependency-free of the rest of the daemon
// package so unit tests can exercise it via a small stubbable interface
// (see ProcessLister below).
package daemon

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcessInfo is the minimal process record the hang detector needs.
// Name is kept as-is from PROCESSENTRY32.szExeFile (short name with
// extension, e.g. "daemon.test.exe").
type ProcessInfo struct {
	PID  uint32
	PPID uint32
	Name string
}

// ProcessLister abstracts process enumeration so HangDetector can be
// tested with a static list instead of hitting the real OS. In
// production, use SnapshotProcesses.
type ProcessLister interface {
	List() ([]ProcessInfo, error)
}

// SystemProcessLister is the production implementation that queries
// the Windows kernel via Toolhelp snapshot.
type SystemProcessLister struct{}

// List returns every process currently known to the kernel. Heavy-ish
// call (one syscall + userspace walk of the snapshot); callers that
// poll should reuse a single lister instance but still call List on
// each probe — snapshots are point-in-time, not cached.
func (SystemProcessLister) List() ([]ProcessInfo, error) {
	const TH32CS_SNAPPROCESS = 0x00000002
	h, err := windows.CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("create snapshot: %w", err)
	}
	defer windows.CloseHandle(h)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	if err := windows.Process32First(h, &entry); err != nil {
		return nil, fmt.Errorf("process32first: %w", err)
	}

	var out []ProcessInfo
	for {
		out = append(out, ProcessInfo{
			PID:  entry.ProcessID,
			PPID: entry.ParentProcessID,
			Name: syscall.UTF16ToString(entry.ExeFile[:]),
		})
		if err := windows.Process32Next(h, &entry); err != nil {
			// ERROR_NO_MORE_FILES signals end of iteration.
			break
		}
	}
	return out, nil
}

// GetProcessTree returns the root PID and every descendant process
// currently alive. The first element is always the root (if found).
// Depth is unbounded; cycles are impossible in the Windows PPID graph
// but we cap iterations at 2×len(all) as a paranoid safety.
func GetProcessTree(lister ProcessLister, rootPID uint32) ([]ProcessInfo, error) {
	all, err := lister.List()
	if err != nil {
		return nil, err
	}

	byPPID := make(map[uint32][]ProcessInfo, len(all))
	var root *ProcessInfo
	for i := range all {
		byPPID[all[i].PPID] = append(byPPID[all[i].PPID], all[i])
		if all[i].PID == rootPID {
			root = &all[i]
		}
	}
	if root == nil {
		return nil, fmt.Errorf("root pid %d not found", rootPID)
	}

	// BFS from root.
	result := []ProcessInfo{*root}
	queue := []uint32{rootPID}
	maxIter := 2 * len(all)
	for len(queue) > 0 && maxIter > 0 {
		maxIter--
		next := queue[0]
		queue = queue[1:]
		children := byPPID[next]
		for _, c := range children {
			result = append(result, c)
			queue = append(queue, c.PID)
		}
	}
	return result, nil
}
