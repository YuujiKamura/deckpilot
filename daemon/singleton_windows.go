package daemon

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// kernel32 + CreateMutexW/CloseHandle are loaded the same way winapi_windows.go
// loads user32.IsHungAppWindow so the lazy-dll pattern stays consistent.
var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
	procCloseHandle  = kernel32.NewProc("CloseHandle")
)

const errAlreadyExists syscall.Errno = 183

// singletonMutexName returns the kernel object name used to enforce
// "at most one daemon process per machine". The Global\ prefix scopes the
// mutex across all login sessions so a second daemon launched from any
// shell/user is blocked. DECKPILOT_SINGLETON_MUTEX_SUFFIX overrides the
// suffix so parallel test runs do not clobber the production mutex.
func singletonMutexName() string {
	if s := os.Getenv("DECKPILOT_SINGLETON_MUTEX_SUFFIX"); s != "" {
		return `Global\deckpilot-daemon-singleton-` + s
	}
	return `Global\deckpilot-daemon-singleton`
}

// acquireSingletonMutex tries to obtain ownership of the named singleton
// mutex. The returned handle MUST be closed by the caller (typically via
// defer releaseSingletonMutex(h)) for the lifetime of the daemon process.
// alreadyExists==true means another process already created the same
// mutex — the caller should treat this as "another daemon instance is
// starting up concurrently" and refuse to proceed.
func acquireSingletonMutex() (handle syscall.Handle, alreadyExists bool, err error) {
	name, err := syscall.UTF16PtrFromString(singletonMutexName())
	if err != nil {
		return 0, false, fmt.Errorf("singleton: encode name: %w", err)
	}
	r, _, lastErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if r == 0 {
		return 0, false, fmt.Errorf("singleton: CreateMutexW failed: %w", lastErr)
	}
	// CreateMutex always returns a valid handle, even when the mutex
	// already exists; the only way to distinguish "I created it" from
	// "someone else already had it" is GetLastError == ERROR_ALREADY_EXISTS.
	if errno, ok := lastErr.(syscall.Errno); ok && errno == errAlreadyExists {
		return syscall.Handle(r), true, nil
	}
	return syscall.Handle(r), false, nil
}

// releaseSingletonMutex closes the singleton mutex handle. Safe to call
// with a zero handle (no-op).
func releaseSingletonMutex(h syscall.Handle) {
	if h == 0 {
		return
	}
	_, _, _ = procCloseHandle.Call(uintptr(h))
}
