package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	modelRouterManagedTag     = "deckpilot-model-router"
	modelCatalogSchemaVersion = 2
	modelPolicySchemaVersion  = 2
)

var (
	defaultCarrierOrder = []string{"agy", "codex", "claude"}
	validTiers          = []string{"light", "standard", "frontier"}
	tierRank            = map[string]int{"light": 1, "standard": 2, "frontier": 3}
	validEfforts        = []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	effortRank          = map[string]int{"low": 1, "medium": 2, "high": 3, "xhigh": 4, "max": 5, "ultra": 6}
	coreEfforts         = []string{"low", "medium", "high"}
)

// EffortSpec describes how a specific reasoning effort level is invoked on a carrier CLI.
type EffortSpec struct {
	RawID   string   `json:"raw_id"`
	CLIArgs []string `json:"cli_args"`
}

// ModelEntry represents one normalized model family in the two-axis (tier x effort) catalog.
type ModelEntry struct {
	ID            string                `json:"id"`
	DisplayName   string                `json:"display_name"`
	Tier          string                `json:"tier"`
	Priority      int                   `json:"priority"`
	Visibility    string                `json:"visibility"`
	Description   string                `json:"description,omitempty"`
	DefaultEffort string                `json:"default_effort,omitempty"`
	Efforts       map[string]EffortSpec `json:"efforts"`
}

// CarrierCatalog holds the collected models and tier defaults for one carrier.
type CarrierCatalog struct {
	Source       string            `json:"source"`
	FetchedAt    string            `json:"fetched_at,omitempty"`
	DefaultModel string            `json:"default_model,omitempty"`
	Available    bool              `json:"available"`
	Error        string            `json:"error"`
	TierDefaults map[string]string `json:"tier_defaults"`
	Models       []ModelEntry      `json:"models"`
}

// ModelCatalog is the persisted schema v2 catalog stored at model-catalog/current.json.
type ModelCatalog struct {
	SchemaVersion int                       `json:"schema_version"`
	CapturedAt    string                    `json:"captured_at"`
	Carriers      map[string]CarrierCatalog `json:"carriers"`
	Fingerprint   string                    `json:"fingerprint"`
}

// CatalogDiff summarizes added and removed model:effort keys per carrier.
type CatalogDiff struct {
	Added   map[string][]string `json:"added"`
	Removed map[string][]string `json:"removed"`
}

// SnapshotResult is returned by saveCatalogSnapshot.
type SnapshotResult struct {
	Changed             bool        `json:"changed"`
	Fingerprint         string      `json:"fingerprint"`
	PreviousFingerprint string      `json:"previous_fingerprint"`
	SnapshotFile        string      `json:"snapshot_file,omitempty"`
	Diff                CatalogDiff `json:"diff"`
}

// SnapshotHistoryItem describes one file under model-catalog/snapshots/.
type SnapshotHistoryItem struct {
	File          string `json:"file"`
	SchemaVersion int    `json:"schema_version"`
	CapturedAt    string `json:"captured_at"`
	Fingerprint   string `json:"fingerprint"`
}

// Test seams for external process / PATH probes.
var (
	runAgyModels   = defaultRunAgyModels
	lookPathClaude = defaultLookPathClaude
)

