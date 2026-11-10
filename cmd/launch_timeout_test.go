package cmd

import (
	"testing"
	"time"
)

func TestParseLaunchArgsReadyTimeout(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		got, err := ParseLaunchArgs([]string{"codex", "wait"}, ".")
		if err != "" {
			t.Fatal(err)
		}
		if got.ReadyTimeout != 2*time.Minute || got.ReadyTimeoutSet {
			t.Fatalf("timeout=%v set=%v", got.ReadyTimeout, got.ReadyTimeoutSet)
		}
	})
	t.Run("explicit", func(t *testing.T) {
		got, err := ParseLaunchArgs([]string{"codex", "wait", "--ready-timeout", "3m15s"}, ".")
		if err != "" {
			t.Fatal(err)
		}
		if got.ReadyTimeout != 195*time.Second || !got.ReadyTimeoutSet {
			t.Fatalf("timeout=%v set=%v", got.ReadyTimeout, got.ReadyTimeoutSet)
		}
	})
	for _, args := range [][]string{{"codex", "wait", "--ready-timeout"}, {"codex", "wait", "--ready-timeout", "0s"}, {"codex", "wait", "--ready-timeout", "nope"}} {
		if _, err := ParseLaunchArgs(args, "."); err == "" {
			t.Errorf("ParseLaunchArgs(%q) accepted invalid timeout", args)
		}
	}
}

func TestOwnedGhosttyPIDRequiresNewSessionIdentity(t *testing.T) {
	known := []string{"ghostty-100"}
	tests := []struct {
		name    string
		session string
		entries []listEntry
		want    int
		ok      bool
	}{
		{"own new session", "ghostty-200", []listEntry{{Name: "ghostty-100", PID: 100}, {Name: "ghostty-200", PID: 200}}, 200, true},
		{"pre-existing session", "ghostty-100", []listEntry{{Name: "ghostty-100", PID: 100}}, 0, false},
		{"pid mismatch", "ghostty-200", []listEntry{{Name: "ghostty-200", PID: 201}}, 0, false},
		{"later session does not change captured identity", "ghostty-200", []listEntry{{Name: "ghostty-100", PID: 100}, {Name: "ghostty-200", PID: 200}, {Name: "ghostty-300", PID: 300}}, 200, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ownedGhosttyPID(tt.session, known, tt.entries)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ownedGhosttyPID()=(%d,%v), want (%d,%v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestNewSessionNamesRejectsAmbiguousDelta(t *testing.T) {
	known := map[string]bool{"ghostty-100": true}
	got := newSessionNames(known, []string{"ghostty-100", "ghostty-200", "ghostty-300"})
	if len(got) != 2 || got[0] != "ghostty-200" || got[1] != "ghostty-300" {
		t.Fatalf("newSessionNames()=%v, want both new sessions", got)
	}
}
