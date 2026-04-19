// Package cmd — Phase 1 (issue #25) post-verification for auto-approvals.
//
// Phase 0.5 stopped approve from corrupting concurrent user sends, but
// it did not answer the follow-up question: "did the Enter actually
// close the modal?" The original loop assumed success on a fixed 2s
// ticker cadence, which turned out to be too short for Gemini 3 with
// heavy reasoning (TUI repaint lag 200-400ms, sometimes > 1s) and too
// blind for Codex (inline confirmation box that sometimes needs a
// second key press depending on the prompt variant).
//
// This file provides a per-session polling helper that asks the daemon
// for the current buffer and re-runs the same approval-pattern matcher.
// When the pattern has disappeared, the Enter landed and the approve
// loop can reset its state cleanly. When the pattern still matches
// after the deadline, the approve loop counts it as a failure so the
// exponential-backoff ceiling fires sooner rather than after three
// independent re-detections.
package cmd

import (
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// ApprovalVerifier is the minimal surface waitForApprovalResolved needs.
// Production wiring uses daemon.DaemonShow + DetectApprovalPromptForAgent;
// tests substitute a fake that returns scripted buffer contents.
type ApprovalVerifier interface {
	// BufferOf returns the current buffer text for the session. The caller
	// identity is forwarded so the daemon can record last-used tracking.
	BufferOf(session, caller string) (content string, err error)
	// HasPrompt returns true iff the given content still matches the
	// agent's approval-prompt patterns. This mirrors
	// DetectApprovalPromptForAgent.
	HasPrompt(content, agent string) bool
}

// daemonVerifier is the production implementation.
type daemonVerifier struct{}

func (daemonVerifier) BufferOf(session, caller string) (string, error) {
	content, _, err := daemon.DaemonShow(session, "buffer", caller)
	return content, err
}

func (daemonVerifier) HasPrompt(content, agent string) bool {
	_, ok := DetectApprovalPromptForAgent(content, agent)
	return ok
}

// defaultVerifier is swapped out by tests.
var defaultVerifier ApprovalVerifier = daemonVerifier{}

// waitForApprovalResolved polls the session buffer up to the deadline,
// returning true as soon as the agent's approval pattern stops matching
// (i.e. the Enter landed and the modal closed). Returns false if the
// pattern is still present after the deadline, or if every buffer probe
// errored out.
//
// pollInterval must be > 0 and ≤ timeout; the loop runs until the first
// deadline check past the final sample.
func waitForApprovalResolved(
	v ApprovalVerifier,
	session, caller, agent string,
	timeout, pollInterval time.Duration,
) bool {
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	// At least one poll, even if timeout <= 0.
	for {
		content, err := v.BufferOf(session, caller)
		if err == nil && !v.HasPrompt(content, agent) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		// Avoid sleeping past the deadline.
		remaining := time.Until(deadline)
		if remaining < pollInterval {
			time.Sleep(remaining)
		} else {
			time.Sleep(pollInterval)
		}
	}
}
