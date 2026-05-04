package daemon

import (
	"errors"
	"testing"
)

// TestClassifyTailError_TransportErrorAdvancesRetry: bare network/transport
// errors (not from the pipe server) should still feed deadRetries — that's
// the legacy behaviour and the only signal we have for "the pipe is gone".
func TestClassifyTailError_TransportErrorAdvancesRetry(t *testing.T) {
	got := classifyTailError(errors.New("dial \\\\.\\pipe\\foo: file not found"))
	if !got.AdvanceRetry {
		t.Errorf("transport error should advance dead retry, got %+v", got)
	}
	if got.MarkDead {
		t.Errorf("transport error should NOT mark dead immediately, got %+v", got)
	}
	if got.NewStatus != "" {
		t.Errorf("transport error should not override status, got %q", got.NewStatus)
	}
}

// TestClassifyTailError_NoTabsMarksDead: NO_TABS is session-fatal.
func TestClassifyTailError_NoTabsMarksDead(t *testing.T) {
	got := classifyTailError(errors.New("server: NO_TABS"))
	if !got.MarkDead {
		t.Errorf("NO_TABS should mark dead, got %+v", got)
	}
	if got.AdvanceRetry {
		t.Errorf("NO_TABS should not advance retry counter (dead is immediate), got %+v", got)
	}
}

// TestClassifyTailError_BusyDoesNotAdvanceRetry: this is the regression
// fixture for issue #14 / 2026-05-04 incident. Backpressure must NOT
// accumulate toward dead. Reaching 3 consecutive BUSY responses should
// leave deadRetries at zero, so a temporarily-locked Ghostty renderer
// never causes deckpilot to silently mark the session dead.
func TestClassifyTailError_BusyDoesNotAdvanceRetry(t *testing.T) {
	cases := []string{
		"server: BUSY|renderer_locked",
		"server: BUSY|data_lane_full",
		"server: TIMEOUT|ui_thread_busy",
		"server: BUSY|some_future_variant",
	}
	for _, s := range cases {
		got := classifyTailError(errors.New(s))
		if got.AdvanceRetry {
			t.Errorf("%q: BUSY should not advance retry, got %+v", s, got)
		}
		if got.MarkDead {
			t.Errorf("%q: BUSY should not mark dead, got %+v", s, got)
		}
		if got.NewStatus != "busy" {
			t.Errorf("%q: BUSY should set status=busy, got %q", s, got.NewStatus)
		}
	}
}

// TestClassifyTailError_NilErrorIsNoop: no error means nothing to classify.
func TestClassifyTailError_NilErrorIsNoop(t *testing.T) {
	got := classifyTailError(nil)
	if got.AdvanceRetry || got.MarkDead || got.NewStatus != "" {
		t.Errorf("nil error should produce zero classification, got %+v", got)
	}
}
