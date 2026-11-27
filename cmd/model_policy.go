package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	defaultMaxEscalationHops = 4
	defaultCheckCooldown     = 15 * time.Minute
)

// GearPreset maps a shortcut gear name (L1/L2/L3) onto a (tier, effort) pair.
type GearPreset struct {
	Tier   string `json:"tier"`
	Effort string `json:"effort"`
}

// CarrierBudget holds per-carrier quota state, low-budget caps, recovery time, and tier pins.
type CarrierBudget struct {
	Status           string            `json:"status"`
	MaxTierWhenLow   string            `json:"max_tier_when_low"`
	MaxEffortWhenLow string            `json:"max_effort_when_low"`
	ResetAt          string            `json:"reset_at"`
	Note             string            `json:"note"`
	TierPins         map[string]string `json:"tier_pins"`
}

// ModelPolicy is the operator-configurable routing policy stored at model-policy.json.
type ModelPolicy struct {
	SchemaVersion      int                      `json:"schema_version"`
	UpdatedAt          string                   `json:"updated_at"`
	CarrierOrder       []string                 `json:"carrier_order"`
	MaxEscalationHops  int                      `json:"max_escalation_hops"`
	OverrideBuiltinAgy bool                     `json:"override_builtin_agy"`
	GearPresets        map[string]GearPreset    `json:"gear_presets"`
	Carriers           map[string]CarrierBudget `json:"carriers"`
}

// ResolvedPair represents one resolved (carrier, model, tier, effort) combination.
type ResolvedPair struct {
	Carrier          string   `json:"carrier"`
	ModelID          string   `json:"model_id"`
	DisplayName      string   `json:"display_name"`
	Tier             string   `json:"tier"`
	Effort           string   `json:"effort"`
	RawID            string   `json:"raw_id"`
	CLIArgs          []string `json:"cli_args"`
	SupportedEfforts []string `json:"supported_efforts"`
}

// RouteResult is the structured JSON output of selectRoute and `models route`.
type RouteResult struct {
	OK              bool     `json:"ok"`
	Action          string   `json:"action"`
	Carrier         string   `json:"carrier,omitempty"`
	RequestedTier   string   `json:"requested_tier"`
	RequestedEffort string   `json:"requested_effort"`
	EffectiveTier   string   `json:"effective_tier,omitempty"`
	EffectiveEffort string   `json:"effective_effort,omitempty"`
	Downgraded      bool     `json:"downgraded,omitempty"`
	Profile         string   `json:"profile,omitempty"`
	Model           string   `json:"model,omitempty"`
	ModelID         string   `json:"model_id,omitempty"`
	CLIArgs         []string `json:"cli_args,omitempty"`
	BudgetStatus    string   `json:"budget_status,omitempty"`
	Reason          string   `json:"reason,omitempty"`
}

// EscalationInput holds the parameters for decideEscalation.
type EscalationInput struct {
	CurrentCarrier string
	CurrentTier    string
	CurrentEffort  string
	FailureKind    string
	TriedCarriers  []string
	HopCount       int
}

// EscalationDecision is the unified JSON output of decideEscalation and `models escalate`.
type EscalationDecision struct {
	OK            bool         `json:"ok"`
	Action        string       `json:"action"`
	Reason        string       `json:"reason"`
	TriedCarriers []string     `json:"tried_carriers"`
	NextHop       int          `json:"next_hop"`
	Route         *RouteResult `json:"route"`
}

func defaultGearPresets() map[string]GearPreset {
	return map[string]GearPreset{
		"L1": {Tier: "light", Effort: "low"},
		"L2": {Tier: "standard", Effort: "medium"},
		"L3": {Tier: "frontier", Effort: "high"},
	}
}

func defaultCarrierBudget() CarrierBudget {
	return CarrierBudget{
		Status:           "ok",
		MaxTierWhenLow:   "standard",
		MaxEffortWhenLow: "medium",
		ResetAt:          "",
		Note:             "",
		TierPins:         map[string]string{},
	}
}

