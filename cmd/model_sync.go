package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SyncResult describes the managed profiles written into agents.json.
type SyncResult struct {
	AgentsFile string            `json:"agents_file"`
	Profiles   map[string]string `json:"profiles"`
}

// atomicWriteJSON encodes v as readable UTF-8 JSON (SetEscapeHTML(false)) into a
// unique sibling temp file and atomically renames it into place.
func atomicWriteJSON(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmpName, 0o600)
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func buildManagedAgentEntry(carrier string, cliArgs []string, note, stamp string) (agentProfileEntry, bool) {
	base, ok := carrierTUIDefs[carrier]
	if !ok {
		return agentProfileEntry{}, false
	}
	args := make([]string, 0, len(base.BaseArgs)+len(cliArgs))
	args = append(args, base.BaseArgs...)
	args = append(args, cliArgs...)
	return agentProfileEntry{
		Cmd:         base.Cmd,
		Args:        args,
		ReadyStr:    base.ReadyStr,
		TrustStr:    base.TrustStr,
		SubmitKey:   base.SubmitKey,
		ThinkingStr: base.ThinkingStr,
		ErrorStr:    base.ErrorStr,
		LearnedAt:   stamp,
		LearnedFrom: modelRouterManagedTag + ": " + note,
	}, true
}

var syncAutoMatrix = [][2]string{
	{"light", "low"},
	{"light", "medium"},
	{"standard", "low"},
	{"standard", "medium"},
	{"standard", "high"},
	{"frontier", "low"},
	{"frontier", "medium"},
	{"frontier", "high"},
}

// syncAgentsJSON regenerates only deckpilot-model-router managed entries in
// agents.json while preserving all manually defined and learned user profiles.
func syncAgentsJSON(home string, cat *ModelCatalog, pol *ModelPolicy) (SyncResult, error) {
	path := agentProfilesPathIn(home)
	existing := make(map[string]agentProfileEntry)
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &existing); err != nil {
			return SyncResult{}, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return SyncResult{}, err
	}

	merged := make(map[string]agentProfileEntry, len(existing))
	for name, entry := range existing {
		if strings.HasPrefix(entry.LearnedFrom, modelRouterManagedTag) {
			continue
		}
		merged[name] = entry
	}

	stamp := formatUTC(deckpilotNow())
	written := make(map[string]string)

	// 1. Explicit 2D profiles per active carrier: <carrier>-<tier>-<effort>
	for _, carrier := range activeCarrierOrder(pol, "") {
		if !carrierAvailableInCatalog(cat, carrier) {
			continue
		}
		for _, tier := range validTiers {
			m := findModelEntry(cat, carrier, tier, pol)
			if m == nil {
				continue
			}
			for _, eff := range coreEfforts {
				spec, ok := m.Efforts[eff]
				if !ok {
					continue
				}
				key := fmt.Sprintf("%s-%s-%s", carrier, tier, eff)
				if entry, ok := buildManagedAgentEntry(carrier, spec.CLIArgs, fmt.Sprintf("%s %s:%s -> %s", carrier, tier, eff, m.ID), stamp); ok {
					merged[key] = entry
					written[key] = fmt.Sprintf("%s (%s)", m.ID, eff)
				}
			}
		}
	}

	// 2. Automatic 2D profiles: auto-<tier>-<effort>
	for _, pair := range syncAutoMatrix {
		tier, eff := pair[0], pair[1]
		route := selectRoute(cat, pol, tier, eff, nil, "")
		if !route.OK {
			continue
		}
		key := fmt.Sprintf("auto-%s-%s", tier, eff)
		note := fmt.Sprintf("auto %s:%s -> %s %s:%s (%s)", tier, eff, route.Carrier, route.EffectiveTier, route.EffectiveEffort, route.ModelID)
		if entry, ok := buildManagedAgentEntry(route.Carrier, route.CLIArgs, note, stamp); ok {
			merged[key] = entry
			written[key] = fmt.Sprintf("%s:%s (%s:%s)", route.Carrier, route.ModelID, route.EffectiveTier, route.EffectiveEffort)
		}
	}

	// 3. Shortcut gear profiles: auto-l1 / auto-l2 / auto-l3
	presets := defaultGearPresets()
	if pol != nil && len(pol.GearPresets) > 0 {
		presets = pol.GearPresets
	}
	gearKeys := make([]string, 0, len(presets))
	for k := range presets {
		gearKeys = append(gearKeys, k)
	}
	sort.Strings(gearKeys)
	for _, gear := range gearKeys {
		pair := presets[gear]
		route := selectRoute(cat, pol, pair.Tier, pair.Effort, nil, "")
		if !route.OK {
			continue
		}
		key := "auto-" + strings.ToLower(gear)
		note := fmt.Sprintf("preset %s (%s:%s) -> %s %s (%s)", gear, pair.Tier, pair.Effort, route.Carrier, route.ModelID, route.EffectiveEffort)
		if entry, ok := buildManagedAgentEntry(route.Carrier, route.CLIArgs, note, stamp); ok {
			merged[key] = entry
			written[key] = fmt.Sprintf("%s:%s (%s:%s)", route.Carrier, route.ModelID, route.EffectiveTier, route.EffectiveEffort)
		}
	}

	// 4. Optional override of built-in `agy`.
	if pol != nil && pol.OverrideBuiltinAgy && carrierAvailableInCatalog(cat, "agy") {
		if res := resolveCarrierPair(cat, pol, "agy", "standard", "low"); res != nil {
			note := fmt.Sprintf("agy default -> %s (%s)", res.ModelID, res.Effort)
			if entry, ok := buildManagedAgentEntry("agy", res.CLIArgs, note, stamp); ok {
				merged["agy"] = entry
				written["agy"] = fmt.Sprintf("%s (%s)", res.ModelID, res.Effort)
			}
		}
	}

	if err := atomicWriteJSON(path, merged); err != nil {
		return SyncResult{}, err
	}
	return SyncResult{
		AgentsFile: path,
		Profiles:   written,
	}, nil
}

