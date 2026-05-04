package daemon

import (
	"log"
	"os"
	"strings"
)

// debugEnabled gates the debugf trace helper. Parsed once from
// DECKPILOT_DEBUG at package init so the hot IPC path only does an
// inlined bool check per call — no env-lookup, no mutex. Tests flip
// this directly via withDebug rather than going through the env var,
// because a runtime os.Setenv would not affect the already-parsed flag.
var debugEnabled = parseDebugFlag(os.Getenv("DECKPILOT_DEBUG"))

// parseDebugFlag returns true only for the documented truthy literals.
// We deliberately do NOT accept "2", whitespace-padded values, or
// arbitrary non-empty strings: the flag is operator-facing and silent
// acceptance of typos would make accidental "always-on debug" too easy.
func parseDebugFlag(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// debugf is the trace-level counterpart to log.Printf. When the daemon
// runs without DECKPILOT_DEBUG it returns immediately, which keeps the
// daemon log file from bloating with per-PING / per-SHOW traffic that
// is only useful when actively debugging IPC.
func debugf(format string, v ...any) {
	if !debugEnabled {
		return
	}
	log.Printf(format, v...)
}