func defaultModelPolicy(now time.Time) *ModelPolicy {
	carriers := make(map[string]CarrierBudget, len(defaultCarrierOrder))
	for _, c := range defaultCarrierOrder {
		carriers[c] = defaultCarrierBudget()
	}
	order := make([]string, len(defaultCarrierOrder))
	copy(order, defaultCarrierOrder)
	return &ModelPolicy{
		SchemaVersion:      modelPolicySchemaVersion,
		UpdatedAt:          formatUTC(now),
		CarrierOrder:       order,
		MaxEscalationHops:  defaultMaxEscalationHops,
		OverrideBuiltinAgy: false,
		GearPresets:        defaultGearPresets(),
		Carriers:           carriers,
	}
}

func clonePolicy(pol *ModelPolicy) *ModelPolicy {
	if pol == nil {
		return nil
	}
	cp := *pol
	cp.CarrierOrder = append([]string(nil), pol.CarrierOrder...)
	cp.GearPresets = make(map[string]GearPreset, len(pol.GearPresets))
	for k, v := range pol.GearPresets {
		cp.GearPresets[k] = v
	}
	cp.Carriers = make(map[string]CarrierBudget, len(pol.Carriers))
	for k, v := range pol.Carriers {
		pins := make(map[string]string, len(v.TierPins))
		for pk, pv := range v.TierPins {
			pins[pk] = pv
		}
		v.TierPins = pins
		cp.Carriers[k] = v
	}
	return &cp
}

func parseStrictRFC3339(raw string) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty RFC3339 timestamp")
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid RFC3339 timestamp %q: %w", raw, err)
	}
	return t.UTC(), nil
}

// loadPolicyIn reads model-policy.json non-destructively (preserving unknown carriers
// and non-zero operator values like max_escalation_hops: 3), fills only zero-value
// defaults, and auto-recovers any carrier whose reset_at has passed.
// Returns (policy, mutated, autoRecovered, err).
func loadPolicyIn(home string, now time.Time) (*ModelPolicy, bool, bool, error) {
	path := modelPolicyPathIn(home)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultModelPolicy(now), true, false, nil
		}
		return nil, false, false, err
	}

	var pol ModelPolicy
	if err := json.Unmarshal(raw, &pol); err != nil {
		return nil, false, false, fmt.Errorf("parse %s: %w", path, err)
	}

	mutated := false
	autoRecovered := false

	if pol.SchemaVersion < modelPolicySchemaVersion {
		pol.SchemaVersion = modelPolicySchemaVersion
		mutated = true
	}
	if pol.MaxEscalationHops <= 0 {
		pol.MaxEscalationHops = defaultMaxEscalationHops
		mutated = true
	}
	if len(pol.CarrierOrder) == 0 {
		pol.CarrierOrder = append([]string(nil), defaultCarrierOrder...)
		mutated = true
	}
	if pol.GearPresets == nil {
		pol.GearPresets = defaultGearPresets()
		mutated = true
	} else {
		for k, v := range defaultGearPresets() {
			if _, ok := pol.GearPresets[k]; !ok {
				pol.GearPresets[k] = v
				mutated = true
			}
		}
	}
	if pol.Carriers == nil {
		pol.Carriers = make(map[string]CarrierBudget, len(defaultCarrierOrder))
		mutated = true
	}

	// Ensure default carriers exist without removing any extra/unknown carriers.
	for _, c := range defaultCarrierOrder {
		if _, ok := pol.Carriers[c]; !ok {
			pol.Carriers[c] = defaultCarrierBudget()
			mutated = true
		}
	}

	// Fill zero-value defaults and check reset_at auto-recovery on all carriers.
	for c, cfg := range pol.Carriers {
		changed := false
		if cfg.Status == "" {
			cfg.Status = "ok"
			changed = true
		}
		if cfg.MaxTierWhenLow == "" {
			cfg.MaxTierWhenLow = "standard"
			changed = true
		}
		if cfg.MaxEffortWhenLow == "" {
			cfg.MaxEffortWhenLow = "medium"
			changed = true
		}
		if cfg.TierPins == nil {
			cfg.TierPins = map[string]string{}
			changed = true
		}
		if cfg.ResetAt != "" && cfg.Status != "ok" {
			if resetTime, parseErr := parseStrictRFC3339(cfg.ResetAt); parseErr == nil && !now.UTC().Before(resetTime) {
				cfg.Status = "ok"
				cfg.ResetAt = ""
				cfg.Note = "auto-recovered at " + formatUTC(now)
				changed = true
				autoRecovered = true
			}
		}
		if changed {
			pol.Carriers[c] = cfg
			mutated = true
		}
	}

	if mutated {
		pol.UpdatedAt = formatUTC(now)
		if err := atomicWriteJSON(path, &pol); err != nil {
			return nil, false, false, fmt.Errorf("persist policy %s: %w", path, err)
		}
	}
	return &pol, mutated, autoRecovered, nil
}

