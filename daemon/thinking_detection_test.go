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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LooksActivelyThinking(c.buf); got != c.want {
				t.Errorf("LooksActivelyThinking() = %v, want %v", got, c.want)
			}
		})
	}
}
