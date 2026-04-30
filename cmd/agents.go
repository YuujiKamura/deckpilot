package cmd

// AgentDef defines how to launch and interact with a TUI agent.
type AgentDef struct {
	Cmd       string   // executable name
	Args      []string // default arguments
	ReadyStr  string   // substring in output that means TUI is ready for input
	TrustStr  string   // substring for trust confirmation dialog (empty = no trust step)
	SubmitKey string   // key sequence appended to text for atomic submit via RAW_INPUT
}

var agents = map[string]AgentDef{
	"codex": {
		Cmd:       "codex",
		Args:      []string{"--full-auto"},
		ReadyStr:  "\xe2\x80\xba", // › (U+203A) - the Codex prompt marker
		TrustStr:  "trust the contents",
		SubmitKey: "\r",
	},
	"claude": {
		Cmd:       "claude",
		Args:      []string{"--dangerously-skip-permissions"},
		ReadyStr:  ">",
		TrustStr:  "",
		SubmitKey: "\r",
	},
	"sonnet": {
		Cmd:       "claude",
		Args:      []string{"--dangerously-skip-permissions", "--model", "sonnet"},
		ReadyStr:  ">",
		TrustStr:  "",
		SubmitKey: "\r",
	},
	"haiku": {
		Cmd:       "claude",
		Args:      []string{"--dangerously-skip-permissions", "--model", "haiku"},
		ReadyStr:  ">",
		TrustStr:  "",
		SubmitKey: "\r",
	},
	"gemini": {
		Cmd:       "gemini",
		Args:      []string{},
		ReadyStr:  ">",
		TrustStr:  "",
		SubmitKey: "\r",
	},
}

// DefaultSubmitKey is the fallback submit key for unknown agents.
const DefaultSubmitKey = "\r"
