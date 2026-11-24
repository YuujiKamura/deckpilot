package cmd

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLaunchWaitOptions(t *testing.T) {
	a, e := ParseLaunchArgs([]string{"python", "print(1)"}, ".")
	if e != "" || a.NoWait || a.WaitTimeout != 90*time.Minute || a.WaitTail != 15 {
		t.Fatalf("defaults: %+v %s", a, e)
	}
	a, e = ParseLaunchArgs([]string{"python", "x", "--no-wait", "--wait-timeout", "1m", "--wait-tail", "3"}, ".")
	if e != "" || !a.NoWait || a.WaitTimeout != time.Minute || a.WaitTail != 3 || a.Prompt != "x" {
		t.Fatalf("options: %+v %s", a, e)
	}
	for _, arg := range [][]string{{"python", "x", "--wait-tail", "0"}, {"python", "x", "--wait-timeout", "bad"}} {
		if _, e := ParseLaunchArgs(arg, "."); e == "" {
			t.Fatalf("accepted %v", arg)
		}
	}
}

func TestFinishLaunchWait(t *testing.T) {
	oldWait, oldShow, oldLookup := launchIdleWait, launchIdleShow, launchIdleLookup
	defer func() { launchIdleWait, launchIdleShow, launchIdleLookup = oldWait, oldShow, oldLookup }()
	calls := 0
	launchIdleShow = func(s, mode, caller, tab string) (string, string, error) {
		calls++
		if s != "session" || tab != "id=t_1" {
			t.Fatalf("wrong target %s %s", s, tab)
		}
		return "first\nlast", "live", nil
	}
	launchIdleLookup = func(string) (string, bool, error) { t.Fatal("window status used for tab"); return "", false, nil }
	launchIdleWait = func(timeout, poll time.Duration, lookup func() (string, bool, error)) (string, bool, error) {
		for i := 0; i < 4; i++ {
			status, _, err := lookup()
			if err != nil {
				t.Fatal(err)
			}
			if i < 3 && status != "active" {
				t.Fatalf("early status %s", status)
			}
			if i == 3 && status != "idle" {
				t.Fatalf("status %s", status)
			}
		}
		return "idle", true, nil
	}
	a := LaunchArgs{WaitTimeout: time.Minute, WaitTail: 1}
	if err := finishLaunchWait(a, "session", "id=t_1"); err != nil {
		t.Fatal(err)
	}
	if calls != 5 {
		t.Fatalf("show calls %d", calls)
	}
	calls = 0
	a.NoWait = true
	if err := finishLaunchWait(a, "session", "id=t_1"); err != nil || calls != 0 {
		t.Fatalf("no wait: %v %d", err, calls)
	}
	a.NoWait = false
	launchIdleWait = func(time.Duration, time.Duration, func() (string, bool, error)) (string, bool, error) {
		return "timeout:active", true, nil
	}
	oldStdout := os.Stdout
	reader, writer, _ := os.Pipe()
	os.Stdout = writer
	err := finishLaunchWait(a, "session", "id=t_1")
	writer.Close()
	os.Stdout = oldStdout
	output, _ := io.ReadAll(reader)
	reader.Close()
	if !strings.Contains(string(output), "last") || strings.Contains(string(output), "first") {
		t.Fatalf("tail output: %s", output)
	}
	if err == nil || !strings.Contains(err.Error(), "not idle within") || calls != 1 {
		t.Fatalf("timeout: %v calls %d", err, calls)
	}
	launchIdleShow = func(string, string, string, string) (string, string, error) { return "", "", errors.New("bad") }
}