type managedProfileSpec struct {
	kind    string // "preset", "auto", "carrier"
	gear    string
	carrier string
	tier    string
	effort  string
}

func parseManagedProfileName(name string) (managedProfileSpec, bool) {
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return managedProfileSpec{}, false
	}
	lower := strings.ToLower(name)
	switch lower {
	case "auto-l1", "auto-l2", "auto-l3":
		return managedProfileSpec{kind: "preset", gear: strings.ToUpper(strings.TrimPrefix(lower, "auto-"))}, true
	}

	parts := strings.Split(lower, "-")
	if len(parts) != 3 {
		return managedProfileSpec{}, false
	}
	prefix, tier, effort := parts[0], parts[1], parts[2]
	if !isValidTier(tier) || !isValidEffort(effort) {
		return managedProfileSpec{}, false
	}
	if prefix == "auto" {
		return managedProfileSpec{kind: "auto", tier: tier, effort: effort}, true
	}
	if isValidCarrier(prefix) {
		return managedProfileSpec{kind: "carrier", carrier: prefix, tier: tier, effort: effort}, true
	}
	return managedProfileSpec{}, false
}

// resolveDynamicManagedAgent attempts dynamic catalog+policy resolution for
// auto-l1/l2/l3, auto-<tier>-<effort>, and <carrier>-<tier>-<effort>.
// Returns:
//   - (def, true, nil) on successful dynamic resolution
//   - (AgentDef{}, true, err) on explicit failure (all carriers exhausted / corrupt catalog)
//   - (AgentDef{}, false, nil) when catalog does not exist yet (caller may fall back to agents.json)
func resolveDynamicManagedAgent(spec managedProfileSpec, name string) (AgentDef, bool, error) {
	home := deckpilotHomeDir()
	cat, err := readCatalogFile(modelCurrentCatalogPathIn(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AgentDef{}, false, nil
		}
		return AgentDef{}, true, fmt.Errorf("resolve %q: %w", name, err)
	}

	pol, _, autoRecovered, err := loadPolicyIn(home, deckpilotNow())
	if err != nil {
		return AgentDef{}, true, fmt.Errorf("resolve %q: load policy: %w", name, err)
	}
	if autoRecovered {
		_, _ = syncAgentsJSON(home, cat, pol)
	}

	switch spec.kind {
	case "preset":
		preset, ok := pol.GearPresets[spec.gear]
		if !ok {
			preset, ok = defaultGearPresets()[spec.gear]
		}
		if !ok {
			return AgentDef{}, true, fmt.Errorf("resolve %q: unknown gear preset %q", name, spec.gear)
		}
		route := selectRoute(cat, pol, preset.Tier, preset.Effort, nil, "")
		if !route.OK {
			return AgentDef{}, true, fmt.Errorf("resolve %q (%s:%s): %s", name, preset.Tier, preset.Effort, route.Reason)
		}
		def, ok := agentDefFromCarrier(route.Carrier, route.CLIArgs, false, true, true)
		if !ok {
			return AgentDef{}, true, fmt.Errorf("resolve %q: unknown carrier %q", name, route.Carrier)
		}
		return def, true, nil

	case "auto":
		route := selectRoute(cat, pol, spec.tier, spec.effort, nil, "")
		if !route.OK {
			return AgentDef{}, true, fmt.Errorf("resolve %q (%s:%s): %s", name, spec.tier, spec.effort, route.Reason)
		}
		def, ok := agentDefFromCarrier(route.Carrier, route.CLIArgs, false, true, true)
		if !ok {
			return AgentDef{}, true, fmt.Errorf("resolve %q: unknown carrier %q", name, route.Carrier)
		}
		return def, true, nil

	case "carrier":
		if !carrierAvailableInCatalog(cat, spec.carrier) {
			return AgentDef{}, true, fmt.Errorf("resolve %q: carrier %q is not available in catalog", name, spec.carrier)
		}
		res := resolveCarrierPair(cat, pol, spec.carrier, spec.tier, spec.effort)
		if res == nil {
			return AgentDef{}, true, fmt.Errorf("resolve %q: no model found for %s %s:%s", name, spec.carrier, spec.tier, spec.effort)
		}
		def, ok := agentDefFromCarrier(spec.carrier, res.CLIArgs, false, true, true)
		if !ok {
			return AgentDef{}, true, fmt.Errorf("resolve %q: unknown carrier %q", name, spec.carrier)
		}
		return def, true, nil
	}
	return AgentDef{}, false, nil
}
