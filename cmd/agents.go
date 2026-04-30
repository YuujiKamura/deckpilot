package cmd

// AgentDef defines how to launch and interact with a TUI agent.
type AgentDef struct {
	Cmd         string   // executable name
	Args        []string // default arguments
	ReadyStr    string   // substring in output that means TUI is ready for input
	TrustStr    string   // substring for trust confirmation dialog (empty = no trust step)
	SubmitKey   string   // key sequence appended to text for atomic submit via RAW_INPUT
	ThinkingStr string   // substring that indicates TUI is processing a submitted prompt
	ErrorStr    string   // substring for early failure detection (empty = disabled)
}

var agents = map[string]AgentDef{
	"codex": {
		Cmd:         "codex",
		Args:        []string{"--full-auto"},
		ReadyStr:    "\xe2\x80\xba", // › (U+203A) - the Codex prompt marker
		TrustStr:    "trust the contents",
		SubmitKey:   "\r",
		ThinkingStr: "Thinking", // 実測候補: "Thinking", "Processing" (未実測、推測)
		ErrorStr:    "",
	},
	"claude": {
		Cmd:         "claude",
		Args:        []string{"--dangerously-skip-permissions"},
		ReadyStr:    ">",
		TrustStr:    "",
		SubmitKey:   "\r",
		ThinkingStr: "Gesticulating", // 実測: "✶ Gesticulating…" を観測済み
		ErrorStr:    "",
	},
	"sonnet": {
		Cmd:         "claude",
		Args:        []string{"--dangerously-skip-permissions", "--model", "sonnet"},
		ReadyStr:    ">",
		TrustStr:    "",
		SubmitKey:   "\r",
		ThinkingStr: "Gesticulating", // claude 系、同じ TUI を共有
		ErrorStr:    "",
	},
	"haiku": {
		Cmd:         "claude",
		Args:        []string{"--dangerously-skip-permissions", "--model", "haiku"},
		ReadyStr:    ">",
		TrustStr:    "",
		SubmitKey:   "\r",
		ThinkingStr: "Gesticulating", // claude 系、同じ TUI を共有
		ErrorStr:    "",
	},
	"gemini": {
		Cmd:         "gemini",
		Args:        []string{},
		ReadyStr:    ">",
		TrustStr:    "",
		SubmitKey:   "\r",
		ThinkingStr: "Generating", // 実測候補: "Generating", "esc to cancel" (未実測、推測)
		ErrorStr:    "",
	},
}

// DefaultSubmitKey is the fallback submit key for unknown agents.
const DefaultSubmitKey = "\r"
