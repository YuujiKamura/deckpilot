package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Models is the CLI entry point for `deckpilot models` (aliases: `model`, `router`).
// It operates purely on local state files without touching or starting the daemon.
func Models(args []string) {
	code, err := ModelsWithError(args, os.Stdout, os.Stderr, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
	}
	if code != 0 {
		os.Exit(code)
	}
}

// ModelsWithError runs the models subcommand with injectable I/O and returns an
// exit code (0=ok, 1=CLI/IO error, 2=exhausted or STOP_AND_ASK_USER).
func ModelsWithError(args []string, stdout, stderr io.Writer, stdin io.Reader) (int, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}

	home, rest, err := extractHomeFlag(args)
	if err != nil {
		return 1, err
	}
	if len(rest) == 0 || rest[0] == "help" || rest[0] == "-h" || rest[0] == "--help" {
		printModelsUsage(stdout)
		return 0, nil
	}

	action := rest[0]
	subArgs := rest[1:]

	switch action {
	case "snapshot":
		return runModelsSnapshot(home, subArgs, stdout)
	case "history":
		return runModelsHistory(home, subArgs, stdout)
	case "rollback":
		return runModelsRollback(home, subArgs, stdout)
	case "status":
		return runModelsStatus(home, subArgs, stdout)
	case "order", "set-order":
		return runModelsOrder(home, subArgs, stdout)
	case "budget", "set-budget":
		return runModelsBudget(home, subArgs, stdout)
	case "pin":
		return runModelsPin(home, subArgs, stdout)
	case "route":
		return runModelsRoute(home, subArgs, stdout)
	case "escalate":
		return runModelsEscalate(home, subArgs, stdout)
	case "check-output":
		return runModelsCheckOutput(home, subArgs, stdout, stdin)
	case "sync":
		return runModelsSync(home, subArgs, stdout)
	default:
		return 1, fmt.Errorf("models: unknown subcommand %q (run 'deckpilot models help')", action)
	}
}

func printModelsUsage(w io.Writer) {
	fmt.Fprint(w, `Usage: deckpilot models [--home DIR] <subcommand> [args]

Aliases: model, router

Subcommands:
  snapshot [--force]
      Collect carrier models (agy/codex/claude), save snapshot on diff, and sync agents.json
  history
      List saved catalog snapshots (JSON)
  rollback <snapshot_name>
      Restore current.json from snapshots/<snapshot_name> and sync agents.json
  status [--json]
      Show 2D (tier x effort) model matrix, budgets, and resolved preset routes
  order <carriers...>                              (alias: set-order)
      Set active carrier priority order and sync agents.json
  budget <carrier> <ok|low|exhausted> [flags]      (alias: set-budget)
      Update carrier budget status and sync agents.json
      [--max-tier-low T] [--max-effort-low E] [--reset-at RFC3339] [--clear-reset-at] [--note TEXT]
  pin <carrier> <tier> <model_id|auto> [--force]
      Pin a model family for a carrier tier, or 'auto' to clear
  route [--tier T] [--effort E] [--gear L1|L2|L3] [--prefer C] [--exclude C]
      Resolve best carrier/model for (tier, effort); exit 2 if all exhausted
  escalate --carrier C --kind <capability|loop|blocked|quota> [flags]
      Decide next 2D move on stall; exit 2 on STOP_AND_ASK_USER
      [--tier T] [--effort E] [--gear L1|L2|L3] [--tried C] [--hops N]
  check-output <carrier> [--file PATH] [--tier T] [--effort E] [--cooldown 15m] [--reset-at RFC3339] [--dry-run]
      Inspect output for rate-limit/quota exhaustion and trip circuit breaker
  sync
      Atomically regenerate managed entries in agents.json
`)
}

func extractHomeFlag(args []string) (string, []string, error) {
	home := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if arg == "--home" {
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return "", nil, fmt.Errorf("models: --home requires a directory path")
			}
			home = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(arg, "--home=") {
			v := strings.TrimPrefix(arg, "--home=")
			if strings.TrimSpace(v) == "" {
				return "", nil, fmt.Errorf("models: --home requires a directory path")
			}
			home = v
			continue
		}
		rest = append(rest, arg)
	}
	if home == "" {
		home = deckpilotHomeDir()
	}
	return home, rest, nil
}