func defaultRunAgyModels(ctx context.Context) (string, string, error) {
	cmd := exec.CommandContext(ctx, "agy", "models")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func defaultLookPathClaude() (string, error) {
	return exec.LookPath("claude")
}

func formatUTC(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func isValidCarrier(c string) bool {
	_, ok := carrierTUIDefs[c]
	return ok
}

func isValidTier(t string) bool {
	_, ok := tierRank[t]
	return ok
}

func isValidEffort(e string) bool {
	_, ok := effortRank[e]
	return ok
}

func sortedEffortNames(efforts map[string]EffortSpec) []string {
	out := make([]string, 0, len(efforts))
	for e := range efforts {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, okI := effortRank[out[i]]
		if !okI {
			ri = 99
		}
		rj, okJ := effortRank[out[j]]
		if !okJ {
			rj = 99
		}
		if ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

func isRoutableModel(m ModelEntry) bool {
	if m.Visibility == "hide" {
		return false
	}
	if strings.Contains(strings.ToLower(m.ID), "orchestrator") {
		return false
	}
	return len(m.Efforts) > 0
}

// RE2-compatible token boundary matchers (Go regexp does not support lookaround).
var (
	effortSuffixRE  = regexp.MustCompile(`(?i)-(low|medium|high|xhigh|max|ultra)$`)
	displayEffortRE = regexp.MustCompile(`(?i)\s*\((low|medium|high|xhigh|max|ultra)\)\s*$`)

	agyFrontierRE   = regexp.MustCompile(`(?:^|[^a-z0-9])(carbon|opus)(?:$|[^a-z0-9])`)
	agyLightRE      = regexp.MustCompile(`(?:^|[^a-z0-9])(hydrogen|flash|mini|lite|haiku)(?:$|[^a-z0-9])`)
	agyStandardRE   = regexp.MustCompile(`(?:^|[^a-z0-9])(neon|argon|sonnet|oss)(?:$|[^a-z0-9])`)
	codexFrontierRE = regexp.MustCompile(`(?:^|[^a-z0-9])(astra)(?:$|[^a-z0-9])`)
	codexLightRE    = regexp.MustCompile(`(?:^|[^a-z0-9])(luna|mini|lite)(?:$|[^a-z0-9])`)

	agyTierPreferPatterns = map[string][]*regexp.Regexp{
		"light": {
			regexp.MustCompile(`(?i)^gemini-4-hydrogen$`),
			regexp.MustCompile(`(?i)hydrogen`),
			regexp.MustCompile(`(?i)flash`),
		},
		"standard": {
			regexp.MustCompile(`(?i)^gemini-4-neon$`),
			regexp.MustCompile(`(?i)neon`),
			regexp.MustCompile(`(?i)argon`),
		},
		"frontier": {
			regexp.MustCompile(`(?i)^gemini-4-carbon$`),
			regexp.MustCompile(`(?i)carbon`),
		},
	}
)

func classifyAgyTier(familyID string) string {
	lower := strings.ToLower(familyID)
	if agyFrontierRE.MatchString(lower) {
		return "frontier"
	}
	if agyLightRE.MatchString(lower) {
		return "light"
	}
	if agyStandardRE.MatchString(lower) {
		return "standard"
	}
	return "standard"
}

func classifyCodexTier(slug, description string) string {
	lowerSlug := strings.ToLower(slug)
	lowerDesc := strings.ToLower(description)
	if codexFrontierRE.MatchString(lowerSlug) || strings.Contains(lowerDesc, "most capable") {
		return "frontier"
	}
	if codexLightRE.MatchString(lowerSlug) || strings.Contains(lowerDesc, "easier tasks") || strings.Contains(lowerDesc, "simpler tasks") {
		return "light"
	}
	return "standard"
}

func computeTierDefaults(models []ModelEntry, preferPatterns map[string][]*regexp.Regexp) map[string]string {
	defaults := make(map[string]string)
	candidates := make([]ModelEntry, 0, len(models))
	for _, m := range models {
		if isRoutableModel(m) {
			candidates = append(candidates, m)
		}
	}
	for _, tier := range validTiers {
		var tierModels []ModelEntry
		for _, m := range candidates {
			if m.Tier == tier {
				tierModels = append(tierModels, m)
			}
		}
		if len(tierModels) == 0 {
			continue
		}
		chosen := ""
		if patterns, ok := preferPatterns[tier]; ok {
			for _, rx := range patterns {
				for _, m := range tierModels {
					if rx.MatchString(m.ID) {
						chosen = m.ID
						break
					}
				}
				if chosen != "" {
					break
				}
			}
		}
		if chosen == "" {
			chosen = tierModels[0].ID
		}
		defaults[tier] = chosen
	}
	return defaults
}

func normalizeAgyEntries(rawPairs [][2]string) ([]ModelEntry, map[string]string) {
	familyOrder := make([]string, 0)
	families := make(map[string]*ModelEntry)

	for _, pair := range rawPairs {
		modelID := strings.TrimSpace(pair[0])
		displayName := strings.TrimSpace(pair[1])
		if modelID == "" {
			continue
		}
		var effort, familyID, familyDisplay string
		if loc := effortSuffixRE.FindStringSubmatchIndex(modelID); loc != nil {
			effort = strings.ToLower(modelID[loc[2]:loc[3]])
			familyID = modelID[:loc[0]]
			familyDisplay = strings.TrimSpace(displayEffortRE.ReplaceAllString(displayName, ""))
			if familyDisplay == "" {
				familyDisplay = familyID
			}
		} else {
			effort = "medium"
			familyID = modelID
			familyDisplay = displayName
			if familyDisplay == "" {
				familyDisplay = familyID
			}
		}

		fam, exists := families[familyID]
		if !exists {
			familyOrder = append(familyOrder, familyID)
			fam = &ModelEntry{
				ID:          familyID,
				DisplayName: familyDisplay,
				Tier:        classifyAgyTier(familyID),
				Priority:    len(familyOrder),
				Visibility:  "list",
				Efforts:     make(map[string]EffortSpec),
			}
			families[familyID] = fam
		}
		fam.Efforts[effort] = EffortSpec{
			RawID:   modelID,
			CLIArgs: []string{"--model", modelID},
		}
	}

	models := make([]ModelEntry, 0, len(familyOrder))
	for _, fid := range familyOrder {
		models = append(models, *families[fid])
	}
	tierDefaults := computeTierDefaults(models, agyTierPreferPatterns)
	return models, tierDefaults
}

func collectAgyModels() CarrierCatalog {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stdout, stderr, err := runAgyModels(ctx)
	if err != nil && strings.TrimSpace(stdout) == "" {
		errMsg := strings.TrimSpace(stderr)
		if errMsg == "" {
			errMsg = err.Error()
		}
		return CarrierCatalog{
			Source:       "agy models",
			Available:    false,
			Error:        errMsg,
			TierDefaults: map[string]string{},
			Models:       []ModelEntry{},
		}
	}

	var rawPairs [][2]string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "\t") {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		modelID := strings.TrimSpace(parts[0])
		display := strings.TrimSpace(parts[1])
		if modelID == "" || strings.HasPrefix(modelID, "Fetching") {
			continue
		}
		rawPairs = append(rawPairs, [2]string{modelID, display})
	}

	models, tierDefaults := normalizeAgyEntries(rawPairs)
	errMsg := ""
	if err != nil {
		errMsg = strings.TrimSpace(stderr)
		if errMsg == "" {
			errMsg = err.Error()
		}
	}
	return CarrierCatalog{
		Source:       "agy models",
		Available:    err == nil && len(models) > 0,
		Error:        errMsg,
		TierDefaults: tierDefaults,
		Models:       models,
	}
}

type codexModelsCache struct {
	FetchedAt string `json:"fetched_at"`
	Models    []struct {
		Slug                    string `json:"slug"`
		DisplayName             string `json:"display_name"`
		Description             string `json:"description"`
		DefaultReasoningLevel   string `json:"default_reasoning_level"`
		Visibility              string `json:"visibility"`
		Priority                *int   `json:"priority"`
		SupportedReasoningLevel []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
	} `json:"models"`
}

func collectCodexModels() CarrierCatalog {
	userHome, err := deckpilotUserHome()
	if err != nil {
		return CarrierCatalog{
			Source:       "~/.codex/models_cache.json",
			Available:    false,
			Error:        err.Error(),
			TierDefaults: map[string]string{},
			Models:       []ModelEntry{},
		}
	}
	cachePath := filepath.Join(userHome, ".codex", "models_cache.json")
	rawBytes, err := os.ReadFile(cachePath)
	if err != nil {
		errMsg := err.Error()
		if errors.Is(err, os.ErrNotExist) {
			errMsg = "models_cache.json not found"
		}
		return CarrierCatalog{
			Source:       cachePath,
			Available:    false,
			Error:        errMsg,
			TierDefaults: map[string]string{},
			Models:       []ModelEntry{},
		}
	}

	var cache codexModelsCache
	if err := json.Unmarshal(rawBytes, &cache); err != nil {
		return CarrierCatalog{
			Source:       cachePath,
			Available:    false,
			Error:        err.Error(),
			TierDefaults: map[string]string{},
			Models:       []ModelEntry{},
		}
	}

	models := make([]ModelEntry, 0, len(cache.Models))
	for _, item := range cache.Models {
		slug := strings.TrimSpace(item.Slug)
		if slug == "" {
			continue
		}
		displayName := strings.TrimSpace(item.DisplayName)
		if displayName == "" {
			displayName = slug
		}
		vis := strings.TrimSpace(item.Visibility)
		if vis == "" {
			vis = "list"
		}
		prio := 999
		if item.Priority != nil {
			prio = *item.Priority
		}

		efforts := make(map[string]EffortSpec)
		for _, rl := range item.SupportedReasoningLevel {
			e := strings.ToLower(strings.TrimSpace(rl.Effort))
			if e == "" {
				continue
			}
			if _, exists := efforts[e]; !exists {
				efforts[e] = EffortSpec{
					RawID:   slug,
					CLIArgs: []string{"-m", slug, "-c", "model_reasoning_effort=" + e},
				}
			}
		}
		if len(efforts) == 0 {
			for _, e := range coreEfforts {
				efforts[e] = EffortSpec{
					RawID:   slug,
					CLIArgs: []string{"-m", slug, "-c", "model_reasoning_effort=" + e},
				}
			}
		}

		models = append(models, ModelEntry{
			ID:            slug,
			DisplayName:   displayName,
			Tier:          classifyCodexTier(slug, item.Description),
			Priority:      prio,
			Visibility:    vis,
			Description:   item.Description,
			DefaultEffort: item.DefaultReasoningLevel,
			Efforts:       efforts,
		})
	}

	sort.Slice(models, func(i, j int) bool {
		vi := models[i].Visibility != "list"
		vj := models[j].Visibility != "list"
		if vi != vj {
			return !vi
		}
		if models[i].Priority != models[j].Priority {
			return models[i].Priority < models[j].Priority
		}
		return models[i].ID < models[j].ID
	})

	tierDefaults := computeTierDefaults(models, nil)
	return CarrierCatalog{
		Source:       cachePath,
		FetchedAt:    cache.FetchedAt,
		Available:    len(models) > 0,
		Error:        "",
		TierDefaults: tierDefaults,
		Models:       models,
	}
}

func collectClaudeModels() CarrierCatalog {
	defaultModel := "opus"
	if userHome, err := deckpilotUserHome(); err == nil {
		settingsPath := filepath.Join(userHome, ".claude", "settings.json")
		if rawBytes, err := os.ReadFile(settingsPath); err == nil {
			var cfg struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(rawBytes, &cfg) == nil && strings.TrimSpace(cfg.Model) != "" {
				defaultModel = strings.TrimSpace(cfg.Model)
			}
		}
	}

	type claudeSpec struct {
		id      string
		display string
		tier    string
		prio    int
		efforts []string
	}
	specs := []claudeSpec{
		{"haiku", "Claude Haiku (latest alias)", "light", 1, []string{"low", "medium"}},
		{"sonnet", "Claude Sonnet (latest alias)", "standard", 2, []string{"low", "medium", "high"}},
		{"opus", "Claude Opus (latest alias)", "frontier", 3, []string{"low", "medium", "high"}},
	}

	models := make([]ModelEntry, 0, len(specs))
	for _, s := range specs {
		effMap := make(map[string]EffortSpec, len(s.efforts))
		for _, e := range s.efforts {
			effMap[e] = EffortSpec{
				RawID:   s.id,
				CLIArgs: []string{"--model", s.id, "--effort", e},
			}
		}
		models = append(models, ModelEntry{
			ID:          s.id,
			DisplayName: s.display,
			Tier:        s.tier,
			Priority:    s.prio,
			Visibility:  "list",
			Efforts:     effMap,
		})
	}
	tierDefaults := computeTierDefaults(models, nil)

	available := true
	errMsg := ""
	if _, err := lookPathClaude(); err != nil {
		available = false
		errMsg = err.Error()
	}

	return CarrierCatalog{
		Source:       "claude aliases + ~/.claude/settings.json",
		DefaultModel: defaultModel,
		Available:    available,
		Error:        errMsg,
		TierDefaults: tierDefaults,
		Models:       models,
	}
}

func catalogFingerprint(cat *ModelCatalog) string {
	type fpModel struct {
		Efforts    []string `json:"efforts"`
		ID         string   `json:"id"`
		Tier       string   `json:"tier"`
		Visibility string   `json:"visibility"`
	}
	type fpCarrier struct {
		Models       []fpModel         `json:"models"`
		TierDefaults map[string]string `json:"tier_defaults"`
	}

	stable := make(map[string]fpCarrier, len(cat.Carriers))
	for carrier, payload := range cat.Carriers {
		ms := make([]fpModel, 0, len(payload.Models))
		for _, m := range payload.Models {
			effs := make([]string, 0, len(m.Efforts))
			for e := range m.Efforts {
				effs = append(effs, e)
			}
			sort.Strings(effs)
			ms = append(ms, fpModel{
				Efforts:    effs,
				ID:         m.ID,
				Tier:       m.Tier,
				Visibility: m.Visibility,
			})
		}
		td := payload.TierDefaults
		if td == nil {
			td = map[string]string{}
		}
		stable[carrier] = fpCarrier{
			Models:       ms,
			TierDefaults: td,
		}
	}
	blob, _ := json.Marshal(stable)
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])[:16]
}

func buildCatalog() *ModelCatalog {
	cat := &ModelCatalog{
		SchemaVersion: modelCatalogSchemaVersion,
		CapturedAt:    formatUTC(deckpilotNow()),
		Carriers: map[string]CarrierCatalog{
			"agy":    collectAgyModels(),
			"codex":  collectCodexModels(),
			"claude": collectClaudeModels(),
		},
	}
	cat.Fingerprint = catalogFingerprint(cat)
	return cat
}

func diffCatalogs(oldCat, newCat *ModelCatalog) CatalogDiff {
	diff := CatalogDiff{
		Added:   map[string][]string{},
		Removed: map[string][]string{},
	}
	keysOf := func(cc CarrierCatalog) map[string]struct{} {
		out := make(map[string]struct{})
		for _, m := range cc.Models {
			if len(m.Efforts) > 0 {
				for e := range m.Efforts {
					out[m.ID+":"+e] = struct{}{}
				}
			} else {
				out[m.ID] = struct{}{}
			}
		}
		return out
	}

	carrierSet := make(map[string]struct{})
	if oldCat != nil {
		for c := range oldCat.Carriers {
			carrierSet[c] = struct{}{}
		}
	}
	if newCat != nil {
		for c := range newCat.Carriers {
			carrierSet[c] = struct{}{}
		}
	}
	carriers := make([]string, 0, len(carrierSet))
	for c := range carrierSet {
		carriers = append(carriers, c)
	}
	sort.Strings(carriers)

	for _, c := range carriers {
		var oldCC, newCC CarrierCatalog
		if oldCat != nil {
			oldCC = oldCat.Carriers[c]
		}
		if newCat != nil {
			newCC = newCat.Carriers[c]
		}
		oldK := keysOf(oldCC)
		newK := keysOf(newCC)

		var added, removed []string
		for k := range newK {
			if _, ok := oldK[k]; !ok {
				added = append(added, k)
			}
		}
		for k := range oldK {
			if _, ok := newK[k]; !ok {
				removed = append(removed, k)
			}
		}
		sort.Strings(added)
		sort.Strings(removed)
		if len(added) > 0 {
			diff.Added[c] = added
		}
		if len(removed) > 0 {
			diff.Removed[c] = removed
		}
	}
	return diff
}

var errLegacyCatalogSchema = errors.New("legacy catalog schema")

type catalogHeader struct {
	SchemaVersion int    `json:"schema_version"`
	CapturedAt    string `json:"captured_at"`
	Fingerprint   string `json:"fingerprint"`
}

func readCatalogHeader(path string) (catalogHeader, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return catalogHeader{}, err
	}
	var hdr catalogHeader
	if err := json.Unmarshal(raw, &hdr); err != nil {
		return catalogHeader{}, fmt.Errorf("parse catalog %s: %w", path, err)
	}
	return hdr, nil
}

