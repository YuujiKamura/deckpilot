package daemon

import "time"

// SessionSnapshot is the minimal read-only view that the callback-registration
// policy consults. It is deliberately decoupled from *Watcher / *Daemon so the
// policy can be tested as a pure function without constructing live watchers,
// pipes, or goroutines.
type SessionSnapshot struct {
	Name       string
	Status     string
	LastPollOK time.Time
}

// ResolveCaller maps a raw caller identifier (WT GUID, PID, session name, etc.)
// to a concrete session name drawn from the given snapshots, or "" if it cannot
// be resolved.
//
// Resolution order:
//  1. Exact session-name match.
//  2. Any snapshot whose status is "active".
//  3. Snapshot with the most recent LastPollOK.
//
// The heuristic fallbacks are lossy by design — when the raw caller does not
// correspond to a tracked watcher (e.g. an external bash / Windows Terminal
// tab), the best we can do is guess. Callers that need strict identity must
// check the returned value against their invariants (see
// [ShouldRegisterCallback] for the self-loop guard that compensates for this).
func ResolveCaller(caller string, snapshots []SessionSnapshot) string {
	if caller == "" {
		return ""
	}

	for _, s := range snapshots {
		if s.Name == caller {
			return s.Name
		}
	}

	for _, s := range snapshots {
		if s.Status == "active" {
			return s.Name
		}
	}

	var best string
	var bestT time.Time
	for _, s := range snapshots {
		if !s.LastPollOK.IsZero() && s.LastPollOK.After(bestT) {
			bestT = s.LastPollOK
			best = s.Name
		}
	}
	return best
}

// CallbackDecision is the result of ShouldRegisterCallback. Register indicates
// whether an idle callback hook should be auto-registered; ActualCaller is the
// resolved caller session (possibly empty on rejection); Reason is a short
// machine-readable tag useful for logging and tests.
type CallbackDecision struct {
	Register     bool
	ActualCaller string
	Reason       string
}

// Callback-decision Reason values.
const (
	CallbackRejectSameRaw        = "same_raw"         // raw caller equals target
	CallbackRejectUnresolved     = "unresolved"       // caller could not be resolved
	CallbackRejectHeuristicLoop  = "heuristic_loop"   // resolved caller aliases back to target (issue #19)
	CallbackRejectTargetNotFound = "target_not_found" // target session is not in snapshots
	CallbackAccept               = "ok"
)

// ShouldRegisterCallback decides whether a send from caller -> target should
// auto-register an idle callback hook. It is a pure function of the target,
// caller, and a snapshot of the current session registry.
//
// Rules, applied in order:
//  1. Reject when the raw caller equals the target (same-session send).
//  2. Reject when the caller cannot be resolved at all.
//  3. Reject when the resolved caller aliases back to the target — the
//     heuristic self-loop that produced issue #19. External callers (no
//     watcher for them) cause [ResolveCaller] to fall back to "most recently
//     active", which immediately after a send IS the target itself.
//  4. Reject when the target session is not in the snapshots.
//  5. Otherwise accept.
func ShouldRegisterCallback(target, caller string, snapshots []SessionSnapshot) CallbackDecision {
	if target == caller {
		return CallbackDecision{Register: false, Reason: CallbackRejectSameRaw}
	}

	actualCaller := ResolveCaller(caller, snapshots)
	if actualCaller == "" {
		return CallbackDecision{Register: false, Reason: CallbackRejectUnresolved}
	}
	if actualCaller == target {
		return CallbackDecision{Register: false, ActualCaller: actualCaller, Reason: CallbackRejectHeuristicLoop}
	}

	found := false
	for _, s := range snapshots {
		if s.Name == target {
			found = true
			break
		}
	}
	if !found {
		return CallbackDecision{Register: false, ActualCaller: actualCaller, Reason: CallbackRejectTargetNotFound}
	}

	return CallbackDecision{Register: true, ActualCaller: actualCaller, Reason: CallbackAccept}
}