func writeJSONOut(w io.Writer, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func splitFlagToken(arg string) (name, val string, hasVal bool) {
	if idx := strings.IndexByte(arg, '='); idx >= 0 {
		return arg[:idx], arg[idx+1:], true
	}
	return arg, "", false
}

func consumeFlagValue(args []string, i *int, flagName string, inlineVal string, hasInline bool) (string, error) {
	if hasInline {
		return inlineVal, nil
	}
	if *i+1 >= len(args) {
		return "", fmt.Errorf("models: %s requires a value", flagName)
	}
	*i++
	return args[*i], nil
}

func resolveTierEffort(pol *ModelPolicy, gear, tier, effort string) (string, string, error) {
	if gear != "" {
		gearKey := strings.ToUpper(strings.TrimSpace(gear))
		presets := defaultGearPresets()
		if pol != nil && len(pol.GearPresets) > 0 {
			presets = pol.GearPresets
		}
		p, ok := presets[gearKey]
		if !ok {
			return "", "", fmt.Errorf("models: unknown gear %q (valid: L1, L2, L3)", gear)
		}
		if tier == "" {
			tier = p.Tier
		}
		if effort == "" {
			effort = p.Effort
		}
	}
	if tier == "" {
		tier = "standard"
	}
	if effort == "" {
		effort = "medium"
	}
	if !isValidTier(tier) {
		return "", "", fmt.Errorf("models: invalid tier %q (valid: light, standard, frontier)", tier)
	}
	if !isValidEffort(effort) {
		return "", "", fmt.Errorf("models: invalid effort %q (valid: low, medium, high, xhigh, max, ultra)", effort)
	}
	return tier, effort, nil
}

func runModelsSnapshot(home string, args []string, stdout io.Writer) (int, error) {
	force := false
	for _, a := range args {
		switch a {
		case "--force":
			force = true
		default:
			return 1, fmt.Errorf("models snapshot: unknown argument %q", a)
		}
	}

	now := deckpilotNow()
	cat := buildCatalog()
	snapRes, err := saveCatalogSnapshot(home, cat, force, now)
	if err != nil {
		return 1, err
	}
	pol, _, _, err := loadPolicyIn(home, now)
	if err != nil {
		return 1, err
	}
	if err := atomicWriteJSON(modelPolicyPathIn(home), pol); err != nil {
		return 1, err
	}
	syncRes, err := syncAgentsJSON(home, cat, pol)
	if err != nil {
		return 1, err
	}
	payload := struct {
		Snapshot SnapshotResult `json:"snapshot"`
		Sync     SyncResult     `json:"sync"`
	}{
		Snapshot: snapRes,
		Sync:     syncRes,
	}
	if err := writeJSONOut(stdout, payload); err != nil {
		return 1, err
	}
	return 0, nil
}

func runModelsHistory(home string, args []string, stdout io.Writer) (int, error) {
	if len(args) > 0 {
		return 1, fmt.Errorf("models history: unexpected argument %q", args[0])
	}
	items, err := listCatalogHistory(home)
	if err != nil {
		return 1, err
	}
	payload := struct {
		Snapshots []SnapshotHistoryItem `json:"snapshots"`
	}{
		Snapshots: items,
	}
	if err := writeJSONOut(stdout, payload); err != nil {
		return 1, err
	}
	return 0, nil
}

func runModelsRollback(home string, args []string, stdout io.Writer) (int, error) {
	if len(args) != 1 {
		return 1, fmt.Errorf("usage: deckpilot models rollback <snapshot_name>")
	}
	name := args[0]
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return 1, fmt.Errorf("models rollback: invalid snapshot name %q (must be a filename inside snapshots/)", name)
	}

	sdir := filepath.Clean(modelSnapshotsDirIn(home))
	target := filepath.Join(sdir, name)
	if filepath.Clean(filepath.Dir(target)) != sdir {
		return 1, fmt.Errorf("models rollback: snapshot %q escapes snapshots directory", name)
	}

	cat, err := readCatalogFile(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 1, fmt.Errorf("models rollback: snapshot not found: %s", target)
		}
		return 1, fmt.Errorf("models rollback: %w", err)
	}
	if err := atomicWriteJSON(modelCurrentCatalogPathIn(home), cat); err != nil {
		return 1, err
	}
	pol, _, _, err := loadPolicyIn(home, deckpilotNow())
	if err != nil {
		return 1, err
	}
	syncRes, err := syncAgentsJSON(home, cat, pol)
	if err != nil {
		return 1, err
	}
	payload := struct {
		RolledBackTo string     `json:"rolled_back_to"`
		Sync         SyncResult `json:"sync"`
	}{
		RolledBackTo: name,
		Sync:         syncRes,
	}
	if err := writeJSONOut(stdout, payload); err != nil {
		return 1, err
	}
	return 0, nil
}