func findModelEntry(cat *ModelCatalog, carrier, tier string, pol *ModelPolicy) *ModelEntry {
	if cat == nil {
		return nil
	}
	cdata, ok := cat.Carriers[carrier]
	if !ok || len(cdata.Models) == 0 {
		return nil
	}
	byID := make(map[string]*ModelEntry, len(cdata.Models))
	for i := range cdata.Models {
		byID[cdata.Models[i].ID] = &cdata.Models[i]
	}

	if pol != nil {
		if cfg, ok := pol.Carriers[carrier]; ok {
			if pinID := cfg.TierPins[tier]; pinID != "" {
				if m, ok := byID[pinID]; ok && len(m.Efforts) > 0 {
					return m
				}
			}
		}
	}

	if defID := cdata.TierDefaults[tier]; defID != "" {
		if m, ok := byID[defID]; ok && len(m.Efforts) > 0 {
			return m
		}
	}

	// Fallback: only visible, non-orchestrator models with efforts.
	for i := range cdata.Models {
		m := &cdata.Models[i]
		if m.Tier == tier && isRoutableModel(*m) {
			return m
		}
	}
	return nil
}

func pickEffortForModel(m *ModelEntry, requestedEffort string) (string, EffortSpec, bool) {
	if m == nil || len(m.Efforts) == 0 {
		return "", EffortSpec{}, false
	}
	if spec, ok := m.Efforts[requestedEffort]; ok {
		return requestedEffort, spec, true
	}
	reqRank, ok := effortRank[requestedEffort]
	if !ok {
		reqRank = 2
	}

	// Highest available effort <= requestedEffort.
	bestLower := ""
	bestLowerRank := -1
	for e := range m.Efforts {
		r, ok := effortRank[e]
		if !ok {
			r = 99
		}
		if r <= reqRank && r > bestLowerRank {
			bestLower = e
			bestLowerRank = r
		}
	}
	if bestLower != "" {
		return bestLower, m.Efforts[bestLower], true
	}

	// Otherwise lowest available effort.
	lowest := ""
	lowestRank := 999
	for e := range m.Efforts {
		r, ok := effortRank[e]
		if !ok {
			r = 99
		}
		if r < lowestRank || (r == lowestRank && e < lowest) {
			lowest = e
			lowestRank = r
		}
	}
	if lowest != "" {
		return lowest, m.Efforts[lowest], true
	}
	return "", EffortSpec{}, false
}

func carrierAllows(pol *ModelPolicy, carrier, tier, effort string) bool {
	if pol == nil {
		return true
	}
	cfg, ok := pol.Carriers[carrier]
	if !ok {
		return true
	}
	switch cfg.Status {
	case "exhausted":
		return false
	case "low":
		maxTierRank := tierRank[cfg.MaxTierWhenLow]
		if maxTierRank == 0 {
			maxTierRank = 2
		}
		maxEffortRank := effortRank[cfg.MaxEffortWhenLow]
		if maxEffortRank == 0 {
			maxEffortRank = 2
		}
		if tierRank[tier] > maxTierRank {
			return false
		}
		if effortRank[effort] > maxEffortRank {
			return false
		}
	}
	return true
}

