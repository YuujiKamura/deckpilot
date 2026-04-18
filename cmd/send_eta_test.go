package cmd

import (
	"strings"
	"testing"
	"time"
)

// TestFormatETA asserts ETA line format with correct time arithmetic.
func TestFormatETA(t *testing.T) {
	now := fixedNow() // 2026-04-19 Sun 08:10 JST
	eta := 30 * time.Minute
	got := formatETA(now, eta)
	// Expected: "NOW: 2026-04-19 Sun 08:10 JST  |  ETA 08:40 JST (+30m)"
	if !strings.HasPrefix(got, "NOW: 2026-04-19 Sun 08:10 JST") {
		t.Errorf("ETA line should start with NOW, got %q", got)
	}
	if !strings.Contains(got, "ETA 08:40 JST") {
		t.Errorf("ETA line should show arrival time 08:40 JST, got %q", got)
	}
	if !strings.Contains(got, "+30m") {
		t.Errorf("ETA line should show +30m, got %q", got)
	}
}

// TestFormatETA_1h asserts 60-minute ETA formats as +1h0m.
func TestFormatETA_1h(t *testing.T) {
	now := fixedNow()
	got := formatETA(now, 60*time.Minute)
	if !strings.Contains(got, "ETA 09:10 JST") {
		t.Errorf("expected ETA 09:10 JST, got %q", got)
	}
	if !strings.Contains(got, "+1h0m") {
		t.Errorf("expected +1h0m, got %q", got)
	}
}