type statusPresetRoute struct {
	Preset    string `json:"preset"`
	Action    string `json:"action"`
	Carrier   string `json:"carrier,omitempty"`
	Effective string `json:"effective,omitempty"`
	Profile   string `json:"profile,omitempty"`
	Model     string `json:"model,omitempty"`
}

type statusTierCell struct {
	ModelID string   `json:"model_id"`
	Efforts []string `json:"efforts"`
}

type statusCarrierView struct {
	Available  bool                      `json:"available"`
	Budget     CarrierBudget             `json:"budget"`
	TierMatrix map[string]statusTierCell `json:"tier_matrix"`
}

type statusPayload struct {
	SchemaVersion      int                          `json:"schema_version"`
	CatalogCapturedAt  string                       `json:"catalog_captured_at"`
	CatalogFingerprint string                       `json:"catalog_fingerprint"`
	CarrierOrder       []string                     `json:"carrier_order"`
	PresetRoutes       map[string]statusPresetRoute `json:"preset_routes"`
	Carriers           map[string]statusCarrierView `json:"carriers"`
}

func runModelsStatus(home string, args []string, stdout io.Writer) (int, error) {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		default:
			return 1, fmt.Errorf("models status: unknown argument %q", a)
		}
	}

	cat, err := ensureCatalogIn(home)
	if err != nil {
		return 1, err
	}
	pol, _, autoRecovered, err := loadPolicyIn(home, deckpilotNow())
	if err != nil {
		return 1, err
	}
	if autoRecovered {
		if _, err := syncAgentsJSON(home, cat, pol); err != nil {
			return 1, err
		}
	}

	presets := defaultGearPresets()
	if len(pol.GearPresets) > 0 {
		presets = pol.GearPresets
	}
	presetRoutes := make(map[string]statusPresetRoute, len(presets))
	for gear, pair := range presets {
		r := selectRoute(cat, pol, pair.Tier, pair.Effort, nil, "")
		eff := ""
		if r.EffectiveTier != "" && r.EffectiveEffort != "" {
			eff = r.EffectiveTier + ":" + r.EffectiveEffort
		}
		presetRoutes[gear] = statusPresetRoute{
			Preset:    pair.Tier + ":" + pair.Effort,
			Action:    r.Action,
			Carrier:   r.Carrier,
			Effective: eff,
			Profile:   r.Profile,
			Model:     r.Model,
		}
	}

	carrierOrder := activeCarrierOrder(pol, "")
	carrierViews := make(map[string]statusCarrierView, len(carrierOrder))
	for _, c := range carrierOrder {
		matrix := make(map[string]statusTierCell)
		for _, tier := range validTiers {
			if m := findModelEntry(cat, c, tier, pol); m != nil {
				matrix[tier] = statusTierCell{
					ModelID: m.ID,
					Efforts: sortedEffortNames(m.Efforts),
				}
			}
		}
		budget := pol.Carriers[c]
		carrierViews[c] = statusCarrierView{
			Available:  carrierAvailableInCatalog(cat, c),
			Budget:     budget,
			TierMatrix: matrix,
		}
	}

	payload := statusPayload{
		SchemaVersion:      cat.SchemaVersion,
		CatalogCapturedAt:  cat.CapturedAt,
		CatalogFingerprint: cat.Fingerprint,
		CarrierOrder:       carrierOrder,
		PresetRoutes:       presetRoutes,
		Carriers:           carrierViews,
	}

	if asJSON {
		if err := writeJSONOut(stdout, payload); err != nil {
			return 1, err
		}
		return 0, nil
	}

	renderStatusTable(stdout, payload)
	return 0, nil
}

