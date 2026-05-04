package pipe

import "testing"

// TestIsBusy_ReverseOrderEnvelope: server-wrapped ERR is the standard form
// (`server: ERR|...`), but the ghostty pipe protocol does not formally rule
// out the reverse order (`ERR|server: ...`) appearing in some legacy or
// future client wrapping. We must classify it correctly either way; both
// are pre-image variants of the same underlying BUSY signal.
func TestIsBusy_ReverseOrderEnvelope(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Standard form (already covered): server: ERR|BUSY|...
		{"server: ERR|BUSY|renderer_locked", true},
		// Reverse form: ERR|server: BUSY|... — currently unhandled
		{"ERR|server: BUSY|renderer_locked", true},
		{"ERR|server: TIMEOUT|ui_thread_busy", true},
	}
	for _, c := range cases {
		got := IsBusy(c.in)
		if got != c.want {
			t.Errorf("IsBusy(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestIsBusy_DoubleWrappedEnvelope: a passthrough proxy could wrap twice
// (`server: server: BUSY|...`). Defensive — we strip prefixes loop-style.
func TestIsBusy_DoubleWrappedEnvelope(t *testing.T) {
	if !IsBusy("server: server: BUSY|renderer_locked") {
		t.Errorf("double-wrapped server: prefix should still classify as busy")
	}
}

// TestIsBusy_NoFalsePositiveOnBusyAsWord: ensure substring matches do not
// classify a sentence containing "BUSY" inside an unrelated message as
// backpressure. Only the leading-token form `BUSY|...` after envelope
// stripping should trigger.
func TestIsBusy_NoFalsePositiveOnBusyAsWord(t *testing.T) {
	cases := []string{
		"server: error message BUSY happens sometimes",       // not at start
		"ERR|something went wrong (BUSY was not the cause)",  // BUSY mid-message
		"please be NOT BUSY",
	}
	for _, s := range cases {
		if IsBusy(s) {
			t.Errorf("IsBusy(%q) = true, must not false-positive on substring", s)
		}
	}
}

// TestIsBusy_BusyVariantTrailingGarbage: a future ghostty release could
// add fields after the variant (`BUSY|renderer_locked|some_field`). The
// classifier uses HasPrefix, so trailing garbage is fine — but make sure
// CRLF / NUL / control chars in the trailing tail do not break it.
func TestIsBusy_BusyVariantTrailingGarbage(t *testing.T) {
	cases := []string{
		"BUSY|renderer_locked|extra_field",
		"BUSY|renderer_locked\r\n",
		"BUSY|renderer_locked\x00trailing",
		"server: BUSY|data_lane_full|queue=1024",
	}
	for _, s := range cases {
		if !IsBusy(s) {
			t.Errorf("IsBusy(%q) = false, want true (HasPrefix should still match)", s)
		}
	}
}

// TestClassifyBusy_EmptyVariantBody: `BUSY|` with no variant body — classify
// as BusyOther (some kind of busy, unrecognised) rather than BusyNone.
func TestClassifyBusy_EmptyVariantBody(t *testing.T) {
	cases := []struct {
		in   string
		want BusyKind
	}{
		{"BUSY|", BusyOther},
		{"server: BUSY|", BusyOther},
		{"ERR|TIMEOUT|", BusyOther},
	}
	for _, c := range cases {
		got := ClassifyBusy(c.in)
		if got != c.want {
			t.Errorf("ClassifyBusy(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
