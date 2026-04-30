//go:build windows

package daemon

import (
	"fmt"
	"os"
)

// killPID terminates the given PID using os.FindProcess + Process.Kill.
// On Windows, Kill translates to TerminateProcess — no SIGTERM handshake,
// so callers should reserve this for genuinely hung processes.
func killPID(pid uint32) error {
	p, err := os.FindProcess(int(pid))
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := p.Kill(); err != nil {
		return fmt.Errorf("kill process %d: %w", pid, err)
	}
	return nil
}