func renderStatusTable(w io.Writer, st statusPayload) {
	fmt.Fprintf(w, "Catalog: v%d  captured=%s  fp=%s  order=%s\n",
		st.SchemaVersion, st.CatalogCapturedAt, st.CatalogFingerprint, strings.Join(st.CarrierOrder, " > "))

	fmt.Fprintln(w, "\nPresets:")
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	gearKeys := make([]string, 0, len(st.PresetRoutes))
	for k := range st.PresetRoutes {
		gearKeys = append(gearKeys, k)
	}
	sort.Strings(gearKeys)
	for _, g := range gearKeys {
		pr := st.PresetRoutes[g]
		if pr.Carrier == "" {
			fmt.Fprintf(tw, "  %s\t%s\t->\t%s\n", g, pr.Preset, pr.Action)
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\t->\t%s\t%s\t[%s]\n", g, pr.Preset, pr.Carrier, pr.Model, pr.Profile)
	}
	_ = tw.Flush()

	fmt.Fprintln(w, "\nCarriers:")
	cw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(cw, "  CARRIER\tSTATUS\tAVAIL\tLIGHT\tSTANDARD\tFRONTIER\tCAP/RESET")
	for _, c := range st.CarrierOrder {
		v := st.Carriers[c]
		avail := "no"
		if v.Available {
			avail = "yes"
		}
		cellStr := func(tier string) string {
			cell, ok := v.TierMatrix[tier]
			if !ok {
				return "-"
			}
			return fmt.Sprintf("%s [%s]", cell.ModelID, strings.Join(cell.Efforts, ","))
		}
		extra := "-"
		var parts []string
		if v.Budget.Status == "low" {
			parts = append(parts, fmt.Sprintf("cap=%s:%s", v.Budget.MaxTierWhenLow, v.Budget.MaxEffortWhenLow))
		}
		if v.Budget.ResetAt != "" {
			parts = append(parts, "reset="+v.Budget.ResetAt)
		}
		if v.Budget.Note != "" {
			parts = append(parts, v.Budget.Note)
		}
		if len(parts) > 0 {
			extra = strings.Join(parts, "; ")
		}
		fmt.Fprintf(cw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			c, v.Budget.Status, avail, cellStr("light"), cellStr("standard"), cellStr("frontier"), extra)
	}
	_ = cw.Flush()
}

func runModelsOrder(home string, args []string, stdout io.Writer) (int, error) {
	if len(args) == 0 {
		return 1, fmt.Errorf("usage: deckpilot models order <carriers...>")
	}
	for _, c := range args {
		if !isValidCarrier(c) {
			return 1, fmt.Errorf("models order: unknown carrier %q (valid: %s)", c, strings.Join(defaultCarrierOrder, ", "))
		}
	}
	order := dedupeStrings(args)

	cat, err := ensureCatalogIn(home)
	if err != nil {
		return 1, err
	}
	now := deckpilotNow()
	pol, _, _, err := loadPolicyIn(home, now)
	if err != nil {
		return 1, err
	}
	pol.CarrierOrder = order
	pol.UpdatedAt = formatUTC(now)
	if err := atomicWriteJSON(modelPolicyPathIn(home), pol); err != nil {
		return 1, err
	}
	syncRes, err := syncAgentsJSON(home, cat, pol)
	if err != nil {
		return 1, err
	}
	payload := struct {
		CarrierOrder []string   `json:"carrier_order"`
		Sync         SyncResult `json:"sync"`
	}{
		CarrierOrder: order,
		Sync:         syncRes,
	}
	if err := writeJSONOut(stdout, payload); err != nil {
		return 1, err
	}
	return 0, nil
}

func runModelsBudget(home string, args []string, stdout io.Writer) (int, error) {
	var pos []string
	var maxTierLow, maxEffortLow, resetAtRaw, noteVal string
	var resetAtSet, clearResetAt, noteSet bool

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			pos = append(pos, arg)
			continue
		}
		name, inlineVal, hasInline := splitFlagToken(arg)
		switch name {
		case "--max-tier-low":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			maxTierLow = v
		case "--max-effort-low":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			maxEffortLow = v
		case "--reset-at":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			resetAtRaw = v
			resetAtSet = true
		case "--clear-reset-at":
			clearResetAt = true
		case "--note":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			noteVal = v
			noteSet = true
		default:
			return 1, fmt.Errorf("models budget: unknown flag %q", name)
		}
	}

	if len(pos) != 2 {
		return 1, fmt.Errorf("usage: deckpilot models budget <carrier> <ok|low|exhausted> [--max-tier-low T] [--max-effort-low E] [--reset-at RFC3339] [--clear-reset-at] [--note TEXT]")
	}
	carrier, status := pos[0], pos[1]
	if !isValidCarrier(carrier) {
		return 1, fmt.Errorf("models budget: unknown carrier %q (valid: %s)", carrier, strings.Join(defaultCarrierOrder, ", "))
	}
	if status != "ok" && status != "low" && status != "exhausted" {
		return 1, fmt.Errorf("models budget: invalid status %q (valid: ok, low, exhausted)", status)
	}
	if maxTierLow != "" && !isValidTier(maxTierLow) {
		return 1, fmt.Errorf("models budget: invalid --max-tier-low %q (valid: light, standard, frontier)", maxTierLow)
	}
	if maxEffortLow != "" && !isValidEffort(maxEffortLow) {
		return 1, fmt.Errorf("models budget: invalid --max-effort-low %q (valid: low, medium, high, xhigh, max, ultra)", maxEffortLow)
	}
	if resetAtSet && clearResetAt {
		return 1, fmt.Errorf("models budget: --reset-at and --clear-reset-at cannot be used together")
	}

	normalizedResetAt := ""
	if resetAtSet {
		parsedTime, err := parseStrictRFC3339(resetAtRaw)
		if err != nil {
			return 1, fmt.Errorf("models budget: %w", err)
		}
		normalizedResetAt = formatUTC(parsedTime)
	}

	cat, err := ensureCatalogIn(home)
	if err != nil {
		return 1, err
	}
	now := deckpilotNow()
	pol, _, _, err := loadPolicyIn(home, now)
	if err != nil {
		return 1, err
	}

	cfg := pol.Carriers[carrier]
	if cfg.TierPins == nil {
		cfg.TierPins = map[string]string{}
	}
	cfg.Status = status
	if maxTierLow != "" {
		cfg.MaxTierWhenLow = maxTierLow
	}
	if maxEffortLow != "" {
		cfg.MaxEffortWhenLow = maxEffortLow
	}
	if status == "ok" {
		cfg.ResetAt = ""
		if noteSet {
			cfg.Note = noteVal
		} else {
			cfg.Note = ""
		}
	} else {
		if resetAtSet {
			cfg.ResetAt = normalizedResetAt
		} else if clearResetAt {
			cfg.ResetAt = ""
		}
		if noteSet {
			cfg.Note = noteVal
		}
	}

	pol.Carriers[carrier] = cfg
	pol.UpdatedAt = formatUTC(now)
	if err := atomicWriteJSON(modelPolicyPathIn(home), pol); err != nil {
		return 1, err
	}
	syncRes, err := syncAgentsJSON(home, cat, pol)
	if err != nil {
		return 1, err
	}
	payload := struct {
		Carrier string        `json:"carrier"`
		Budget  CarrierBudget `json:"budget"`
		Sync    SyncResult    `json:"sync"`
	}{
		Carrier: carrier,
		Budget:  cfg,
		Sync:    syncRes,
	}
	if err := writeJSONOut(stdout, payload); err != nil {
		return 1, err
	}
	return 0, nil
}

