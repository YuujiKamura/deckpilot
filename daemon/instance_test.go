package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

// Every DECKPILOT_INSTANCE value x DECKPILOT_PIPE_SUFFIX / mutex-suffix
// presence, checked on every name the instance switches.
func TestInstanceSwitchesEveryName(t *testing.T) {
	type want struct {
		inst  string // "" = default instance
		valid bool
	}
	values := map[string]want{
		"<unset>":               {"", true},
		"default":               {"", true},
		"canary":                {"canary", true},
		"dev-2":                 {"dev-2", true},
		"9":                     {"9", true},
		"Canary":                {"", false},
		"a b":                   {"", false},
		"../evil":               {"", false},
		"-lead":                 {"", false},
		strings.Repeat("x", 33): {"", false},
	}
	home := t.TempDir()
	prev := daemonUserHome
	daemonUserHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { daemonUserHome = prev })

	for val, w := range values {
		for _, suffix := range []string{"", "testsfx"} {
			t.Run(val+"/suffix="+suffix, func(t *testing.T) {
				if val == "<unset>" {
					t.Setenv("DECKPILOT_INSTANCE", "")
				} else {
					t.Setenv("DECKPILOT_INSTANCE", val)
				}
				t.Setenv("DECKPILOT_PIPE_SUFFIX", suffix)
				t.Setenv("DECKPILOT_SINGLETON_MUTEX_SUFFIX", suffix)

				if got := Instance(); got != w.inst {
					t.Fatalf("Instance() = %q, want %q", got, w.inst)
				}
				_, bad := InstanceInvalid()
				if bad == w.valid {
					t.Fatalf("InstanceInvalid() bad=%v, want %v", bad, !w.valid)
				}

				// pipe + mutex: explicit suffix wins, then instance, then default
				tag := suffix
				if tag == "" {
					tag = w.inst
				}
				wantPipe := `\\.\pipe\deckpilot-daemon`
				wantMutex := `Global\deckpilot-daemon-singleton`
				if tag != "" {
					wantPipe += "-" + tag
					wantMutex += "-" + tag
				}
				if got := IPCPipePath(); got != wantPipe {
					t.Errorf("IPCPipePath() = %q, want %q", got, wantPipe)
				}
				if got := singletonMutexName(); got != wantMutex {
					t.Errorf("singletonMutexName() = %q, want %q", got, wantMutex)
				}

				wantDir := filepath.Join(home, ".deckpilot")
				wantLog := "deckpilot-daemon.log"
				wantWS := 8080
				if w.inst != "" {
					wantDir = filepath.Join(home, ".deckpilot", "instances", w.inst)
					wantLog = "deckpilot-daemon-" + w.inst + ".log"
					wantWS = 0
				}
				if got := idleHooksDir(); got != filepath.Join(wantDir, "idle-hooks") {
					t.Errorf("idleHooksDir() = %q", got)
				}
				if got := notificationInboxPath(); got != filepath.Join(wantDir, "notifications.jsonl") {
					t.Errorf("notificationInboxPath() = %q", got)
				}
				if got := instanceLogName(); got != wantLog {
					t.Errorf("instanceLogName() = %q, want %q", got, wantLog)
				}
				if got := New().WSPort; got != wantWS {
					t.Errorf("New().WSPort = %d, want %d", got, wantWS)
				}
			})
		}
	}
}

// Hooks persisted by one instance are not loaded by the other, so a hook
// never fires from two daemons.
func TestIdleHooksDoNotCrossInstances(t *testing.T) {
	home := t.TempDir()
	prev := daemonUserHome
	daemonUserHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { daemonUserHome = prev })

	t.Setenv("DECKPILOT_INSTANCE", "canary")
	if err := writeIdleHookFile(IdleHook{ID: "h-canary", Type: "stdout", Message: "x"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if n := len(loadIdleHooks()); n != 1 {
		t.Fatalf("canary sees %d hooks, want 1", n)
	}
	t.Setenv("DECKPILOT_INSTANCE", "")
	if n := len(loadIdleHooks()); n != 0 {
		t.Fatalf("default instance sees %d canary hooks, want 0", n)
	}
}
