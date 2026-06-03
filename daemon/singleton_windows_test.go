//go:build windows

package daemon

import (
	"fmt"
	"testing"
	"time"
)

// uniqueSuffix returns a per-test mutex suffix so parallel test runs do
// not clobber each other's named mutex and so production daemons (which
// hold the unsuffixed mutex) are never disturbed by `go test`.
func uniqueSuffix(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test-%s-%d", t.Name(), time.Now().UnixNano())
}

func TestSingletonMutex_FirstAcquireReportsFresh(t *testing.T) {
	t.Setenv("DECKPILOT_SINGLETON_MUTEX_SUFFIX", uniqueSuffix(t))

	h, exists, err := acquireSingletonMutex()
	if err != nil {
		t.Fatalf("unexpected error on first acquire: %v", err)
	}
	defer releaseSingletonMutex(h)

	if h == 0 {
		t.Fatal("expected non-zero handle on first acquire")
	}
	if exists {
		t.Fatal("first acquire should report alreadyExists=false")
	}
}

func TestSingletonMutex_SecondAcquireDetectsExisting(t *testing.T) {
	t.Setenv("DECKPILOT_SINGLETON_MUTEX_SUFFIX", uniqueSuffix(t))

	first, firstExists, err := acquireSingletonMutex()
	if err != nil {
		t.Fatalf("unexpected error on first acquire: %v", err)
	}
	defer releaseSingletonMutex(first)
	if firstExists {
		t.Fatal("first acquire should report alreadyExists=false")
	}

	second, secondExists, err := acquireSingletonMutex()
	if err != nil {
		t.Fatalf("unexpected error on second acquire: %v", err)
	}
	defer releaseSingletonMutex(second)

	if !secondExists {
		t.Fatal("second acquire must report alreadyExists=true (the singleton invariant)")
	}
}

func TestSingletonMutex_ReleasedHandleAllowsReAcquire(t *testing.T) {
	t.Setenv("DECKPILOT_SINGLETON_MUTEX_SUFFIX", uniqueSuffix(t))

	first, _, err := acquireSingletonMutex()
	if err != nil {
		t.Fatalf("unexpected error on first acquire: %v", err)
	}
	releaseSingletonMutex(first)

	// Once the only handle is closed, the kernel object is destroyed and
	// the next acquire should once again see a fresh mutex.
	second, exists, err := acquireSingletonMutex()
	if err != nil {
		t.Fatalf("unexpected error on re-acquire: %v", err)
	}
	defer releaseSingletonMutex(second)

	if exists {
		t.Fatal("after releasing the only handle, re-acquire should report alreadyExists=false")
	}
}

func TestSingletonMutexName_EnvOverride(t *testing.T) {
	t.Setenv("DECKPILOT_SINGLETON_MUTEX_SUFFIX", "abc123")
	got := singletonMutexName()
	want := `Global\deckpilot-daemon-singleton-abc123`
	if got != want {
		t.Errorf("with suffix env: got %q, want %q", got, want)
	}

	t.Setenv("DECKPILOT_SINGLETON_MUTEX_SUFFIX", "")
	got = singletonMutexName()
	want = `Global\deckpilot-daemon-singleton`
	if got != want {
		t.Errorf("without suffix env: got %q, want %q", got, want)
	}
}
