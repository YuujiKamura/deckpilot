package cmd

import (
	"testing"
	"time"
)

// TestFormatLastOutput exercises the relative-time rendering and the hang
// asterisk threshold used by `deckpilot list`'s LAST_OUTPUT column.
func TestFormatLastOutput(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	const hang = 480 // 8 minutes

	tests := []struct {
		name string
		last time.Time
		want string
	}{
		{"zero renders dash", time.Time{}, "-"},
		{"seconds", now.Add(-5 * time.Second), "5s"},
		{"just under one minute", now.Add(-59 * time.Second), "59s"},
		{"minutes truncate seconds", now.Add(-(2*time.Minute + 30*time.Second)), "2m"},
		// An hour-scale stall is always far past the 8m threshold, so it carries
		// the hang asterisk; the un-flagged hour format is covered separately by
		// TestFormatLastOutput_ThresholdDisabled.
		{"hours and minutes flagged", now.Add(-(1*time.Hour + 12*time.Minute)), "1h12m*"},
		{"exactly threshold is not flagged", now.Add(-480 * time.Second), "8m"},
		{"one second over threshold is flagged", now.Add(-481 * time.Second), "8m*"},
		{"well over threshold is flagged", now.Add(-9 * time.Minute), "9m*"},
		{"future timestamp clamps to zero", now.Add(5 * time.Second), "0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatLastOutput(tt.last, now, hang); got != tt.want {
				t.Errorf("formatLastOutput(%v) = %q, want %q", tt.last, got, tt.want)
			}
		})
	}
}

// TestFormatLastOutput_ThresholdDisabled: a non-positive threshold disables the
// asterisk marker entirely (env override for operators who don't want it).
func TestFormatLastOutput_ThresholdDisabled(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	if got := formatLastOutput(now.Add(-1*time.Hour), now, 0); got != "1h0m" {
		t.Errorf("formatLastOutput with disabled threshold = %q, want %q", got, "1h0m")
	}
}

// TestHangThresholdSeconds: DECKPILOT_HANG_SECONDS overrides the default; an
// unparseable value falls back to the default rather than failing.
func TestHangThresholdSeconds(t *testing.T) {
	t.Setenv("DECKPILOT_HANG_SECONDS", "120")
	if got := hangThresholdSeconds(); got != 120 {
		t.Errorf("hangThresholdSeconds with env=120 => %d, want 120", got)
	}

	t.Setenv("DECKPILOT_HANG_SECONDS", "garbage")
	if got := hangThresholdSeconds(); got != defaultHangSeconds {
		t.Errorf("hangThresholdSeconds with bad env => %d, want %d", got, defaultHangSeconds)
	}
}
