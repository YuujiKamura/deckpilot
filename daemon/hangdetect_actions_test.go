package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

func withFakeKill(t *testing.T, killed *[]uint32, wantErr error) (restore func()) {
	t.Helper()
	orig := killProcessFn
	killProcessFn = func(pid uint32) error {
		*killed = append(*killed, pid)
		return wantErr
	}
	return func() { killProcessFn = orig }
}

// T1 ActionNotify_LogsToStderr
func TestActionNotify_LogsToStderr(t *testing.T) {
	// Redirect stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	
	// Also redirect log output because log.Printf writes to stderr by default
	oldLogOut := log.Writer()
	log.SetOutput(w)
	
	defer func() {
		os.Stderr = oldStderr
		log.SetOutput(oldLogOut)
	}()

	info := HangInfo{
		SessionName:  "test-sess",
		RootPID:      1234,
		ProcessCount: 5,
		CPUPercent:   0.5,
		StallSince:   100 * time.Second,
	}

	action := ActionNotify(nil)
	err := action(context.Background(), info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	checks := []string{"[hang]", "pid=1234", "cpu=0.50", "stall=1m40s"}
	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("output missing %q: %q", check, output)
		}
	}
}

// T2 ActionNotify_NilDaemonSafe
func TestActionNotify_NilDaemonSafe(t *testing.T) {
	// Already implicitly tested in T1, but making it explicit
	info := HangInfo{SessionName: "s"}
	action := ActionNotify(nil)
	err := action(context.Background(), info)
	if err != nil {
		t.Errorf("ActionNotify(nil) returned error: %v", err)
	}
}

// T3 ActionKillChild_PicksFirstNonGhostty
func TestActionKillChild_PicksFirstNonGhostty(t *testing.T) {
	stub := stubProcessLister{
		list: []ProcessInfo{
			{PID: 100, PPID: 0, Name: "ghostty.exe"},
			{PID: 200, PPID: 100, Name: "daemon.test.exe"},
			{PID: 300, PPID: 200, Name: "cmd.exe"},
		},
	}
	
	var killed []uint32
	defer withFakeKill(t, &killed, nil)()

	info := HangInfo{RootPID: 100, SessionName: "s"}
	action := ActionKillChild(stub)
	err := action(context.Background(), info)
	if err != nil {
		t.Fatalf("ActionKillChild failed: %v", err)
	}

	if len(killed) != 1 || killed[0] != 200 {
		t.Errorf("expected PID 200 to be killed, got %v", killed)
	}
}

// T4 ActionKillChild_ExcludesGhosttyEvenIfDescendant
func TestActionKillChild_ExcludesGhosttyEvenIfDescendant(t *testing.T) {
	stub := stubProcessLister{
		list: []ProcessInfo{
			{PID: 100, PPID: 0, Name: "ghostty"},
			{PID: 200, PPID: 100, Name: "ghostty-child"},
		},
	}
	
	var killed []uint32
	defer withFakeKill(t, &killed, nil)()

	info := HangInfo{RootPID: 100, SessionName: "s"}
	action := ActionKillChild(stub)
	err := action(context.Background(), info)
	
	if err == nil || !strings.Contains(err.Error(), "no non-ghostty descendant") {
		t.Errorf("expected 'no non-ghostty descendant' error, got %v", err)
	}
}

// T5 ActionKillChild_ListerError
func TestActionKillChild_ListerError(t *testing.T) {
	stub := stubProcessLister{err: fmt.Errorf("tree failed")}
	action := ActionKillChild(stub)
	err := action(context.Background(), HangInfo{RootPID: 100})
	
	if err == nil || !strings.Contains(err.Error(), "tree:") {
		t.Errorf("expected tree error, got %v", err)
	}
}

// T6 ActionKillChild_KillErrorPropagates
func TestActionKillChild_KillErrorPropagates(t *testing.T) {
	stub := stubProcessLister{
		list: []ProcessInfo{
			{PID: 100, Name: "ghostty"},
			{PID: 200, PPID: 100, Name: "target"},
		},
	}
	
	var killed []uint32
	killErr := fmt.Errorf("kill failed")
	defer withFakeKill(t, &killed, killErr)()

	action := ActionKillChild(stub)
	err := action(context.Background(), HangInfo{RootPID: 100})
	
	if err == nil || !strings.Contains(err.Error(), "kill") {
		t.Errorf("expected kill error, got %v", err)
	}
}

// T7 ActionKillSession_CallsKillOnRoot
func TestActionKillSession_CallsKillOnRoot(t *testing.T) {
	var killed []uint32
	defer withFakeKill(t, &killed, nil)()

	action := ActionKillSession()
	err := action(context.Background(), HangInfo{RootPID: 42})
	if err != nil {
		t.Fatalf("ActionKillSession failed: %v", err)
	}

	if len(killed) != 1 || killed[0] != 42 {
		t.Errorf("expected PID 42 to be killed, got %v", killed)
	}
}

// T8 ActionKillSession_KillError
func TestActionKillSession_KillError(t *testing.T) {
	var killed []uint32
	killErr := fmt.Errorf("permission denied")
	defer withFakeKill(t, &killed, killErr)()

	action := ActionKillSession()
	err := action(context.Background(), HangInfo{RootPID: 42})
	
	if err == nil || !strings.Contains(err.Error(), "ActionKillSession: kill 42") {
		t.Errorf("expected detailed kill error, got %v", err)
	}
}

// T9 ActionSendCtrlC_NilDaemon
func TestActionSendCtrlC_NilDaemon(t *testing.T) {
	action := ActionSendCtrlC(nil)
	err := action(context.Background(), HangInfo{SessionName: "s"})
	if err == nil || !strings.Contains(err.Error(), "nil daemon") {
		t.Errorf("expected nil daemon error, got %v", err)
	}
}
