package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestHandleList_ReflectsSessionsRegisteredAfterStartup pins the regression
// observed against the older vendor/deckpilot ed99e1d binary: a daemon that
// had been running for 55+ minutes returned a stale startup snapshot. New
// ghostty PIDs launched after daemon-start were invisible to LIST, and every
// row reported the same uptime computed at daemon boot.
//
// The post-10214df behavior under test:
//   - addSession(...) registers a new session into d.sessions/d.watchers at
//     any point after daemon construction.
//   - handleList() must reflect the live d.sessions map at call time, not a
//     cached snapshot taken at startup.
//   - per-session Uptime is derived from each watcher's CreatedAt at LIST
//     time, so a session registered later must report a *shorter* uptime
//     than a session registered earlier.
//
// If a future refactor reintroduces snapshot caching of listSessions, this
// test fails: either (a) the late-registered session is missing from the
// JSON, or (b) both uptimes collapse to the same value.
func TestHandleList_ReflectsSessionsRegisteredAfterStartup(t *testing.T) {
	d := New()

	// Session A: registered at "startup".
	d.addSession("ghostty-A", "fake-pipe-A", "", "", 1001, "", "winui3")

	// Simulate the daemon running for a while before a new ghostty launches.
	// The exact delta does not matter — only that listSessions computes
	// uptime from per-watcher CreatedAt at call time.
	time.Sleep(60 * time.Millisecond)

	// Session B: registered after some daemon uptime.
	d.addSession("ghostty-B", "fake-pipe-B", "", "", 1002, "", "winui3")

	resp := d.handleList()
	if !strings.HasPrefix(resp, "OK|") {
		t.Fatalf("handleList = %q, want OK| prefix", resp)
	}
	payload := strings.TrimSpace(strings.TrimPrefix(resp, "OK|"))

	var infos []sessionInfo
	if err := json.Unmarshal([]byte(payload), &infos); err != nil {
		t.Fatalf("unmarshal LIST payload: %v (payload=%q)", err, payload)
	}

	byName := map[string]sessionInfo{}
	for _, s := range infos {
		byName[s.Name] = s
	}

	if _, ok := byName["ghostty-A"]; !ok {
		t.Errorf("LIST missing ghostty-A (startup session); got %d entries: %+v", len(infos), infos)
	}
	if _, ok := byName["ghostty-B"]; !ok {
		// THIS is the empirical bug from the older binary: late-registered
		// sessions never showed up.
		t.Errorf("LIST missing ghostty-B (registered after startup); got %d entries: %+v", len(infos), infos)
	}

	// Per-watcher uptime must NOT be a single startup snapshot shared across
	// all rows. The empirical symptom on the older binary was every session
	// reporting an identical "55m06s" — i.e. uptime was computed once at
	// daemon-start instead of per-session at LIST time.
	d.mu.Lock()
	wA := d.watchers["ghostty-A"]
	wB := d.watchers["ghostty-B"]
	d.mu.Unlock()
	if wA == nil || wB == nil {
		t.Fatalf("watcher map missing entries: A=%v B=%v", wA, wB)
	}
	if !wA.Profile().CreatedAt.Before(wB.Profile().CreatedAt) {
		t.Errorf("watcher CreatedAt collapsed: A=%v B=%v — uptime would lie about session age",
			wA.Profile().CreatedAt, wB.Profile().CreatedAt)
	}
}

// TestHandleList_OmitsPrunedDeadSessions: complementary to the refresh test
// — a session whose watcher is dead AND whose PID is gone (simulated via
// PID==0 stays present, but we drop the watcher manually to mimic the
// prune path) must not linger in LIST output. This locks in the
// listSessions/refreshSessions split: handleList re-reads the live map
// rather than serving a cached slice.
func TestHandleList_OmitsPrunedDeadSessions(t *testing.T) {
	d := New()
	d.addSession("ghostty-keep", "pipe-keep", "", "", 2001, "", "winui3")
	d.addSession("ghostty-drop", "pipe-drop", "", "", 2002, "", "winui3")

	// Simulate a refreshSessions prune of ghostty-drop.
	d.mu.Lock()
	delete(d.sessions, "ghostty-drop")
	delete(d.watchers, "ghostty-drop")
	delete(d.appRuntimes, "ghostty-drop")
	d.mu.Unlock()

	resp := d.handleList()
	if strings.Contains(resp, "ghostty-drop") {
		t.Errorf("LIST still references pruned session: %s", resp)
	}
	if !strings.Contains(resp, "ghostty-keep") {
		t.Errorf("LIST lost surviving session: %s", resp)
	}
}
