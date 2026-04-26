package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// withIdleHooksHome replaces the daemon's UserHomeDir seam with a temp
// dir for the duration of the test, so the persistence tree is fully
// isolated. Mirrors cmd/launchmeta_test.go's withMetaHome.
func withIdleHooksHome(t *testing.T) string {
	t.Helper()
	tempHome := t.TempDir()
	old := daemonUserHome
	t.Cleanup(func() { daemonUserHome = old })
	daemonUserHome = func() (string, error) { return tempHome, nil }
	return tempHome
}

// TestAddIdleHook_PersistsToFile pins that AddIdleHook writes a JSON
// file under ~/.deckpilot/idle-hooks/<id>.json — the core promise of
// issue #31.
func TestAddIdleHook_PersistsToFile(t *testing.T) {
	home := withIdleHooksHome(t)

	d := New()
	hook := IdleHook{
		Type:          "stdout",
		Message:       "hello",
		SessionFilter: "ghostty-1",
	}
	d.AddIdleHook(hook)

	hooks := d.ListIdleHooks()
	if len(hooks) != 1 {
		t.Fatalf("expected 1 in-memory hook, got %d", len(hooks))
	}
	id := hooks[0].ID
	if id == "" {
		t.Fatal("AddIdleHook must assign an ID for persistence")
	}

	path := filepath.Join(home, ".deckpilot", "idle-hooks", id+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected sidecar JSON at %s, err=%v", path, err)
	}
}

// TestNew_RestoresIdleHooksFromDisk pins the daemon-restart invariant.
// AddIdleHook on one daemon instance, then a fresh New() must rehydrate
// the same hook from disk. Without this, `deckpilot notify add ...`
// silently dies on every restart.
func TestNew_RestoresIdleHooksFromDisk(t *testing.T) {
	withIdleHooksHome(t)

	d1 := New()
	d1.AddIdleHook(IdleHook{
		Type:          "callback",
		SessionFilter: "target",
		// Concrete CallbackSession so the round-trip checks more than just Type.
		CallbackSession: "caller",
		OneTime:         false,
	})

	// Simulate a daemon restart: discard d1, build a fresh Daemon.
	d2 := New()
	got := d2.ListIdleHooks()
	if len(got) != 1 {
		t.Fatalf("expected 1 restored hook, got %d", len(got))
	}
	if got[0].Type != "callback" || got[0].SessionFilter != "target" || got[0].CallbackSession != "caller" {
		t.Errorf("restored hook lost fields: %+v", got[0])
	}
	if got[0].ID == "" {
		t.Error("restored hook should keep its ID")
	}
}

// TestRemoveIdleHooks_DeletesAllFiles pins that RemoveIdleHooks wipes
// the on-disk tree as well as the in-memory slice. Otherwise `notify
// remove` lies — the hooks come back next restart.
func TestRemoveIdleHooks_DeletesAllFiles(t *testing.T) {
	home := withIdleHooksHome(t)

	d := New()
	d.AddIdleHook(IdleHook{Type: "stdout", Message: "a"})
	d.AddIdleHook(IdleHook{Type: "stdout", Message: "b"})

	dir := filepath.Join(home, ".deckpilot", "idle-hooks")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("setup: expected 2 sidecar files, got %d", len(entries))
	}

	d.RemoveIdleHooks()

	if hooks := d.ListIdleHooks(); len(hooks) != 0 {
		t.Errorf("in-memory hooks not cleared: %d remain", len(hooks))
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("sidecar files not cleared: %d remain", len(entries))
	}
}

