//go:build windows

package daemon

import (
	"fmt"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// CPUSampler measures the CPU utilization (as a 0-100 percent value) of
// one or more processes over a sampling window. The interface lets
// HangDetector use a stub in tests that returns scripted values.
type CPUSampler interface {
	// Sample returns the average CPU percentage used by all given PIDs
	// over the interval. Interval must be > 0. Missing / dead pids are
	// silently skipped — they contribute 0. An overall value > 100 is
	// possible on multi-core machines (we do not normalize by core
	// count; the hang detector cares only about "essentially idle" vs
	// "doing work", both thresholds survive multi-core).
	Sample(pids []uint32, interval time.Duration) (percent float64, err error)
}

// SystemCPUSampler is the production implementation. It calls
// GetProcessTimes before and after a wall-clock sleep, then computes
// (kernel+user delta CPU time) / (wall-clock delta).
type SystemCPUSampler struct{}

// Sample implements the production path described above.
func (SystemCPUSampler) Sample(pids []uint32, interval time.Duration) (float64, error) {
	if interval <= 0 {
		return 0, fmt.Errorf("interval must be > 0")
	}
	if len(pids) == 0 {
		return 0, nil
	}

	start, err := readAggregateCPU(pids)
	if err != nil {
		return 0, err
	}
	startWall := time.Now()
	time.Sleep(interval)
	end, err := readAggregateCPU(pids)
	if err != nil {
		return 0, err
	}
	wall := time.Since(startWall)
	if wall <= 0 {
		return 0, nil
	}

	cpuDelta := end - start
	return float64(cpuDelta) * 100.0 / float64(wall), nil
}

// readAggregateCPU returns the sum of (kernel+user) CPU time across all
// given pids as a time.Duration. Dead / access-denied pids are skipped.
func readAggregateCPU(pids []uint32) (time.Duration, error) {
	var total time.Duration
	for _, pid := range pids {
		cpu, err := processCPUTime(pid)
		if err != nil {
			// Skip dead/protected processes instead of propagating —
			// the hang detector's contract is "best-effort aggregate",
			// not "strict per-pid".
			continue
		}
		total += cpu
	}
	return total, nil
}

// processCPUTime returns kernel+user CPU time consumed by pid so far.
func processCPUTime(pid uint32) (time.Duration, error) {
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	h, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, err
	}
	defer syscall.CloseHandle(h)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(
		windows.Handle(h), &creation, &exit, &kernel, &user,
	); err != nil {
		return 0, err
	}
	return filetimeToDuration(kernel) + filetimeToDuration(user), nil
}

// filetimeToDuration converts a Windows FILETIME (100-ns units) into a
// time.Duration.
func filetimeToDuration(ft windows.Filetime) time.Duration {
	t := int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
	return time.Duration(t) * 100 * time.Nanosecond
}
