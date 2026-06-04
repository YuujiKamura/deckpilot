package daemon

import "testing"

// Locks the thinking-aware status distinction added 2026-06-04: a frozen
// buffer is "idle" (done, at the prompt) only when no spinner is on screen;
// a frozen spinner means the worker stalled mid-task (a silent stop that is
// NOT an OS hang, so IsHungAppWindow never catches it). Buffers are the real
// shapes captured from a launched claude worker.
func TestUpdateContentStalledVsIdle(t *testing.T) {
	readyPrompt := "C:\\Users\\yuuji>claude\n\n❯  \n  bypass permissions on (shift+tab)"
	spinner := "❯ task\n\n✳ Manifesting…\n\n❯  "

	t.Run("frozen ready prompt becomes idle", func(t *testing.T) {
		w := NewWatcher("t", "", "", 0, "", nil)
		for i := 0; i < 5; i++ {
			w.updateContent(readyPrompt)
		}
		if got := w.Status(); got != "idle" {
			t.Fatalf("status = %q, want idle", got)
		}
	})

	t.Run("frozen spinner becomes stalled past threshold", func(t *testing.T) {
		w := NewWatcher("t", "", "", 0, "", nil)
		w.updateContent(spinner) // first poll: changed → active
		for i := 0; i < stalledSpinnerPolls+2; i++ {
			w.updateContent(spinner) // held frozen
		}
		if got := w.Status(); got != "stalled" {
			t.Fatalf("status = %q, want stalled", got)
		}
	})

	t.Run("frozen spinner below threshold is not stalled", func(t *testing.T) {
		w := NewWatcher("t", "", "", 0, "", nil)
		w.updateContent(spinner)
		for i := 0; i < 3; i++ { // a few frozen polls, well under threshold
			w.updateContent(spinner)
		}
		if got := w.Status(); got == "stalled" {
			t.Fatalf("status = stalled below threshold; want still working")
		}
	})

	t.Run("stall recovers to active when the buffer moves again", func(t *testing.T) {
		w := NewWatcher("t", "", "", 0, "", nil)
		w.updateContent(spinner)
		for i := 0; i < stalledSpinnerPolls+2; i++ {
			w.updateContent(spinner)
		}
		if w.Status() != "stalled" {
			t.Fatalf("setup: expected stalled before recovery")
		}
		w.updateContent(spinner + "\nmore output now") // worker resumed
		if got := w.Status(); got != "active" {
			t.Fatalf("status = %q, want active after recovery", got)
		}
	})
}