func readCatalogFile(path string) (*ModelCatalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var hdr catalogHeader
	if err := json.Unmarshal(raw, &hdr); err != nil {
		return nil, fmt.Errorf("parse catalog %s: %w", path, err)
	}
	if hdr.SchemaVersion > 0 && hdr.SchemaVersion < modelCatalogSchemaVersion {
		legacy := &ModelCatalog{
			SchemaVersion: hdr.SchemaVersion,
			CapturedAt:    hdr.CapturedAt,
			Fingerprint:   hdr.Fingerprint,
			Carriers:      map[string]CarrierCatalog{},
		}
		return legacy, fmt.Errorf("%w: %s has schema_version %d (need %d)", errLegacyCatalogSchema, path, hdr.SchemaVersion, modelCatalogSchemaVersion)
	}
	var cat ModelCatalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, fmt.Errorf("parse catalog %s: %w", path, err)
	}
	if cat.Carriers == nil {
		cat.Carriers = map[string]CarrierCatalog{}
	}
	return &cat, nil
}

func saveCatalogSnapshot(home string, cat *ModelCatalog, force bool, now time.Time) (SnapshotResult, error) {
	currPath := modelCurrentCatalogPathIn(home)
	prev, err := readCatalogFile(currPath)
	isLegacyPrev := errors.Is(err, errLegacyCatalogSchema)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !isLegacyPrev {
		return SnapshotResult{}, err
	}
	if errors.Is(err, os.ErrNotExist) {
		prev = nil
	}

	prevFP := ""
	prevSchema := 0
	if prev != nil {
		prevFP = prev.Fingerprint
		prevSchema = prev.SchemaVersion
	}
	changed := (prev == nil) || (prevFP != cat.Fingerprint) || (prevSchema != cat.SchemaVersion)

	snapshotFile := ""
	if changed || force {
		stamp := now.UTC().Format("2006-01-02_150405")
		snapshotFile = filepath.Join(modelSnapshotsDirIn(home), fmt.Sprintf("%s_%s.json", stamp, cat.Fingerprint))
		if err := atomicWriteJSON(snapshotFile, cat); err != nil {
			return SnapshotResult{}, fmt.Errorf("write snapshot: %w", err)
		}
		if err := atomicWriteJSON(currPath, cat); err != nil {
			return SnapshotResult{}, fmt.Errorf("write current catalog: %w", err)
		}
	}

	diff := CatalogDiff{Added: map[string][]string{}, Removed: map[string][]string{}}
	if prev != nil && !isLegacyPrev {
		diff = diffCatalogs(prev, cat)
	}
	return SnapshotResult{
		Changed:             changed,
		Fingerprint:         cat.Fingerprint,
		PreviousFingerprint: prevFP,
		SnapshotFile:        snapshotFile,
		Diff:                diff,
	}, nil
}

