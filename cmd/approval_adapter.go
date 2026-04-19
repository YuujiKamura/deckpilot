// Package cmd — Phase 2 (issue #25) ApprovalAdapter infrastructure.
//
// Phase 0 (mutex) and Phase 1 (post-verify) fixed the "approve can
// corrupt concurrent sends" and "approve thinks it succeeded when the
// modal is still up" bugs at the keystroke-injection layer. What they
// did not fix is that the detector and sender are still a single
// hard-coded path: "grep the buffer for patterns, press Enter". That
// works for Claude's Action Required dialog, is mostly right for
// Gemini's overlay, and is demonstrably wrong for Codex (where some
// variants need `y` + Enter instead of Enter alone — see issue #25
// comment thread).
//
// Phase 2 splits the single path into per-agent ApprovalAdapter
// implementations plus a FallbackAdapter that preserves today's
// generic behavior for agents we haven't yet profiled. Observation
// mode (--observe) runs detection without firing any send, so new
// agent patterns can be discovered safely in production.
package cmd

import (
	"fmt"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// ApprovalAdapter knows how to recognize and accept one specific
// agent's approval UI. Concrete adapters live alongside this file.
type ApprovalAdapter interface {
	// Name returns the adapter identifier used for logs and the
	// --agent / inferAgentFromBuffer resolution.
	Name() string

	// Detect inspects a buffer snapshot (typically the last 15-30 lines)
	// and returns (matched, true) when the approval UI is present. The
	// matched string is the specific pattern that fired, for logging.
	Detect(content string) (matched string, found bool)

	// SendAccept performs the keystroke sequence that closes the modal
	// for this agent. Implementations must route through DaemonSendTry
	// (or an equivalent try-lock aware helper) so they never queue
	// behind a user send — Phase 0/0.5 guarantees still apply.
	SendAccept(session, caller string) error
}

// ---------------------------------------------------------------- adapters

// patternsAdapter is the common implementation shared by
// claudeAdapter / geminiAdapter / codexAdapter / fallbackAdapter.
// The only thing that differs per agent is (a) the pattern list used
// for Detect and (b) the accept sequence used for SendAccept.
type patternsAdapter struct {
	name         string
	patterns     []string // inlined from agentApprovalPatterns at New-time
	acceptSender func(session, caller string) error
}

func (a *patternsAdapter) Name() string { return a.name }

func (a *patternsAdapter) Detect(content string) (string, bool) {
	// Reuse the existing tail-15-lines matcher so Detect and the
	// legacy DetectApprovalPromptForAgent stay behaviorally identical
	// for the same pattern list.
	return matchTail(content, a.patterns, 15)
}

func (a *patternsAdapter) SendAccept(session, caller string) error {
	return a.acceptSender(session, caller)
}

// ---------------------------------------------------------------- senders

// sendEnterViaDaemon is the default accept sequence: empty message
// through the try-lock daemon path, which translates to a bare Enter on
// the PTY. 500ms is the same budget auto-approvals used in Phase 0.5.
func sendEnterViaDaemon(session, caller string) error {
	_, err := daemon.DaemonSendTry(session, "", caller, 500)
	return err
}

// ---------------------------------------------------------------- registry

// NewApprovalAdapter returns the adapter for the named agent. Unknown
// names return the FallbackAdapter, preserving today's generic
// behavior for agents we have not yet profiled — see Gemini's review
// on issue #25 (D: "generic matcher の完全削除は時期尚早").
func NewApprovalAdapter(agent string) ApprovalAdapter {
	switch agent {
	case "claude":
		return &patternsAdapter{
			name:         "claude",
			patterns:     agentApprovalPatterns["claude"],
			acceptSender: sendEnterViaDaemon,
		}
	case "gemini":
		return &patternsAdapter{
			name:         "gemini",
			patterns:     agentApprovalPatterns["gemini"],
			acceptSender: sendEnterViaDaemon,
		}
	case "codex":
		return &patternsAdapter{
			name:         "codex",
			patterns:     agentApprovalPatterns["codex"],
			acceptSender: sendEnterViaDaemon,
		}
	default:
		return newFallbackAdapter()
	}
}

// newFallbackAdapter returns the generic-matcher adapter used when the
// agent cannot be identified. Its pattern set is the union of every
// registered agent's patterns; this trades a higher false-positive
// rate for not being blind against unknown agents.
func newFallbackAdapter() ApprovalAdapter {
	// De-duplicate while preserving the claude-first priority used by
	// the legacy DetectApprovalPrompt.
	seen := map[string]struct{}{}
	order := []string{"claude", "gemini", "codex"}
	var merged []string
	for _, a := range order {
		for _, p := range agentApprovalPatterns[a] {
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			merged = append(merged, p)
		}
	}
	return &patternsAdapter{
		name:         "fallback",
		patterns:     merged,
		acceptSender: sendEnterViaDaemon,
	}
}

// ---------------------------------------------------------------- observe

// ObservationAdapter wraps any ApprovalAdapter and replaces SendAccept
// with a no-op that only logs. Used by --observe mode to discover new
// approval patterns in production without any keystroke injection.
type ObservationAdapter struct {
	Inner  ApprovalAdapter
	Logger func(format string, args ...any)
}

func (o *ObservationAdapter) Name() string {
	return o.Inner.Name() + "-observe"
}

func (o *ObservationAdapter) Detect(content string) (string, bool) {
	return o.Inner.Detect(content)
}

func (o *ObservationAdapter) SendAccept(session, caller string) error {
	if o.Logger != nil {
		o.Logger("observe: would SendAccept session=%q caller=%q (suppressed)", session, caller)
	}
	return nil
}

// Sentinel error returned by SendAccept wrappers that refuse to fire
// because observation mode is in effect. Not returned by the default
// ObservationAdapter — logging-only is the intended behavior — but
// available for callers that want to branch on "did we really send?".
var ErrObservationOnly = fmt.Errorf("observation mode: accept suppressed")
