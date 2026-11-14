package daemon

import (
	"errors"
	"testing"
)

func TestErrFromResponse(t *testing.T) {
	cases := []struct {
		body     string
		notFound bool
		msg      string
	}{
		{"session not found: ghostty-36764", true, "session not found: ghostty-36764"},
		{"session not found: ghostty-36764\n", true, "session not found: ghostty-36764"},
		{"submit_failed_stuck|404ms", false, "submit_failed_stuck|404ms"},
		{"no recent session for this caller", false, "no recent session for this caller"},
		{"history: open x: access denied", false, "history: open x: access denied"},
	}
	for _, c := range cases {
		err := errFromResponse(c.body)
		if got := errors.Is(err, ErrSessionNotFound); got != c.notFound {
			t.Errorf("%q: errors.Is(ErrSessionNotFound)=%v, want %v", c.body, got, c.notFound)
		}
		if err.Error() != c.msg {
			t.Errorf("%q: message=%q, want %q (text must stay unchanged)", c.body, err.Error(), c.msg)
		}
	}
}

// The server-side line and the client-side parser must round-trip.
func TestSessionNotFoundWireRoundTrip(t *testing.T) {
	line := sessionNotFoundResp("ghostty-9")
	if line != "ERR|session not found: ghostty-9\n" {
		t.Fatalf("wire line changed: %q", line)
	}
	err := errFromResponse(line[len("ERR|"):])
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("round trip lost the sentinel: %v", err)
	}
}