func runModelsPin(home string, args []string, stdout io.Writer) (int, error) {
	var pos []string
	force := false
	for _, arg := range args {
		if arg == "--force" {
			force = true
			continue
		}
		if strings.HasPrefix(arg, "--") {
			return 1, fmt.Errorf("models pin: unknown flag %q", arg)
		}
		pos = append(pos, arg)
	}
	if len(pos) != 3 {
		return 1, fmt.Errorf("usage: deckpilot models pin <carrier> <tier> <model_id|auto> [--force]")
	}
	carrier, tier, modelID := pos[0], pos[1], pos[2]
	if !isValidCarrier(carrier) {
		return 1, fmt.Errorf("models pin: unknown carrier %q (valid: %s)", carrier, strings.Join(defaultCarrierOrder, ", "))
	}
	if !isValidTier(tier) {
		return 1, fmt.Errorf("models pin: invalid tier %q (valid: light, standard, frontier)", tier)
	}

	cat, err := ensureCatalogIn(home)
	if err != nil {
		return 1, err
	}
	now := deckpilotNow()
	pol, _, _, err := loadPolicyIn(home, now)
	if err != nil {
		return 1, err
	}

	cfg := pol.Carriers[carrier]
	if cfg.TierPins == nil {
		cfg.TierPins = map[string]string{}
	}
	if strings.EqualFold(modelID, "auto") {
		delete(cfg.TierPins, tier)
	} else {
		found := false
		for _, m := range cat.Carriers[carrier].Models {
			if m.ID == modelID {
				found = true
				break
			}
		}
		if !found && !force {
			return 1, fmt.Errorf("models pin: model %q not found in catalog for carrier %q (pass --force to pin anyway)", modelID, carrier)
		}
		cfg.TierPins[tier] = modelID
	}

	pol.Carriers[carrier] = cfg
	pol.UpdatedAt = formatUTC(now)
	if err := atomicWriteJSON(modelPolicyPathIn(home), pol); err != nil {
		return 1, err
	}
	syncRes, err := syncAgentsJSON(home, cat, pol)
	if err != nil {
		return 1, err
	}
	payload := struct {
		Carrier  string            `json:"carrier"`
		TierPins map[string]string `json:"tier_pins"`
		Sync     SyncResult        `json:"sync"`
	}{
		Carrier:  carrier,
		TierPins: cfg.TierPins,
		Sync:     syncRes,
	}
	if err := writeJSONOut(stdout, payload); err != nil {
		return 1, err
	}
	return 0, nil
}

