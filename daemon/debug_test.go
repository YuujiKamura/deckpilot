package daemon

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// captureLog redirects the package's log output to a buffer for the
// duration of fn and restores the previous output afterwards. The
// daemon-wide logger is the same one debugf writes to, so this is the
// only honest way to assert "did debugf actually emit?".
func captureLog(t *testing.T, fn func()) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0) // strip timestamp prefix so substring asserts are stable
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	fn()
	return &buf
}

// withDebug temporarily forces the package-level debugEnabled flag to v.
// Tests must not rely on the real DECKPILOT_DEBUG env var because the
// flag is parsed once at startup; a test that mutates the env after
// init() would silently no-op.
func withDebug(t *testing.T, v bool) {
	t.Helper()
	prev := debugEnabled
	debugEnabled = v
	t.Cleanup(func() { debugEnabled = prev })
}

func TestDebugf_NoOpWhenDisabled(t *testing.T) {
	withDebug(t, false)
	buf := captureLog(t, func() {
		debugf("trace message %d", 42)
	})
	if buf.Len() != 0 {
		t.Fatalf("expected no output when debug disabled, got %q", buf.String())
	}
}

func TestDebugf_WritesWhenEnabled(t *testing.T) {
	withDebug(t, true)
	buf := captureLog(t, func() {
		debugf("trace message %d", 42)
	})
	if buf.Len() == 0 {
		t.Fatal("expected debug output when debug enabled, got empty buffer")
	}
	if !strings.Contains(buf.String(), "trace message 42") {
		t.Fatalf("expected formatted message in output, got %q", buf.String())
	}
}

func TestParseDebugFlag_Truthy(t *testing.T) {
	cases := []string{"1", "true", "TRUE", "True", "yes", "YES", "Yes"}
	for _, c := range cases {
		if !parseDebugFlag(c) {
			t.Errorf("parseDebugFlag(%q) = false, want true", c)
		}
	}
}

func TestParseDebugFlag_Falsy(t *testing.T) {
	cases := []string{"", "0", "false", "no", "off", "FALSE", "2", " 1", "1 ", "anything"}
	for _, c := range cases {
		if parseDebugFlag(c) {
			t.Errorf("parseDebugFlag(%q) = true, want false", c)
		}
	}
}

// TestHandleConn_PingNoLogWhenDebugOff is the integration-style assertion
// that the highest-frequency offender (PING from every CLI tab every
// 1-2s) really is silent when the debug flag is off. We cannot easily
// stand up a full named-pipe listener in a unit test on Windows, so we
// invoke the same trace site (handleConn's raw-line debug) directly via
// debugf and assert the log buffer stays empty.
//
// Limitation: this exercises the trace helper, not handleConn itself.
// Future contributors should not assume this proves end-to-end IPC
// silence — only that the gating helper behaves correctly in the
// configuration handleConn relies on.
func TestHandleConn_PingNoLogWhenDebugOff(t *testing.T) {
	withDebug(t, false)
	buf := captureLog(t, func() {
		debugf("handleConn: raw line=%q parts=%v", "PING", []string{"PING"})
	})
	if buf.Len() != 0 {
		t.Fatalf("PING trace must not be written when debug disabled, got %q", buf.String())
	}
}
