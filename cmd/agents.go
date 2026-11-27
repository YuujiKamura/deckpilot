package cmd

import "strings"

// AgentDef defines how to launch and interact with a TUI agent.
type AgentDef struct {
	Cmd         string   // executable name
	Args        []string // default arguments
	ReadyStr    string   // substring in output that means TUI is ready for input
	TrustStr    string   // substring for trust confirmation dialog (empty = no trust step)
	SubmitKey   string   // key sequence appended to text for atomic submit via RAW_INPUT
	ThinkingStr string   // substring that indicates TUI is processing a submitted prompt
	ErrorStr    string   // substring for early failure detection (empty = disabled)
	Generic     bool     // built from a raw command line; no per-CLI knowledge
	Learned     bool     // loaded from the user's agents.json
	Managed     bool     // managed by deckpilot model router
}

// carrierTUIDef holds the shared TUI interaction metadata and base CLI flags
// for each model-router carrier (agy, codex, claude). Both built-in agents
// and router-managed profiles derive from this single source of truth.
type carrierTUIDef struct {
	Cmd         string
	BaseArgs    []string
	ReadyStr    string
	TrustStr    string
	SubmitKey   string
	ThinkingStr string
	ErrorStr    string
}

var carrierTUIDefs = map[string]carrierTUIDef{
	"codex": {
		Cmd:         "codex",
		BaseArgs:    []string{"--dangerously-bypass-approvals-and-sandbox"},
		ReadyStr:    "\xe2\x80\xba", // › (U+203A) - the Codex prompt marker
		TrustStr:    "trust the contents",
		SubmitKey:   DefaultSubmitKey,
		ThinkingStr: "Thinking", // 実測候補: "Thinking", "Processing" (未実測、推測)
		ErrorStr:    "",
	},
	"claude": {
		Cmd:      "claude",
		BaseArgs: claudeDefaultArgs(),
		// The current Claude Code TUI shows this footer alongside its prompt box.
		ReadyStr: "bypass permissions",
		// claude は初回フォルダ起動で信頼ダイアログを出す
		// (--dangerously-skip-permissions では消えない別ゲート)。非空 =
		// 「信頼ダイアログを出し得る」オプトイン印。実際の照合は文言に依存
		// しない looksLikeTrustDialog (fuzzy) が行う ── 値そのものは
		// サンプル文言の記録用 (2026-06-04: 文言変更で exact 一致が死んだ反省)。
		TrustStr:    "trust this folder",
		SubmitKey:   DefaultSubmitKey,
		ThinkingStr: "Gesticulating", // 実測: "✶ Gesticulating…" を観測済み
		ErrorStr:    "",
	},
	"agy": {
		Cmd:      "agy",
		BaseArgs: []string{"--mode", "accept-edits"},
		ReadyStr: ">",
		// agy asks "Do you trust the contents of this project?" on a new folder.
		TrustStr:    "trust the contents",
		SubmitKey:   DefaultSubmitKey,
		ThinkingStr: "Generating",
		ErrorStr:    "",
	},
}

func agentDefFromCarrier(carrier string, extraArgs []string, prependExtra bool, learned, managed bool) (AgentDef, bool) {
	base, ok := carrierTUIDefs[carrier]
	if !ok {
		return AgentDef{}, false
	}
	args := make([]string, 0, len(base.BaseArgs)+len(extraArgs))
	if prependExtra {
		args = append(args, extraArgs...)
		args = append(args, base.BaseArgs...)
	} else {
		args = append(args, base.BaseArgs...)
		args = append(args, extraArgs...)
	}
	return AgentDef{
		Cmd:         base.Cmd,
		Args:        args,
		ReadyStr:    base.ReadyStr,
		TrustStr:    base.TrustStr,
		SubmitKey:   base.SubmitKey,
		ThinkingStr: base.ThinkingStr,
		ErrorStr:    base.ErrorStr,
		Learned:     learned,
		Managed:     managed,
	}, true
}

func mustCarrierAgent(carrier string, extraArgs []string, prependExtra bool) AgentDef {
	def, ok := agentDefFromCarrier(carrier, extraArgs, prependExtra, false, false)
	if !ok {
		panic("unknown carrier: " + carrier)
	}
	return def
}

var agents = map[string]AgentDef{
	"codex":  mustCarrierAgent("codex", nil, false),
	"claude": mustCarrierAgent("claude", nil, false),
	"sonnet": mustCarrierAgent("claude", []string{"--model", "sonnet"}, false),
	"haiku":  mustCarrierAgent("claude", []string{"--model", "haiku"}, false),
	"gemini": {
		Cmd:         "gemini",
		Args:        []string{},
		ReadyStr:    ">",
		TrustStr:    "",
		SubmitKey:   DefaultSubmitKey,
		ThinkingStr: "Generating", // 実測候補: "Generating", "esc to cancel" (未実測、推測)
		ErrorStr:    "",
	},
	// argon low: the larger models exhaust the agy quota quickly (2026-11-14).
	"agy": mustCarrierAgent("agy", []string{"--model", "gemini-4-argon-low"}, true),
}

// genericAgent builds a definition for a CLI deckpilot has never heard of.
// The name is taken as a command line ("aider --model x"). It carries no
// ready marker: readiness is read from the screen settling, and menus are
// reported to the caller instead of being answered.
func genericAgent(commandLine string) (AgentDef, bool) {
	fields := strings.Fields(commandLine)
	if len(fields) == 0 {
		return AgentDef{}, false
	}
	return AgentDef{Cmd: fields[0], Args: fields[1:], SubmitKey: DefaultSubmitKey, Generic: true}, true
}

// DefaultSubmitKey is the fallback submit key for unknown agents.
const DefaultSubmitKey = "\r"

func claudeDefaultArgs() []string {
	return []string{
		"--dangerously-skip-permissions",
	}
}
