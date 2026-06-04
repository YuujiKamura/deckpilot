package cmd

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestIsSubmitFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"stuck", errors.New("submit_failed_stuck|404ms"), true},
		{"unconfirmed", errors.New("submit_unconfirmed|timeout_after_2000ms"), true},
		{"pipe", errors.New("connect to daemon: pipe not found"), false},
		{"session", errors.New("session not found: ghostty-123"), false},
		{"text_not_visible", errors.New("text_not_visible|phase1_timeout"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSubmitFailure(c.err); got != c.want {
				t.Fatalf("isSubmitFailure(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestSendWithSubmitRetry_FirstTrySuccess(t *testing.T) {
	sendCalls := 0
	readCalls := 0
	send := func(msg string) (string, error) {
		sendCalls++
		return "OK|submit_ok|ok_cleared|405ms", nil
	}
	readHash := func() (string, error) { readCalls++; return "h", nil }

	result, err := sendWithSubmitRetry(send, readHash, "hello", 3, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "submit_ok") {
		t.Fatalf("result = %q, want submit_ok", result)
	}
	if sendCalls != 1 {
		t.Fatalf("sendCalls = %d, want 1 (no retry on success)", sendCalls)
	}
	if readCalls != 0 {
		t.Fatalf("readCalls = %d, want 0 (no buffer check on success)", readCalls)
	}
}

func TestSendWithSubmitRetry_NonSubmitErrorNoRetry(t *testing.T) {
	sendCalls := 0
	send := func(msg string) (string, error) {
		sendCalls++
		return "", errors.New("session not found: ghostty-9")
	}
	readHash := func() (string, error) {
		t.Fatal("readHash must not be called for a non-submit error")
		return "", nil
	}

	_, err := sendWithSubmitRetry(send, readHash, "hello", 3, 0)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if sendCalls != 1 {
		t.Fatalf("sendCalls = %d, want 1 (no retry on pipe/session error)", sendCalls)
	}
}

func TestSendWithSubmitRetry_RecoversAfterNudge(t *testing.T) {
	sendCalls := 0
	send := func(msg string) (string, error) {
		sendCalls++
		if sendCalls == 1 {
			// first full send: stuck
			return "", errors.New("submit_failed_stuck|404ms")
		}
		// enter nudge: the empty path always reports OK
		if msg != "" {
			t.Fatalf("retry must send empty msg (Enter only), got %q", msg)
		}
		return "OK|sent|enter", nil
	}
	// stuck snapshot "h0", then buffer moves to "h1" after the first nudge.
	readSeq := []string{"h0", "h1"}
	readIdx := 0
	readHash := func() (string, error) {
		h := readSeq[readIdx]
		if readIdx < len(readSeq)-1 {
			readIdx++
		}
		return h, nil
	}

	result, err := sendWithSubmitRetry(send, readHash, "hello", 3, 0)
	if err != nil {
		t.Fatalf("expected recovery, got error: %v", err)
	}
	if !strings.Contains(result, "submit_recovered") {
		t.Fatalf("result = %q, want submit_recovered", result)
	}
	if sendCalls != 2 {
		t.Fatalf("sendCalls = %d, want 2 (full send + 1 nudge)", sendCalls)
	}
}

func TestSendWithSubmitRetry_AllNudgesFail(t *testing.T) {
	sendCalls := 0
	send := func(msg string) (string, error) {
		sendCalls++
		if sendCalls == 1 {
			return "", errors.New("submit_failed_stuck|404ms")
		}
		return "OK|sent|enter", nil
	}
	// buffer never moves from the stuck snapshot.
	readHash := func() (string, error) { return "h0", nil }

	_, err := sendWithSubmitRetry(send, readHash, "hello", 3, 0)
	if err == nil {
		t.Fatal("expected persistent-failure error, got nil")
	}
	if !strings.Contains(err.Error(), "after 3 enter-nudge retries") {
		t.Fatalf("err = %v, want mention of 3 retries", err)
	}
	if sendCalls != 4 {
		t.Fatalf("sendCalls = %d, want 4 (full send + 3 nudges)", sendCalls)
	}
}

func TestSendWithSubmitRetry_PipeErrorMidRetry(t *testing.T) {
	sendCalls := 0
	send := func(msg string) (string, error) {
		sendCalls++
		if sendCalls == 1 {
			return "", errors.New("submit_failed_stuck|404ms")
		}
		// pipe breaks during the first nudge
		return "", errors.New("connect to daemon: pipe closed")
	}
	readHash := func() (string, error) { return "h0", nil }

	_, err := sendWithSubmitRetry(send, readHash, "hello", 3, 0)
	if err == nil {
		t.Fatal("expected pipe error, got nil")
	}
	if !strings.Contains(err.Error(), "pipe closed") {
		t.Fatalf("err = %v, want pipe error surfaced", err)
	}
	if sendCalls != 2 {
		t.Fatalf("sendCalls = %d, want 2 (full send + 1 failed nudge)", sendCalls)
	}
}

func TestSendWithSubmitRetry_NoRecoveryWhenReadFails(t *testing.T) {
	// If the buffer read fails (empty stuckHash), the movement check is
	// disabled and we must NOT report a false recovery.
	sendCalls := 0
	send := func(msg string) (string, error) {
		sendCalls++
		if sendCalls == 1 {
			return "", errors.New("submit_failed_stuck|404ms")
		}
		return "OK|sent|enter", nil
	}
	readHash := func() (string, error) { return "", errors.New("buffer read failed") }

	_, err := sendWithSubmitRetry(send, readHash, "hello", 2, 0)
	if err == nil {
		t.Fatal("expected failure (no false recovery on read error), got nil")
	}
}

func TestKillGhosttyOnLeak_ZeroPID(t *testing.T) {
	// Must be a no-op and never panic for non-positive PIDs.
	killGhosttyOnLeak(0)
	killGhosttyOnLeak(-1)
}

func TestKillGhosttyOnLeak_LiveProcess(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("live-process kill test uses a Windows-specific sleeper")
	}
	// ping -n 30 keeps a child alive ~29s; we kill it and confirm it dies.
	c := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := c.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	pid := c.Process.Pid
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()

	killGhosttyOnLeak(pid)

	select {
	case <-done:
		// process exited as expected
	case <-time.After(5 * time.Second):
		_ = c.Process.Kill() // cleanup
		t.Fatalf("process pid=%d still alive 5s after killGhosttyOnLeak", pid)
	}
}