func runModelsRoute(home string, args []string, stdout io.Writer) (int, error) {
	var tier, effort, gear, prefer string
	var excludes []string

	for i := 0; i < len(args); i++ {
		name, inlineVal, hasInline := splitFlagToken(args[i])
		switch name {
		case "--tier":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			tier = v
		case "--effort":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			effort = v
		case "--gear":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			gear = v
		case "--prefer":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			prefer = v
		case "--exclude":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			for _, part := range strings.Split(v, ",") {
				p := strings.TrimSpace(part)
				if p != "" {
					excludes = append(excludes, p)
				}
			}
		default:
			return 1, fmt.Errorf("models route: unknown argument %q", args[i])
		}
	}

	if prefer != "" && !isValidCarrier(prefer) {
		return 1, fmt.Errorf("models route: unknown --prefer carrier %q", prefer)
	}
	for _, ex := range excludes {
		if !isValidCarrier(ex) {
			return 1, fmt.Errorf("models route: unknown --exclude carrier %q", ex)
		}
	}

	cat, err := ensureCatalogIn(home)
	if err != nil {
		return 1, err
	}
	pol, _, autoRecovered, err := loadPolicyIn(home, deckpilotNow())
	if err != nil {
		return 1, err
	}
	if autoRecovered {
		_, _ = syncAgentsJSON(home, cat, pol)
	}

	resolvedTier, resolvedEffort, err := resolveTierEffort(pol, gear, tier, effort)
	if err != nil {
		return 1, err
	}

	res := selectRoute(cat, pol, resolvedTier, resolvedEffort, excludes, prefer)
	if err := writeJSONOut(stdout, res); err != nil {
		return 1, err
	}
	if !res.OK {
		return 2, nil
	}
	return 0, nil
}

func runModelsEscalate(home string, args []string, stdout io.Writer) (int, error) {
	var carrier, tier, effort, gear, kind string
	var tried []string
	hops := 1

	for i := 0; i < len(args); i++ {
		name, inlineVal, hasInline := splitFlagToken(args[i])
		switch name {
		case "--carrier":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			carrier = v
		case "--tier":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			tier = v
		case "--effort":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			effort = v
		case "--gear":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			gear = v
		case "--kind":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			kind = v
		case "--tried":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			for _, part := range strings.Split(v, ",") {
				p := strings.TrimSpace(part)
				if p != "" {
					tried = append(tried, p)
				}
			}
		case "--hops":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			n, parseErr := strconv.Atoi(v)
			if parseErr != nil || n < 0 {
				return 1, fmt.Errorf("models escalate: --hops must be a non-negative integer, got %q", v)
			}
			hops = n
		default:
			return 1, fmt.Errorf("models escalate: unknown argument %q", args[i])
		}
	}

	if carrier == "" {
		return 1, fmt.Errorf("models escalate: --carrier is required")
	}
	if !isValidCarrier(carrier) {
		return 1, fmt.Errorf("models escalate: unknown carrier %q (valid: %s)", carrier, strings.Join(defaultCarrierOrder, ", "))
	}
	if kind == "" {
		return 1, fmt.Errorf("models escalate: --kind is required (capability|loop|blocked|quota)")
	}
	switch kind {
	case "capability", "loop", "blocked", "quota":
	default:
		return 1, fmt.Errorf("models escalate: invalid --kind %q (valid: capability, loop, blocked, quota)", kind)
	}
	for _, tc := range tried {
		if !isValidCarrier(tc) {
			return 1, fmt.Errorf("models escalate: unknown --tried carrier %q", tc)
		}
	}

	cat, err := ensureCatalogIn(home)
	if err != nil {
		return 1, err
	}
	now := deckpilotNow()
	pol, _, autoRecovered, err := loadPolicyIn(home, now)
	if err != nil {
		return 1, err
	}

	resolvedTier, resolvedEffort, err := resolveTierEffort(pol, gear, tier, effort)
	if err != nil {
		return 1, err
	}

	if kind == "quota" {
		cfg := pol.Carriers[carrier]
		if cfg.TierPins == nil {
			cfg.TierPins = map[string]string{}
		}
		cfg.Status = "exhausted"
		// Preserve any existing reset_at rather than clearing it.
		cfg.Note = "tripped via escalate at " + formatUTC(now)
		pol.Carriers[carrier] = cfg
		pol.UpdatedAt = formatUTC(now)
		if err := atomicWriteJSON(modelPolicyPathIn(home), pol); err != nil {
			return 1, err
		}
		if _, err := syncAgentsJSON(home, cat, pol); err != nil {
			return 1, err
		}
	} else if autoRecovered {
		_, _ = syncAgentsJSON(home, cat, pol)
	}

	res := decideEscalation(cat, pol, EscalationInput{
		CurrentCarrier: carrier,
		CurrentTier:    resolvedTier,
		CurrentEffort:  resolvedEffort,
		FailureKind:    kind,
		TriedCarriers:  tried,
		HopCount:       hops,
	})
	if err := writeJSONOut(stdout, res); err != nil {
		return 1, err
	}
	if !res.OK {
		return 2, nil
	}
	return 0, nil
}

