package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type agentProfileEntry struct {
	Cmd         string   `json:"cmd"`
	Args        []string `json:"args"`
	ReadyStr    string   `json:"ready_str"`
	TrustStr    string   `json:"trust_str"`
	SubmitKey   string   `json:"submit_key"`
	ThinkingStr string   `json:"thinking_str"`
	ErrorStr    string   `json:"error_str"`
	LearnedAt   string   `json:"learned_at"`
	LearnedFrom string   `json:"learned_from"`
}

func agentProfilesPath() string {
	return filepath.Join(deckpilotHomeDir(), "agents.json")
}

func loadAgentProfiles() (map[string]AgentDef, error) {
	raw, err := os.ReadFile(agentProfilesPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]AgentDef{}, nil
		}
		return nil, err
	}
	var entries map[string]agentProfileEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	out := make(map[string]AgentDef, len(entries))
	for name, e := range entries {
		submitKey := e.SubmitKey
		if submitKey == "" {
			submitKey = DefaultSubmitKey
		}
		out[name] = AgentDef{
			Cmd:         e.Cmd,
			Args:        e.Args,
			ReadyStr:    e.ReadyStr,
			TrustStr:    e.TrustStr,
			SubmitKey:   submitKey,
			ThinkingStr: e.ThinkingStr,
			ErrorStr:    e.ErrorStr,
			Learned:     true,
		}
	}
	return out, nil
}

func saveAgentProfile(name string, def AgentDef, settledScreen string) error {
	path := agentProfilesPath()
	entries := make(map[string]agentProfileEntry)
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if _, exists := entries[name]; exists {
		return nil
	}

	args := def.Args
	if args == nil {
		args = []string{}
	}
	submitKey := def.SubmitKey
	if submitKey == "" {
		submitKey = DefaultSubmitKey
	}
	entries[name] = agentProfileEntry{
		Cmd:         def.Cmd,
		Args:        args,
		ReadyStr:    def.ReadyStr,
		TrustStr:    def.TrustStr,
		SubmitKey:   submitKey,
		ThinkingStr: def.ThinkingStr,
		ErrorStr:    def.ErrorStr,
		LearnedAt:   deckpilotNow().Format(time.RFC3339),
		LearnedFrom: lastNonBlankLines(settledScreen, 5),
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Keep ">" and "<" readable: this file is meant to be edited by hand.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(entries); err != nil {
		return err
	}
	data := buf.Bytes()
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func lastNonBlankLines(screen string, n int) string {
	var nonBlank []string
	for _, line := range strings.Split(screen, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonBlank = append(nonBlank, line)
	}
	if n > 0 && len(nonBlank) > n {
		nonBlank = nonBlank[len(nonBlank)-n:]
	}
	return strings.Join(nonBlank, "\n")
}

func readyMarkerFrom(screen string) string {
	if looksLikeCommandPrompt(screen) {
		return ""
	}
	lines := strings.Split(screen, "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed != "" {
			last = trimmed
			break
		}
	}
	if last == "" {
		return ""
	}
	if idx := strings.Index(last, "  "); idx >= 0 {
		last = last[:idx]
	}
	last = strings.TrimSpace(last)
	if utf8.RuneCountInString(last) < 2 {
		return ""
	}
	return last
}

func resolveAgent(name string) (AgentDef, error) {
	profiles, err := loadAgentProfiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load %s: %v\n", agentProfilesPath(), err)
	} else if def, ok := profiles[name]; ok {
		return def, nil
	}
	if def, ok := agents[name]; ok {
		return def, nil
	}
	if def, ok := genericAgent(name); ok {
		return def, nil
	}
	return AgentDef{}, fmt.Errorf("empty agent command")
}