func ensureCatalogIn(home string) (*ModelCatalog, error) {
	cat, err := readCatalogFile(modelCurrentCatalogPathIn(home))
	if err == nil && cat.SchemaVersion >= modelCatalogSchemaVersion {
		return cat, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errLegacyCatalogSchema) {
		return nil, err
	}
	cat = buildCatalog()
	if _, err := saveCatalogSnapshot(home, cat, false, deckpilotNow()); err != nil {
		return nil, err
	}
	return cat, nil
}

func listCatalogHistory(home string) ([]SnapshotHistoryItem, error) {
	sdir := modelSnapshotsDirIn(home)
	entries, err := os.ReadDir(sdir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []SnapshotHistoryItem{}, nil
		}
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	items := make([]SnapshotHistoryItem, 0, len(names))
	for _, name := range names {
		hdr, err := readCatalogHeader(filepath.Join(sdir, name))
		if err != nil {
			items = append(items, SnapshotHistoryItem{File: name})
			continue
		}
		sv := hdr.SchemaVersion
		if sv == 0 {
			sv = 1
		}
		items = append(items, SnapshotHistoryItem{
			File:          name,
			SchemaVersion: sv,
			CapturedAt:    hdr.CapturedAt,
			Fingerprint:   hdr.Fingerprint,
		})
	}
	return items, nil
}