func runModelsCheckOutput(home string, args []string, stdout io.Writer, stdin io.Reader) (int, error) {
	var pos []string
	var filePath, tier, effort, resetAtRaw string
	cooldown := defaultCheckCooldown
	dryRun := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			pos = append(pos, arg)
			continue
		}
		name, inlineVal, hasInline := splitFlagToken(arg)
		switch name {
		case "--file":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			filePath = v
		case "--tier":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			tier = v
		case "--effort":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			effort = v
		case "--cooldown":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return 1, fmt.Errorf("models check-output: --cooldown must be a positive duration, got %q", v)
			}
			cooldown = d
		case "--reset-at":
			v, err := consumeFlagValue(args, &i, name, inlineVal, hasInline)
			if err != nil {
				return 1, err
			}
			resetAtRaw = v
		case "--dry-run":
			dryRun = true
		default:
			return 1, fmt.Errorf("models check-output: unknown flag %q", name)
		}
	}

	if len(pos) != 1 {
		return 1, fmt.Errorf("usage: deckpilot models check-output <carrier> [--file PATH] [--tier T] [--effort E] [--cooldown 15m] [--reset-at RFC3339] [--dry-run]")
	}
	carrier := pos[0]
	if !isValidCarrier(carrier) {
		return 1, fmt.Errorf("models check-output: unknown carrier %q (valid: %s)", carrier, strings.Join(defaultCarrierOrder, ", "))
	}

	if tier == "" {
		tier = "standard"
	}
	if effort == "" {
		effort = "medium"
	}
	if !isValidTier(tier) {
		return 1, fmt.Errorf("models check-output: invalid --tier %q", tier)
	}
	if !isValidEffort(effort) {
		return 1, fmt.Errorf("models check-output: invalid --effort %q", effort)
	}

	explicitResetAt := ""
	if resetAtRaw != "" {
		parsedTime, err := parseStrictRFC3339(resetAtRaw)
		if err != nil {
			return 1, fmt.Errorf("models check-output: %w", err)
		}
		explicitResetAt = formatUTC(parsedTime)
	}

	var text string
	if filePath != "" {
		raw, err := os.ReadFile(filePath)
		if err != nil {
			return 1, fmt.Errorf("models check-output: read %s: %w", filePath, err)
		}
		text = string(raw)
	} else {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return 1, fmt.Errorf("models check-output: read stdin: %w", err)
		}
		text = string(raw)
	}

	kind, matched := classifyOutputQuota(text)
	if kind == "" {
		payload := struct {
			Tripped bool   `json:"tripped"`
			DryRun  bool   `json:"dry_run,omitempty"`
			Carrier string `json:"carrier"`
		}{
			Tripped: false,
			DryRun:  dryRun,
			Carrier: carrier,
		}
		if err := writeJSONOut(stdout, payload); err != nil {
			return 1, err
		}
		return 0, nil
	}

	now := deckpilotNow()
	var cat *ModelCatalog
	var pol *ModelPolicy
	var err error
	if dryRun {
		// In --dry-run mode, never write catalog/policy/agents.json to disk.
		cat, err = readCatalogFile(modelCurrentCatalogPathIn(home))
		if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errLegacyCatalogSchema) {
			return 1, err
		}
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errLegacyCatalogSchema) {
			cat = buildCatalog()
		}
		pol, err = loadPolicyReadOnly(home, now)
		if err != nil {
			return 1, err
		}
	} else {
		cat, err = ensureCatalogIn(home)
		if err != nil {
			return 1, err
		}
		pol, _, _, err = loadPolicyIn(home, now)
		if err != nil {
			return 1, err
		}
	}

	updatedPol := clonePolicy(pol)
	cfg := updatedPol.Carriers[carrier]
	if cfg.TierPins == nil {
		cfg.TierPins = map[string]string{}
	}
	cfg.Status = "exhausted"
	cfg.ResetAt = resolveCheckOutputResetAt(cfg.ResetAt, explicitResetAt, kind, now, cooldown)
	cfg.Note = fmt.Sprintf("auto-tripped (%s) on %q at %s", kind, matched, formatUTC(now))
	updatedPol.Carriers[carrier] = cfg
	updatedPol.UpdatedAt = formatUTC(now)

	fallback := selectRoute(cat, updatedPol, tier, effort, []string{carrier}, "")

	var syncRes *SyncResult
	if !dryRun {
		if err := atomicWriteJSON(modelPolicyPathIn(home), updatedPol); err != nil {
			return 1, err
		}
		sr, err := syncAgentsJSON(home, cat, updatedPol)
		if err != nil {
			return 1, err
		}
		syncRes = &sr
	}

	payload := struct {
		Tripped  bool        `json:"tripped"`
		DryRun   bool        `json:"dry_run,omitempty"`
		Kind     string      `json:"kind"`
		Carrier  string      `json:"carrier"`
		Matched  string      `json:"matched"`
		ResetAt  string      `json:"reset_at"`
		Fallback RouteResult `json:"fallback"`
		Sync     *SyncResult `json:"sync,omitempty"`
	}{
		Tripped:  true,
		DryRun:   dryRun,
		Kind:     kind,
		Carrier:  carrier,
		Matched:  matched,
		ResetAt:  cfg.ResetAt,
		Fallback: fallback,
		Sync:     syncRes,
	}
	if err := writeJSONOut(stdout, payload); err != nil {
		return 1, err
	}
	if !fallback.OK {
		return 2, nil
	}
	return 0, nil
}

