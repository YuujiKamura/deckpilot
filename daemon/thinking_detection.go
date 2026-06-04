package daemon

import "strings"

// LooksActivelyThinking reports whether a TUI buffer shows an agent that is
// still mid-task, as opposed to one parked at its ready prompt.
//
// It keys off the single signal that survives the agent's per-frame churn: the
// spinner line is rendered as "<glyph> <word>…" with a trailing ellipsis while
// a turn is in progress. Claude cycles BOTH the glyph (✶ ✻ ✳ …) and the gerund
// word (Boondoggling, Manifesting, Sautéing, …) every frame, so neither is
// matchable — but the trailing "…" that means "in progress" is constant. A
// finished turn renders "<glyph> <word> for Ns" (past tense, no "…") and the
// prompt returns to an empty "❯ ". Matching the ellipsis instead of any fixed
// phrase is deliberate: hardcoded markers rot when wording changes (the same
// trap that silently broke trust-dialog detection, 2026-06-04).
//
// Used to gate hang detection: a frozen, low-CPU buffer is only a *stall* when
// the agent is still thinking; the same shape at the ready prompt is just
// "done" and must not raise a false alarm.
func LooksActivelyThinking(buf string) bool {
	for _, line := range strings.Split(buf, "\n") {
		t := strings.TrimRight(line, " \r\t")
		if t == "" {
			continue
		}
		// The spinner line is short ("✳ Manifesting…"). A long content line
		// that merely happens to end in an ellipsis is ordinary output, not
		// the progress spinner — skip it to avoid false "still thinking".
		if len([]rune(t)) > 60 {
			continue
		}
		if strings.HasSuffix(t, "…") || strings.HasSuffix(t, "...") {
			return true
		}
	}
	return false
}