func carrierAvailableInCatalog(cat *ModelCatalog, carrier string) bool {
	if cat == nil {
		return false
	}
	cdata, ok := cat.Carriers[carrier]
	if !ok {
		return false
	}
	return cdata.Available
}

func resolveCarrierPair(cat *ModelCatalog, pol *ModelPolicy, carrier, tier, effort string) *ResolvedPair {
	m := findModelEntry(cat, carrier, tier, pol)
	if m == nil {
		return nil
	}
	effActual, spec, ok := pickEffortForModel(m, effort)
	if !ok {
		return nil
	}
	rawID := spec.RawID
	if rawID == "" {
		rawID = m.ID
	}
	disp := m.DisplayName
	if disp == "" {
		disp = m.ID
	}
	return &ResolvedPair{
		Carrier:          carrier,
		ModelID:          m.ID,
		DisplayName:      disp,
		Tier:             tier,
		Effort:           effActual,
		RawID:            rawID,
		CLIArgs:          append([]string(nil), spec.CLIArgs...),
		SupportedEfforts: sortedEffortNames(m.Efforts),
	}
}

func activeCarrierOrder(pol *ModelPolicy, preferCarrier string) []string {
	var rawOrder []string
	if pol != nil && len(pol.CarrierOrder) > 0 {
		rawOrder = pol.CarrierOrder
	} else {
		rawOrder = defaultCarrierOrder
	}
	seen := make(map[string]struct{}, len(rawOrder))
	order := make([]string, 0, len(rawOrder))
	for _, c := range rawOrder {
		if !isValidCarrier(c) {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		order = append(order, c)
	}
	if preferCarrier != "" {
		if _, inOrder := seen[preferCarrier]; inOrder {
			reordered := make([]string, 0, len(order))
			reordered = append(reordered, preferCarrier)
			for _, c := range order {
				if c != preferCarrier {
					reordered = append(reordered, c)
				}
			}
			order = reordered
		}
	}
	return order
}

// selectRoute chooses the best available carrier for (tier, effort) honoring
// carrier_order and 2D budget caps. For each candidate (candTier, candEffort),
// an exact-effort pass across all carriers runs before falling back to clamped
// effort resolution so a higher-priority carrier that lacks the requested effort
// never preempts another carrier that supports it natively.
func selectRoute(
	cat *ModelCatalog,
	pol *ModelPolicy,
	tier, effort string,
	excludeCarriers []string,
	preferCarrier string,
) RouteResult {
	excluded := make(map[string]struct{}, len(excludeCarriers))
	for _, c := range excludeCarriers {
		if c != "" {
			excluded[c] = struct{}{}
		}
	}

	order := activeCarrierOrder(pol, preferCarrier)

	tRank := tierRank[tier]
	if tRank == 0 {
		tRank = 2
		tier = "standard"
	}
	eRank := effortRank[effort]
	if eRank == 0 {
		eRank = 2
		effort = "medium"
	}

	tierCandidates := make([]string, 0, tRank)
	for i := tRank - 1; i >= 0; i-- {
		tierCandidates = append(tierCandidates, validTiers[i])
	}
	effortCandidates := make([]string, 0, eRank)
	for i := eRank - 1; i >= 0; i-- {
		effortCandidates = append(effortCandidates, validEfforts[i])
	}

	buildResult := func(carrier, candTier string, resolved *ResolvedPair) RouteResult {
		downgraded := (candTier != tier) || (resolved.Effort != effort)
		action := "RUN"
		if downgraded {
			action = "RUN_DOWNGRADED"
		}
		budgetStatus := "ok"
		if pol != nil {
			if cfg, ok := pol.Carriers[carrier]; ok && cfg.Status != "" {
				budgetStatus = cfg.Status
			}
		}
		res := RouteResult{
			OK:              true,
			Action:          action,
			Carrier:         carrier,
			RequestedTier:   tier,
			RequestedEffort: effort,
			EffectiveTier:   candTier,
			EffectiveEffort: resolved.Effort,
			Downgraded:      downgraded,
			Profile:         fmt.Sprintf("%s-%s-%s", carrier, candTier, resolved.Effort),
			Model:           fmt.Sprintf("%s (%s)", resolved.ModelID, resolved.Effort),
			ModelID:         resolved.ModelID,
			CLIArgs:         append([]string(nil), resolved.CLIArgs...),
			BudgetStatus:    budgetStatus,
		}
		if downgraded {
			res.Reason = fmt.Sprintf(
				"Budget cap or model limits adjusted (%s:%s) -> %s (%s:%s).",
				tier, effort, carrier, candTier, resolved.Effort,
			)
		}
		return res
	}

	for _, candTier := range tierCandidates {
		for _, candEffort := range effortCandidates {
			// Pass 1: strict effort match across all eligible carriers.
			for _, carrier := range order {
				if _, skip := excluded[carrier]; skip {
					continue
				}
				if !carrierAvailableInCatalog(cat, carrier) {
					continue
				}
				if !carrierAllows(pol, carrier, candTier, candEffort) {
					continue
				}
				resolved := resolveCarrierPair(cat, pol, carrier, candTier, candEffort)
				if resolved == nil || resolved.Effort != candEffort {
					continue
				}
				return buildResult(carrier, candTier, resolved)
			}

			// Pass 2: clamped effort fallback across eligible carriers.
			for _, carrier := range order {
				if _, skip := excluded[carrier]; skip {
					continue
				}
				if !carrierAvailableInCatalog(cat, carrier) {
					continue
				}
				if !carrierAllows(pol, carrier, candTier, candEffort) {
					continue
				}
				resolved := resolveCarrierPair(cat, pol, carrier, candTier, candEffort)
				if resolved == nil {
					continue
				}
				if !carrierAllows(pol, carrier, candTier, resolved.Effort) {
					continue
				}
				return buildResult(carrier, candTier, resolved)
			}
		}
	}

	return RouteResult{
		OK:              false,
		Action:          "STOP_AND_ASK_USER",
		RequestedTier:   tier,
		RequestedEffort: effort,
		Reason:          "All eligible carriers are exhausted or excluded.",
	}
}

func nextEffort(currentEffort, capEffort string) string {
	idx := effortRank[currentEffort]
	if idx == 0 {
		idx = 2
	}
	capIdx := effortRank[capEffort]
	if capIdx == 0 {
		capIdx = 3
	}
	if idx >= capIdx || idx >= len(validEfforts) {
		return ""
	}
	return validEfforts[idx]
}

func nextTier(currentTier string) string {
	idx := tierRank[currentTier]
	if idx == 0 {
		idx = 2
	}
	if idx >= len(validTiers) {
		return ""
	}
	return validTiers[idx]
}

// compareTierEffort compares two (tier, effort) pairs lexicographically.
// Returns -1 if (t1,e1) < (t2,e2), 0 if equal, +1 if greater.
func compareTierEffort(t1, e1, t2, e2 string) int {
	tr1, tr2 := tierRank[t1], tierRank[t2]
	if tr1 != tr2 {
		if tr1 < tr2 {
			return -1
		}
		return 1
	}
	er1, er2 := effortRank[e1], effortRank[e2]
	if er1 != er2 {
		if er1 < er2 {
			return -1
		}
		return 1
	}
	return 0
}

func dedupeStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, s := range items {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func decideEscalation(cat *ModelCatalog, pol *ModelPolicy, in EscalationInput) EscalationDecision {
	tried := dedupeStrings(append(append([]string(nil), in.TriedCarriers...), in.CurrentCarrier))
	maxHops := defaultMaxEscalationHops
	if pol != nil && pol.MaxEscalationHops > 0 {
		maxHops = pol.MaxEscalationHops
	}

	stop := func(reason string) EscalationDecision {
		return EscalationDecision{
			OK:            false,
			Action:        "STOP_AND_ASK_USER",
			Reason:        reason,
			TriedCarriers: tried,
			NextHop:       in.HopCount,
			Route:         nil,
		}
	}

	if in.FailureKind == "blocked" {
		return stop("Task is blocked by missing prerequisites, contradictory specs, or broken environment. Do not burn higher tiers/efforts.")
	}

	// Quota failover runs BEFORE max_escalation_hops and does not increment the reasoning hop count.
	if in.FailureKind == "quota" {
		route := selectRoute(cat, pol, in.CurrentTier, in.CurrentEffort, tried, "")
		if route.OK {
			route.Action = "FAILOVER_CARRIER"
			route.Reason = fmt.Sprintf("Carrier %q hit quota/rate limit; switching to %q.", in.CurrentCarrier, route.Carrier)
			return EscalationDecision{
				OK:            true,
				Action:        "FAILOVER_CARRIER",
				Reason:        route.Reason,
				TriedCarriers: tried,
				NextHop:       in.HopCount,
				Route:         &route,
			}
		}
		return stop(fmt.Sprintf("Carrier %q hit quota/rate limit and no remaining carrier can take over.", in.CurrentCarrier))
	}

	if in.HopCount >= maxHops {
		return stop(fmt.Sprintf("Reached max escalation hops (%d). Likely impossible/underspecified; ask user with evidence.", maxHops))
	}

	switch in.FailureKind {
	case "loop":
		if len(tried) >= 2 {
			return stop(fmt.Sprintf("Multiple carrier families (%s) stalled on the same issue. Stop and ask user.", strings.Join(tried, ", ")))
		}
		targetTier := in.CurrentTier
		if targetTier == "light" {
			targetTier = "standard"
		}
		targetEffort := in.CurrentEffort
		if targetEffort == "low" {
			targetEffort = "medium"
		}
		route := selectRoute(cat, pol, targetTier, targetEffort, tried, "")
		if route.OK {
			route.Action = "SECOND_OPINION_OTHER_CARRIER"
			route.Reason = fmt.Sprintf(
				"Same-error loop on %q. Switching to different carrier %q (%s:%s) instead of blindly raising effort.",
				in.CurrentCarrier, route.Carrier, route.EffectiveTier, route.EffectiveEffort,
			)
			return EscalationDecision{
				OK:            true,
				Action:        "SECOND_OPINION_OTHER_CARRIER",
				Reason:        route.Reason,
				TriedCarriers: tried,
				NextHop:       in.HopCount + 1,
				Route:         &route,
			}
		}
		return stop("Loop detected and no other carrier has remaining budget for a second opinion.")

	case "capability":
		// Step 1: raise effort within same tier (light capped at medium; others capped at high).
		// Require a non-downgraded resolution so an ok peer carrier can step in rather than jumping to frontier.
		capEffort := "high"
		if in.CurrentTier == "light" {
			capEffort = "medium"
		}
		if nextEff := nextEffort(in.CurrentEffort, capEffort); nextEff != "" {
			route := selectRoute(cat, pol, in.CurrentTier, nextEff, nil, in.CurrentCarrier)
			if route.OK && !route.Downgraded {
				route.Action = "EFFORT_UP"
				route.Reason = fmt.Sprintf(
					"Keeping tier %q and raising effort %s -> %s on %q.",
					in.CurrentTier, in.CurrentEffort, route.EffectiveEffort, route.Carrier,
				)
				return EscalationDecision{
					OK:            true,
					Action:        "EFFORT_UP",
					Reason:        route.Reason,
					TriedCarriers: tried,
					NextHop:       in.HopCount + 1,
					Route:         &route,
				}
			}
		}

		// Step 2: step up model tier, starting at medium effort to avoid immediate overspend.
		if nextT := nextTier(in.CurrentTier); nextT != "" {
			startEff := "medium"
			route := selectRoute(cat, pol, nextT, startEff, nil, in.CurrentCarrier)
			if !route.OK {
				return stop(route.Reason)
			}
			// Guard against regression/stall loops on the same carrier.
			if route.Carrier == in.CurrentCarrier && compareTierEffort(route.EffectiveTier, route.EffectiveEffort, in.CurrentTier, in.CurrentEffort) <= 0 {
				return stop(fmt.Sprintf(
					"Wanted tier %s:%s but carrier %q is capped at or below current level (%s:%s).",
					nextT, startEff, in.CurrentCarrier, route.EffectiveTier, route.EffectiveEffort,
				))
			}
			action := "TIER_UP"
			if route.Downgraded {
				action = "BUDGET_CAPPED"
			}
			route.Action = action
			route.Reason = fmt.Sprintf(
				"Upgrading model tier %s:%s -> %s:%s on %q.",
				in.CurrentTier, in.CurrentEffort, route.EffectiveTier, route.EffectiveEffort, route.Carrier,
			)
			return EscalationDecision{
				OK:            true,
				Action:        action,
				Reason:        route.Reason,
				TriedCarriers: tried,
				NextHop:       in.HopCount + 1,
				Route:         &route,
			}
		}

		// Step 3: already at frontier:high -> allow one cross-carrier second opinion before stopping.
		if len(tried) >= 2 {
			return stop("Already tried frontier:high across multiple carriers without convergence.")
		}
		route := selectRoute(cat, pol, "frontier", "high", tried, "")
		if route.OK {
			route.Action = "SECOND_OPINION_OTHER_CARRIER"
			route.Reason = fmt.Sprintf(
				"Already at frontier:high on %q; switching to %q (%s:%s).",
				in.CurrentCarrier, route.Carrier, route.EffectiveTier, route.EffectiveEffort,
			)
			return EscalationDecision{
				OK:            true,
				Action:        "SECOND_OPINION_OTHER_CARRIER",
				Reason:        route.Reason,
				TriedCarriers: tried,
				NextHop:       in.HopCount + 1,
				Route:         &route,
			}
		}
		return stop("Already at frontier:high and no other carrier has remaining budget.")

	default:
		return stop(fmt.Sprintf("Unknown failure kind %q.", in.FailureKind))
	}
}

// Quota & rate-limit output detectors, separated so transient rate limits set a
// cooldown while hard quota exhaustion trips until manual/explicit reset.
var (
	hardQuotaRE = regexp.MustCompile(
		`(?i)quota\s*(?:exceeded|exhausted|reached|limit)|(?:weekly|daily|usage|token)\s*limit(?:\s*(?:exceeded|reached))?|credit balance is too low|利用上限|上限に達|クォータ超過`,
	)
	rateLimitRE = regexp.MustCompile(
		`(?i)(?:http(?:/[0-9.]+)?[/\s]*|status[:=\s]*|error[:=\s]*)429(?:$|[^0-9])|429\s+too\s+many\s+requests|too many requests|rate[\s_-]*limit[\s_-]*(?:exceeded|reached|hit|error)`,
	)
)

// classifyOutputQuota inspects terminal text and returns ("quota"|"rate_limit"|"", matchedSubstring).
// Hard quota takes precedence over transient rate limits when both appear.
func classifyOutputQuota(text string) (string, string) {
	if m := hardQuotaRE.FindString(text); m != "" {
		return "quota", strings.TrimSpace(m)
	}
	if m := rateLimitRE.FindString(text); m != "" {
		return "rate_limit", strings.TrimSpace(m)
	}
	return "", ""
}

// resolveCheckOutputResetAt computes the next reset_at value for check-output without
// clearing an already valid reset_at on repeated trips.
func resolveCheckOutputResetAt(existingResetAt, explicitResetAt, kind string, now time.Time, cooldown time.Duration) string {
	if explicitResetAt != "" {
		return explicitResetAt
	}
	if kind == "rate_limit" {
		candidate := now.UTC().Add(cooldown)
		if existingResetAt != "" {
			if existingTime, err := parseStrictRFC3339(existingResetAt); err == nil && existingTime.After(candidate) {
				return formatUTC(existingTime)
			}
		}
		return formatUTC(candidate)
	}
	// Hard quota: preserve any existing valid reset_at instead of wiping it to "".
	if existingResetAt != "" {
		if existingTime, err := parseStrictRFC3339(existingResetAt); err == nil {
			return formatUTC(existingTime)
		}
	}
	return ""
}
