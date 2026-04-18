package cmd

import (
	"strings"
	"testing"
)

// TestBuildTimestampedMessage asserts timestamp prefix is injected by default.
func TestBuildTimestampedMessage(t *testing.T) {
	now := fixedNow()
	msg := "run the tests"
	got := buildTimestampedMessage(msg, now)
	// Expected: "[2026-04-19 Sun 08:10 JST] run the tests"
	if !strings.HasPrefix(got, "[2026-04-19 Sun 08:10 JST]") {
		t.Errorf("message should start with JST timestamp prefix, got %q", got)
	}
	if !strings.HasSuffix(got, msg) {
		t.Errorf("message should end with original message, got %q", got)
	}
}

// TestBuildTimestampedMessage_Format verifies the timestamp bracket format exactly.
func TestBuildTimestampedMessage_Format(t *testing.T) {
	now := fixedNow()
	got := buildTimestampedMessage("hello", now)
	if got != "[2026-04-19 Sun 08:10 JST] hello" {
		t.Errorf("got %q, want %q", got, "[2026-04-19 Sun 08:10 JST] hello")
	}
}
