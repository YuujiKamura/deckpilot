package daemon

import (
	"testing"
	"time"
)

// TestUpdateContent_StampsLastBufferChange: the first non-empty content a
// watcher observes records a wall-clock change time, surfaced via Profile().
// Before any poll the timestamp is zero ("no output yet").
func TestUpdateContent_StampsLastBufferChange(t *testing.T) {
	fc := &fakeClient{tailFn: func(int) (string, error) { return "hello", nil }}
	w := newWatcherForTest("stamp-session", fc)

	if got := w.Profile().LastBufferChangeAt; !got.IsZero() {
		t.Fatalf("LastBufferChangeAt before any poll = %v, want zero", got)
	}

	w.poll()
	if w.Profile().LastBufferChangeAt.IsZero() {
		t.Fatal("LastBufferChangeAt after first content poll is zero, want stamped")
	}
}

// TestUpdateContent_UnchangedContentKeepsTimestamp: polling identical content
// must NOT advance the timestamp — only a genuine change counts as new output.
func TestUpdateContent_UnchangedContentKeepsTimestamp(t *testing.T) {
	fc := &fakeClient{tailFn: func(int) (string, error) { return "steady", nil }}
	w := newWatcherForTest("steady-session", fc)

	w.poll()
	first := w.Profile().LastBufferChangeAt

	time.Sleep(5 * time.Millisecond)
	w.poll() // identical content
	second := w.Profile().LastBufferChangeAt

	if !first.Equal(second) {
		t.Errorf("timestamp advanced on unchanged content: %v -> %v", first, second)
	}
}

// TestUpdateContent_ChangedContentAdvancesTimestamp: distinct content on a
// later poll must move the timestamp forward.
func TestUpdateContent_ChangedContentAdvancesTimestamp(t *testing.T) {
	out := "first"
	fc := &fakeClient{tailFn: func(int) (string, error) { return out, nil }}
	w := newWatcherForTest("changing-session", fc)

	w.poll()
	first := w.Profile().LastBufferChangeAt

	time.Sleep(5 * time.Millisecond)
	out = "second"
	w.poll()
	second := w.Profile().LastBufferChangeAt

	if !second.After(first) {
		t.Errorf("timestamp did not advance on changed content: %v not after %v", second, first)
	}
}

// TestUpdateContent_EmptyContentDoesNotStamp: a session whose buffer reads
// empty must stay "no output yet" (zero timestamp) so list renders "-" rather
// than "0s" the moment a watcher is attached.
func TestUpdateContent_EmptyContentDoesNotStamp(t *testing.T) {
	fc := &fakeClient{tailFn: func(int) (string, error) { return "", nil }}
	w := newWatcherForTest("empty-session", fc)

	w.poll()
	if got := w.Profile().LastBufferChangeAt; !got.IsZero() {
		t.Errorf("empty buffer stamped LastBufferChangeAt = %v, want zero", got)
	}
}