// loadPolicyReadOnly loads model-policy.json without writing back to disk (for --dry-run).
func loadPolicyReadOnly(home string, now time.Time) (*ModelPolicy, error) {
	path := modelPolicyPathIn(home)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultModelPolicy(now), nil
		}
		return nil, err
	}
	var pol ModelPolicy
	if err := json.Unmarshal(raw, &pol); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if pol.MaxEscalationHops <= 0 {
		pol.MaxEscalationHops = defaultMaxEscalationHops
	}
	if len(pol.CarrierOrder) == 0 {
		pol.CarrierOrder = append([]string(nil), defaultCarrierOrder...)
	}
	if pol.GearPresets == nil {
		pol.GearPresets = defaultGearPresets()
	}
	if pol.Carriers == nil {
		pol.Carriers = make(map[string]CarrierBudget, len(defaultCarrierOrder))
	}
	for _, c := range defaultCarrierOrder {
		if _, ok := pol.Carriers[c]; !ok {
			pol.Carriers[c] = defaultCarrierBudget()
		}
	}
	for c, cfg := range pol.Carriers {
		if cfg.Status == "" {
			cfg.Status = "ok"
		}
		if cfg.MaxTierWhenLow == "" {
			cfg.MaxTierWhenLow = "standard"
		}
		if cfg.MaxEffortWhenLow == "" {
			cfg.MaxEffortWhenLow = "medium"
		}
		if cfg.TierPins == nil {
			cfg.TierPins = map[string]string{}
		}
		if cfg.ResetAt != "" && cfg.Status != "ok" {
			if resetTime, parseErr := parseStrictRFC3339(cfg.ResetAt); parseErr == nil && !now.UTC().Before(resetTime) {
				cfg.Status = "ok"
				cfg.ResetAt = ""
			}
		}
		pol.Carriers[c] = cfg
	}
	return &pol, nil
}

func runModelsSync(home string, args []string, stdout io.Writer) (int, error) {
	if len(args) > 0 {
		return 1, fmt.Errorf("models sync: unexpected argument %q", args[0])
	}
	cat, err := ensureCatalogIn(home)
	if err != nil {
		return 1, err
	}
	now := deckpilotNow()
	pol, _, _, err := loadPolicyIn(home, now)
	if err != nil {
		return 1, err
	}
	if err := atomicWriteJSON(modelPolicyPathIn(home), pol); err != nil {
		return 1, err
	}
	syncRes, err := syncAgentsJSON(home, cat, pol)
	if err != nil {
		return 1, err
	}
	if err := writeJSONOut(stdout, syncRes); err != nil {
		return 1, err
	}
	return 0, nil
}
