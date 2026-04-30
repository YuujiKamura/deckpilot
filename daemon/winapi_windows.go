package daemon

import (
	"strconv"
	"strings"
	"syscall"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procIsHungAppWindow = user32.NewProc("IsHungAppWindow")
)

// IsHungAppWindow checks if the given window is not responding to messages.
func IsHungAppWindow(hwnd syscall.Handle) bool {
	if hwnd == 0 {
		return false
	}
	ret, _, _ := procIsHungAppWindow.Call(uintptr(hwnd))
	return ret != 0
}

// ParseHWND converts a hex string (e.g. "0x97928D4") to a syscall.Handle.
func ParseHWND(s string) syscall.Handle {
	if s == "" {
		return 0
	}
	// Support both "0x" prefix and raw hex
	hexStr := strings.TrimPrefix(s, "0x")
	u, err := strconv.ParseUint(hexStr, 16, 64)
	if err != nil {
		return 0
	}
	return syscall.Handle(u)
}
