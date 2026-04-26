package daemon

import (
	"os"
	"testing"
)

// TestMain isolates the daemon test suite from the developer's real
// ~/.deckpilot/idle-hooks tree. Issue #31 made daemon.New() load
// persisted hooks from disk; without this isolation, every test that
// calls New() (callback_test, hook_invariants_test, agent_to_agent_test,
// wsserver_test, etc.) would inherit whatever hooks the dev machine
// happens to have configured, breaking deterministic counts in
// invariants like "OneTime fires exactly once".
//
// Tests that need to assert on persisted files specifically still call
// withIdleHooksHome to swap to their own per-test temp dir on top of
// this global override.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "deckpilot-daemon-test-home-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)
	old := daemonUserHome
	daemonUserHome = func() (string, error) { return tmp, nil }
	defer func() { daemonUserHome = old }()
	os.Exit(m.Run())
}