// TestExecuteStatusHooks_OneTimeFiresAndDeletesFile pins the persistence
// half of the OneTime contract: when a one-time hook fires, both the
// in-memory entry AND the sidecar JSON must vanish, otherwise the next
// daemon restart will resurrect the already-spent hook and re-fire it.
func TestExecuteStatusHooks_OneTimeFiresAndDeletesFile(t *testing.T) {
	home := withIdleHooksHome(t)

	d := New()
	callerW := NewWatcher("caller", "p", "f", 1, "", nil)
	d.mu.Lock()
	d.watchers["caller"] = callerW
	d.mu.Unlock()

	d.AddIdleHook(IdleHook{
		Type:            "callback",
		SessionFilter:   "target",
		CallbackSession: "caller",
		OneTime:         true,
	})

	// Capture the pre-fire path so we can assert it disappears.
	hooks := d.ListIdleHooks()
	if len(hooks) != 1 {
		t.Fatalf("setup: expected 1 hook, got %d", len(hooks))
	}
	preFirePath := filepath.Join(home, ".deckpilot", "idle-hooks", hooks[0].ID+".json")
	if _, err := os.Stat(preFirePath); err != nil {
		t.Fatalf("setup: sidecar must exist before fire, err=%v", err)
	}

	var got int32
	stop := drainCaller(callerW, &got, nil, nil)
	defer stop()

	d.executeStatusHooks(newIdleNotification("target"))
	time.Sleep(150 * time.Millisecond) // let the goroutine + cleanup land

	if atomic.LoadInt32(&got) != 1 {
		t.Errorf("expected 1 callback fire, got %d", got)
	}
	if hooks := d.ListIdleHooks(); len(hooks) != 0 {
		t.Errorf("in-memory hook not pruned: %d remain", len(hooks))
	}
	if _, err := os.Stat(preFirePath); !os.IsNotExist(err) {
		t.Errorf("sidecar must be deleted after one-time fire, stat err=%v", err)
	}
}

// TestAddIdleHook_PersistFailureIsNonFatal pins that a read-only
// idle-hooks dir does not crash AddIdleHook — the in-memory state
// must still get the hook, only persistence is degraded. A panic here
// would mean a misconfigured machine could brick `deckpilot notify`.
func TestAddIdleHook_PersistFailureIsNonFatal(t *testing.T) {
	if runtime.GOOS != "windows" {
		// chmod-based read-only dir test is POSIX; on Windows we use a
		// different strategy: force the home dir to point at a *file*
		// path so MkdirAll fails. Same intent — write is impossible.
	}
	tempHome := t.TempDir()
	// Make the home dir resolution point to a regular file so any
	// MkdirAll under it fails on every platform.
	blockedFile := filepath.Join(tempHome, "blocked")
	if err := os.WriteFile(blockedFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	old := daemonUserHome
	t.Cleanup(func() { daemonUserHome = old })
	daemonUserHome = func() (string, error) { return blockedFile, nil }

	d := New()
	// AddIdleHook must not panic, even though MkdirAll(blockedFile/.deckpilot/idle-hooks)
	// will fail because blockedFile is a regular file.
	d.AddIdleHook(IdleHook{Type: "stdout", Message: "best-effort"})

	if hooks := d.ListIdleHooks(); len(hooks) != 1 {
		t.Fatalf("AddIdleHook lost the hook on persistence failure: %d in memory", len(hooks))
	}
}

// TestNew_BadJSONFileSkipped pins that a corrupt sidecar JSON does not
// stop the daemon from starting — the offending file is skipped with
// a warning and the rest of the directory loads normally. Otherwise a
// single hand-edited typo would brick the daemon's boot.
func TestNew_BadJSONFileSkipped(t *testing.T) {
	home := withIdleHooksHome(t)
	dir := filepath.Join(home, ".deckpilot", "idle-hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := IdleHook{ID: "good-1", Type: "stdout", Message: "ok"}
	body := `{"id":"good-1","type":"stdout","message":"ok"}`
	if err := os.WriteFile(filepath.Join(dir, "good-1.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	d := New()
	hooks := d.ListIdleHooks()
	if len(hooks) != 1 {
		t.Fatalf("expected 1 surviving hook, got %d (broken.json should be skipped)", len(hooks))
	}
	if hooks[0].ID != good.ID || hooks[0].Type != good.Type || hooks[0].Message != good.Message {
		t.Errorf("good hook lost fields: %+v", hooks[0])
	}
}

// TestLoadIdleHooks_BackfillsIDFromFilename pins that a file written
// with no embedded ID still loads cleanly using the filename as the ID
// fallback. Lets operators hand-author hooks without managing IDs.
func TestLoadIdleHooks_BackfillsIDFromFilename(t *testing.T) {
	home := withIdleHooksHome(t)
	dir := filepath.Join(home, ".deckpilot", "idle-hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// JSON with no "id" key.
	body := `{"type":"stdout","message":"hi"}`
	if err := os.WriteFile(filepath.Join(dir, "hand-written.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	hooks := loadIdleHooks()
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks))
	}
	if hooks[0].ID != "hand-written" {
		t.Errorf("expected ID backfilled to filename stem, got %q", hooks[0].ID)
	}
}
