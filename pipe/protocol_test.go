package pipe

import "testing"

func TestIsError_ParsesPrefix(t *testing.T) {
	cases := []struct {
		in        string
		wantMsg   string
		wantIsErr bool
	}{
		{"ERR|something failed", "something failed", true},
		{"ERR|BUSY|renderer_locked", "BUSY|renderer_locked", true},
		{"OK|payload", "", false},
		{"PONG|", "", false},
		{"", "", false},
		{"  ERR|trimmed  ", "trimmed", true}, // TrimSpace applied first
	}
	for _, c := range cases {
		gotMsg, gotIsErr := IsError(c.in)
		if gotIsErr != c.wantIsErr || gotMsg != c.wantMsg {
			t.Errorf("IsError(%q) = (%q, %v), want (%q, %v)",
				c.in, gotMsg, gotIsErr, c.wantMsg, c.wantIsErr)
		}
	}
}

func TestIsBusy_RecognisesBackpressureVariants(t *testing.T) {
	// IsBusy must accept the response in every form the deckpilot stack
	// observes it: raw protocol, stripped, or wrapped by the pipe Client
	// in fmt.Errorf("server: %s", errMsg). NO_TABS is session-fatal and
	// must NOT be classified as busy.
	cases := []struct {
		in   string
		want bool
	}{
		// raw protocol form
		{"ERR|BUSY|renderer_locked", true},
		{"ERR|BUSY|data_lane_full", true},
		{"ERR|TIMEOUT|ui_thread_busy", true},
		// stripped (post-IsError)
		{"BUSY|renderer_locked", true},
		{"BUSY|data_lane_full", true},
		{"TIMEOUT|ui_thread_busy", true},
		// wrapped by pipe.Client.SendRecv error path
		{"server: BUSY|renderer_locked", true},
		{"server: TIMEOUT|ui_thread_busy", true},
		// nested: server-wrapped ERR| (defensive — prefix order matters)
		{"server: ERR|BUSY|renderer_locked", true},
		{"server: ERR|TIMEOUT|ui_thread_busy", true},
		// session end — must NOT be busy
		{"ERR|NO_TABS", false},
		{"NO_TABS", false},
		{"server: NO_TABS", false},
		// unrelated errors — must NOT be busy
		{"ERR|unknown command: FOO", false},
		{"ERR|usage: SEND|...", false},
		{"OK|payload", false},
		{"", false},
	}
	for _, c := range cases {
		got := IsBusy(c.in)
		if got != c.want {
			t.Errorf("IsBusy(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBusyKind_Classifies(t *testing.T) {
	cases := []struct {
		in   string
		want BusyKind
	}{
		{"ERR|BUSY|renderer_locked", BusyRendererLocked},
		{"BUSY|renderer_locked", BusyRendererLocked},
		{"server: BUSY|renderer_locked", BusyRendererLocked},
		{"ERR|BUSY|data_lane_full", BusyDataLaneFull},
		{"server: BUSY|data_lane_full", BusyDataLaneFull},
		{"ERR|TIMEOUT|ui_thread_busy", BusyUIThreadBusy},
		{"server: TIMEOUT|ui_thread_busy", BusyUIThreadBusy},
		{"BUSY|something_new_we_havent_seen", BusyOther},
		{"server: BUSY|something_new_we_havent_seen", BusyOther},
		{"ERR|NO_TABS", BusyNone},
		{"OK|payload", BusyNone},
		{"", BusyNone},
	}
	for _, c := range cases {
		got := ClassifyBusy(c.in)
		if got != c.want {
			t.Errorf("ClassifyBusy(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
