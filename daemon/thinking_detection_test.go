package daemon

import "testing"

// Cases are real deckpilot show buffers captured from a launched claude worker
// on 2026-06-04 (the session that proved the done-vs-stall discriminator).
func TestLooksActivelyThinking(t *testing.T) {
	cases := []struct {
		name string
		buf  string
		want bool
	}{
		{
			name: "working: spinner with trailing ellipsis",
			buf:  "❯ 1から20まで素数を\n\n✳ Manifesting… \n\n❯  \n  bypass permissions on",
			want: true,
		},
		{
			name: "working: different glyph and word, same ellipsis",
			buf:  "❯ 5回ありがとう\n\n✶ Boondoggling…\n\n❯  ",
			want: true,
		},
		{
			name: "done: past-tense summary, no ellipsis, back at prompt",
			buf:  "  8\n  9\n  10\n\n✻ Sautéed for 9s\n\n❯  \n  bypass permissions on",
			want: false,
		},
		{
			name: "done: bare ready prompt",
			buf:  "C:\\Users\\yuuji>claude\n\n❯  \n  bypass permissions on (shift+tab to cycle)",
			want: false,
		},
		{
			name: "empty buffer",
			buf:  "",
			want: false,
		},
		{
			name: "long output line ending in ellipsis is not the spinner",
			buf:  "ここに非常に長い説明文が続いていて行末が三点リーダで終わっているがこれはスピナーではなく通常の出力なので作業中とは見なさない…",
			want: false,
		},
		{
			name: "ascii three-dot spinner also counts",
			buf:  "❯ task\n\n* Thinking...\n\n❯  ",
			want: true,
		},
		// Claude Code v2.1.330 spinners, captured live 2026-11-21.
		{
			name: "working: spinner with timer and tokens",
			buf:  "● Bash(go test ./pipe)\n  ⎿  Running…\n\n✶ Considering… (12s · ↓ 618 tokens)\n\n────\n> \n────",
			want: true,
		},
		{
			name: "working: spinner with timer, tokens and thought time",
			buf:  "· Garnishing… (36s · ↓ 2.4k tokens · thought for 1s)\n  ⎿  Tip: Press Shift+Enter to send a multi-line message\n\n> ",
			want: true,
		},
		{
			name: "working: minutes in the timer",
			buf:  "✻ Pondering… (1m 30s · ↑ 12k tokens)\n> ",
			want: true,
		},
		{
			name: "working: timer only",
			buf:  "✳ Musing… (4s)\n> ",
			want: true,
		},
		{
			name: "done: parenthesis after ellipsis without a timer is ordinary output",
			buf:  "Saved… (see log.txt)\n\n> ",
			want: false,
		},
		{
			name: "done: past-tense summary with timer",
			buf:  "✻ Cooked for 2m 3s\n\n> ",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LooksActivelyThinking(c.buf); got != c.want {
				t.Errorf("LooksActivelyThinking() = %v, want %v", got, c.want)
			}
		})
	}
}
