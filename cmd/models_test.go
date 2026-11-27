package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const sampleAgyModelsOutput = `Fetching models...
gemini-4-neon-low	Gemini 4 Neon (Low)
gemini-4-neon-medium	Gemini 4 Neon (Medium)
gemini-4-neon-high	Gemini 4 Neon (High)
gemini-4-neon-xhigh	Gemini 4 Neon (XHigh)
gemini-4-carbon-low	Gemini 4 Carbon (Low)
gemini-4-carbon-medium	Gemini 4 Carbon (Medium)
gemini-4-carbon-high	Gemini 4 Carbon (High)
gemini-4-hydrogen-low	Gemini 4 Hydrogen (Low)
gemini-4-hydrogen-medium	Gemini 4 Hydrogen (Medium)
gemini-4-hydrogen-high	Gemini 4 Hydrogen (High)
gemini-pro-orchestrator-neon	Orchestrator (Neon)
gpt-oss-120b-medium	GPT-OSS 120B (Medium)
`

const sampleCodexCacheJSON = `{
  "fetched_at": "2026-11-27T23:00:00Z",
  "models": [
    {
      "slug": "gpt-6.2-sol",
      "display_name": "GPT-6.2-Sol",
      "description": "Fast, go-to model for coding and everyday work.",
      "default_reasoning_level": "low",
      "visibility": "list",
      "priority": 1,
      "supported_reasoning_levels": [
        {"effort": "low"},
        {"effort": "medium"},
        {"effort": "high"},
        {"effort": "xhigh"},
        {"effort": "max"},
        {"effort": "ultra"}
      ]
    },
    {
      "slug": "gpt-6.2-astra",
      "display_name": "GPT-6.2-Astra",
      "description": "Our most capable model for complex coding.",
      "default_reasoning_level": "low",
      "visibility": "list",
      "priority": 2,
      "supported_reasoning_levels": [
        {"effort": "low"},
        {"effort": "medium"},
        {"effort": "high"},
        {"effort": "xhigh"},
        {"effort": "max"},
        {"effort": "ultra"}
      ]
    },
    {
      "slug": "gpt-6-luna",
      "display_name": "GPT-6-Luna",
      "description": "Fast and affordable model for easier tasks.",
      "default_reasoning_level": "medium",
      "visibility": "list",
      "priority": 3,
      "supported_reasoning_levels": [
        {"effort": "low"},
        {"effort": "medium"},
        {"effort": "high"}
      ]
    },
    {
      "slug": "gpt-hidden-top",
      "display_name": "Hidden Top",
      "description": "Hidden internal model.",
      "visibility": "hide",
      "priority": 0,
      "supported_reasoning_levels": [
        {"effort": "low"},
        {"effort": "medium"}
      ]
    }
  ]
}`

func setupModelRouterTestEnv(t *testing.T, now time.Time) (userHome, dpHome string) {
	t.Helper()
	t.Setenv("DECKPILOT_HOME", "")

	userHome = useTempHome(t)
	dpHome = filepath.Join(userHome, ".deckpilot")

	oldNow := deckpilotNow
	oldAgy := runAgyModels
	oldClaude := lookPathClaude
	t.Cleanup(func() {
		deckpilotNow = oldNow
		runAgyModels = oldAgy
		lookPathClaude = oldClaude
	})

	deckpilotNow = func() time.Time { return now }
	runAgyModels = func(ctx context.Context) (string, string, error) {
		return sampleAgyModelsOutput, "", nil
	}
	lookPathClaude = func() (string, error) {
		return `C:\mock\claude.exe`, nil
	}

	codexDir := filepath.Join(userHome, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "models_cache.json"), []byte(sampleCodexCacheJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	claudeDir := filepath.Join(userHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"model":"sonnet"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return userHome, dpHome
}

func runModelsCLI(t *testing.T, stdin string, args ...string) (int, string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code, err := ModelsWithError(args, &stdout, &stderr, strings.NewReader(stdin))
	return code, stdout.String(), stderr.String(), err
}

func TestDeckpilotHomeDirHonorsEnvOverride(t *testing.T) {
	useTempHome(t)
	custom := filepath.Join(t.TempDir(), "custom-dp")
	t.Setenv("DECKPILOT_HOME", custom)

	if got := deckpilotHomeDir(); got != custom {
		t.Fatalf("deckpilotHomeDir() = %q, want %q", got, custom)
	}
	if got := modelCatalogDir(); got != filepath.Join(custom, "model-catalog") {
		t.Fatalf("modelCatalogDir() = %q", got)
	}
	if got := modelSnapshotsDir(); got != filepath.Join(custom, "model-catalog", "snapshots") {
		t.Fatalf("modelSnapshotsDir() = %q", got)
	}
	if got := modelCurrentCatalogPath(); got != filepath.Join(custom, "model-catalog", "current.json") {
		t.Fatalf("modelCurrentCatalogPath() = %q", got)
	}
	if got := modelPolicyPath(); got != filepath.Join(custom, "model-policy.json") {
		t.Fatalf("modelPolicyPath() = %q", got)
	}
}

func TestClassifyTierTokenBoundaryDoesNotFalseMatchGeminiAsMini(t *testing.T) {
	agyCases := []struct {
		id   string
		want string
	}{
		{"gemini", "standard"},
		{"gemini-4-neon", "standard"},
		{"gemini-4-argon", "standard"},
		{"gemini-4-carbon", "frontier"},
		{"claude-opus-5-5", "frontier"},
		{"gemini-4-hydrogen", "light"},
		{"gemini-3.8-flash", "light"},
		{"gemini-mini", "light"},
		{"model-lite-v2", "light"},
		{"claude-haiku-4", "light"},
		{"claude-sonnet-5-5", "standard"},
		{"gpt-oss-120b", "standard"},
	}
	for _, tc := range agyCases {
		if got := classifyAgyTier(tc.id); got != tc.want {
			t.Errorf("classifyAgyTier(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}

	codexCases := []struct {
		slug string
		desc string
		want string
	}{
		{"gemini-custom", "", "standard"},
		{"gpt-6.2-sol", "Fast, go-to model", "standard"},
		{"gpt-6.2-astra", "Complex coding", "frontier"},
		{"gpt-6-new", "Our most capable model", "frontier"},
		{"gpt-6-luna", "Everyday", "light"},
		{"gpt-6-mini", "Everyday", "light"},
		{"gpt-6-fast", "Affordable model for easier tasks", "light"},
	}
	for _, tc := range codexCases {
		if got := classifyCodexTier(tc.slug, tc.desc); got != tc.want {
			t.Errorf("classifyCodexTier(%q, %q) = %q, want %q", tc.slug, tc.desc, got, tc.want)
		}
	}
}

func TestCollectorsNormalizeAndRespectVisibilityAndOrchestratorFilter(t *testing.T) {
	now := time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC)
	setupModelRouterTestEnv(t, now)

	cat := buildCatalog()
	if cat.SchemaVersion != 2 {
		t.Fatalf("schema_version = %d, want 2", cat.SchemaVersion)
	}
	if len(cat.Carriers) != 3 {
		t.Fatalf("expected exactly 3 carriers (agy/codex/claude), got %v", cat.Carriers)
	}
	if _, hasGemini := cat.Carriers["gemini"]; hasGemini {
		t.Fatal("legacy gemini carrier must not be present")
	}

	agy := cat.Carriers["agy"]
	if !agy.Available {
		t.Fatalf("expected agy available, error=%q", agy.Error)
	}
	if agy.TierDefaults["light"] != "gemini-4-hydrogen" ||
		agy.TierDefaults["standard"] != "gemini-4-neon" ||
		agy.TierDefaults["frontier"] != "gemini-4-carbon" {
		t.Fatalf("unexpected agy tier_defaults: %+v", agy.TierDefaults)
	}

	codex := cat.Carriers["codex"]
	if !codex.Available {
		t.Fatalf("expected codex available, error=%q", codex.Error)
	}
	if codex.TierDefaults["standard"] != "gpt-6.2-sol" {
		t.Fatalf("codex tier_defaults must skip hidden model gpt-hidden-top, got %+v", codex.TierDefaults)
	}

	claude := cat.Carriers["claude"]
	if !claude.Available || claude.DefaultModel != "sonnet" {
		t.Fatalf("unexpected claude catalog: %+v", claude)
	}

	// Verify findModelEntry final fallback also excludes visibility=="hide" and "orchestrator".
	synthetic := &ModelCatalog{
		SchemaVersion: 2,
		Carriers: map[string]CarrierCatalog{
			"agy": {
				Available:    true,
				TierDefaults: map[string]string{},
				Models: []ModelEntry{
					{
						ID:         "gemini-pro-orchestrator-neon",
						Tier:       "standard",
						Visibility: "list",
						Efforts:    map[string]EffortSpec{"medium": {RawID: "gemini-pro-orchestrator-neon", CLIArgs: []string{"--model", "gemini-pro-orchestrator-neon"}}},
					},
					{
						ID:         "hidden-standard",
						Tier:       "standard",
						Visibility: "hide",
						Efforts:    map[string]EffortSpec{"medium": {RawID: "hidden-standard", CLIArgs: []string{"--model", "hidden-standard"}}},
					},
					{
						ID:         "gemini-4-argon",
						Tier:       "standard",
						Visibility: "list",
						Efforts:    map[string]EffortSpec{"medium": {RawID: "gemini-4-argon-medium", CLIArgs: []string{"--model", "gemini-4-argon-medium"}}},
					},
				},
			},
		},
	}
	m := findModelEntry(synthetic, "agy", "standard", defaultModelPolicy(now))
	if m == nil || m.ID != "gemini-4-argon" {
		t.Fatalf("findModelEntry fallback should skip orchestrator and hidden models, got %+v", m)
	}

	// Verify lookPathClaude seam controls availability.
	lookPathClaude = func() (string, error) { return "", errors.New("claude not found") }
	claudeMissing := collectClaudeModels()
	if claudeMissing.Available {
		t.Fatal("expected claude available=false when LookPath fails")
	}
}

func TestLoadPolicyNonDestructiveAndAutoRecovery(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	_, dpHome := setupModelRouterTestEnv(t, now)

	// Existing v2 policy with max_escalation_hops: 3, unknown carrier "customx", and expired reset_at.
	existing := `{
  "schema_version": 2,
  "updated_at": "2026-11-27T00:00:00Z",
  "carrier_order": ["agy", "codex", "claude", "customx"],
  "max_escalation_hops": 3,
  "override_builtin_agy": false,
  "carriers": {
    "agy": {
      "status": "exhausted",
      "reset_at": "2026-11-28T11:00:00Z",
      "note": "rate limited",
      "max_tier_when_low": "standard",
      "max_effort_when_low": "medium",
      "tier_pins": {}
    },
    "customx": {
      "status": "ok",
      "reset_at": "",
      "note": "keep me",
      "max_tier_when_low": "standard",
      "max_effort_when_low": "medium",
      "tier_pins": {}
    }
  }
}`
	if err := os.MkdirAll(dpHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPolicyPathIn(dpHome), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	pol, mutated, recovered, err := loadPolicyIn(dpHome, now)
	if err != nil {
		t.Fatalf("loadPolicyIn: %v", err)
	}
	if !mutated || !recovered {
		t.Fatalf("expected mutated=true recovered=true, got mutated=%v recovered=%v", mutated, recovered)
	}
	if pol.MaxEscalationHops != 3 {
		t.Fatalf("max_escalation_hops must be preserved as 3, got %d", pol.MaxEscalationHops)
	}
	if _, ok := pol.Carriers["customx"]; !ok {
		t.Fatal("unknown carrier 'customx' must not be silently deleted from policy.Carriers")
	}
	if !reflect.DeepEqual(pol.CarrierOrder, []string{"agy", "codex", "claude", "customx"}) {
		t.Fatalf("carrier_order was unexpectedly mutated: %v", pol.CarrierOrder)
	}
	if pol.Carriers["agy"].Status != "ok" || pol.Carriers["agy"].ResetAt != "" {
		t.Fatalf("expected agy to auto-recover to ok, got %+v", pol.Carriers["agy"])
	}
	if _, ok := pol.Carriers["codex"]; !ok {
		t.Fatal("missing default carrier codex should be backfilled")
	}
}

func TestSelectRouteExactEffortPassBeforeClamping(t *testing.T) {
	now := time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC)
	setupModelRouterTestEnv(t, now)
	cat := buildCatalog()

	cases := []struct {
		name         string
		setupPolicy  func(*ModelPolicy)
		tier         string
		effort       string
		wantOK       bool
		wantCarrier  string
		wantTier     string
		wantEffort   string
		wantDowngrad bool
	}{
		{
			name: "claude lacks light:high so exact pass picks agy light:high",
			setupPolicy: func(p *ModelPolicy) {
				p.CarrierOrder = []string{"claude", "agy", "codex"}
			},
			tier:         "light",
			effort:       "high",
			wantOK:       true,
			wantCarrier:  "agy",
			wantTier:     "light",
			wantEffort:   "high",
			wantDowngrad: false,
		},
		{
			name: "when only claude remains, clamps light:high to light:medium",
			setupPolicy: func(p *ModelPolicy) {
				p.CarrierOrder = []string{"claude", "agy", "codex"}
				agy := p.Carriers["agy"]
				agy.Status = "exhausted"
				p.Carriers["agy"] = agy
				codex := p.Carriers["codex"]
				codex.Status = "exhausted"
				p.Carriers["codex"] = codex
			},
			tier:         "light",
			effort:       "high",
			wantOK:       true,
			wantCarrier:  "claude",
			wantTier:     "light",
			wantEffort:   "medium",
			wantDowngrad: true,
		},
		{
			name: "pinned medium-only model does not preempt exact low on next carrier",
			setupPolicy: func(p *ModelPolicy) {
				p.CarrierOrder = []string{"agy", "codex", "claude"}
				agy := p.Carriers["agy"]
				agy.TierPins["standard"] = "gpt-oss-120b"
				p.Carriers["agy"] = agy
			},
			tier:         "standard",
			effort:       "low",
			wantOK:       true,
			wantCarrier:  "codex",
			wantTier:     "standard",
			wantEffort:   "low",
			wantDowngrad: false,
		},
		{
			name: "xhigh skips claude and agy frontier, picks codex frontier:xhigh",
			setupPolicy: func(p *ModelPolicy) {
				p.CarrierOrder = []string{"agy", "claude", "codex"}
			},
			tier:         "frontier",
			effort:       "xhigh",
			wantOK:       true,
			wantCarrier:  "codex",
			wantTier:     "frontier",
			wantEffort:   "xhigh",
			wantDowngrad: false,
		},
		{
			name: "all exhausted returns STOP_AND_ASK_USER",
			setupPolicy: func(p *ModelPolicy) {
				for _, c := range defaultCarrierOrder {
					cfg := p.Carriers[c]
					cfg.Status = "exhausted"
					p.Carriers[c] = cfg
				}
			},
			tier:   "standard",
			effort: "medium",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pol := defaultModelPolicy(now)
			if tc.setupPolicy != nil {
				tc.setupPolicy(pol)
			}
			got := selectRoute(cat, pol, tc.tier, tc.effort, nil, "")
			if got.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (res=%+v)", got.OK, tc.wantOK, got)
			}
			if !tc.wantOK {
				if got.Action != "STOP_AND_ASK_USER" {
					t.Fatalf("Action = %q, want STOP_AND_ASK_USER", got.Action)
				}
				return
			}
			if got.Carrier != tc.wantCarrier || got.EffectiveTier != tc.wantTier || got.EffectiveEffort != tc.wantEffort || got.Downgraded != tc.wantDowngrad {
				t.Fatalf("got (%s %s:%s downgraded=%v), want (%s %s:%s downgraded=%v)",
					got.Carrier, got.EffectiveTier, got.EffectiveEffort, got.Downgraded,
					tc.wantCarrier, tc.wantTier, tc.wantEffort, tc.wantDowngrad)
			}
		})
	}
}

func TestDecideEscalationEdgeCases(t *testing.T) {
	now := time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC)
	setupModelRouterTestEnv(t, now)
	cat := buildCatalog()

	cases := []struct {
		name        string
		setupPolicy func(*ModelPolicy)
		input       EscalationInput
		wantOK      bool
		wantAction  string
		wantNextHop int
		wantCarrier string
		wantTier    string
		wantEffort  string
	}{
		{
			name: "blocked stops immediately",
			input: EscalationInput{
				CurrentCarrier: "agy", CurrentTier: "standard", CurrentEffort: "low",
				FailureKind: "blocked", HopCount: 1,
			},
			wantOK: false, wantAction: "STOP_AND_ASK_USER", wantNextHop: 1,
		},
		{
			name: "quota runs BEFORE max_escalation_hops and keeps hop count",
			setupPolicy: func(p *ModelPolicy) {
				p.MaxEscalationHops = 3
				agy := p.Carriers["agy"]
				agy.Status = "exhausted"
				p.Carriers["agy"] = agy
			},
			input: EscalationInput{
				CurrentCarrier: "agy", CurrentTier: "standard", CurrentEffort: "medium",
				FailureKind: "quota", HopCount: 3,
			},
			wantOK: true, wantAction: "FAILOVER_CARRIER", wantNextHop: 3,
			wantCarrier: "codex", wantTier: "standard", wantEffort: "medium",
		},
		{
			name: "quota excludes both tried and current",
			setupPolicy: func(p *ModelPolicy) {
				p.MaxEscalationHops = 2
			},
			input: EscalationInput{
				CurrentCarrier: "codex", CurrentTier: "standard", CurrentEffort: "medium",
				FailureKind: "quota", TriedCarriers: []string{"agy"}, HopCount: 5,
			},
			wantOK: true, wantAction: "FAILOVER_CARRIER", wantNextHop: 5,
			wantCarrier: "claude", wantTier: "standard", wantEffort: "medium",
		},
		{
			name: "max hops stops capability escalation",
			setupPolicy: func(p *ModelPolicy) {
				p.MaxEscalationHops = 3
			},
			input: EscalationInput{
				CurrentCarrier: "agy", CurrentTier: "standard", CurrentEffort: "low",
				FailureKind: "capability", HopCount: 3,
			},
			wantOK: false, wantAction: "STOP_AND_ASK_USER", wantNextHop: 3,
		},
		{
			name: "loop switches carrier and lifts light:low to standard:medium",
			input: EscalationInput{
				CurrentCarrier: "agy", CurrentTier: "light", CurrentEffort: "low",
				FailureKind: "loop", HopCount: 1,
			},
			wantOK: true, wantAction: "SECOND_OPINION_OTHER_CARRIER", wantNextHop: 2,
			wantCarrier: "codex", wantTier: "standard", wantEffort: "medium",
		},
		{
			name: "loop stops when 2 carrier families tried",
			input: EscalationInput{
				CurrentCarrier: "codex", CurrentTier: "standard", CurrentEffort: "medium",
				FailureKind: "loop", TriedCarriers: []string{"agy"}, HopCount: 2,
			},
			wantOK: false, wantAction: "STOP_AND_ASK_USER", wantNextHop: 2,
		},
		{
			name: "capability light:low raises effort to light:medium",
			input: EscalationInput{
				CurrentCarrier: "agy", CurrentTier: "light", CurrentEffort: "low",
				FailureKind: "capability", HopCount: 1,
			},
			wantOK: true, wantAction: "EFFORT_UP", wantNextHop: 2,
			wantCarrier: "agy", wantTier: "light", wantEffort: "medium",
		},
		{
			name: "capability light:medium caps light at medium and tiers up to standard:medium",
			input: EscalationInput{
				CurrentCarrier: "agy", CurrentTier: "light", CurrentEffort: "medium",
				FailureKind: "capability", HopCount: 1,
			},
			wantOK: true, wantAction: "TIER_UP", wantNextHop: 2,
			wantCarrier: "agy", wantTier: "standard", wantEffort: "medium",
		},
		{
			name: "capability standard:medium when current carrier is low uses peer standard:high instead of jumping to frontier",
			setupPolicy: func(p *ModelPolicy) {
				agy := p.Carriers["agy"]
				agy.Status = "low"
				agy.MaxTierWhenLow = "standard"
				agy.MaxEffortWhenLow = "medium"
				p.Carriers["agy"] = agy
			},
			input: EscalationInput{
				CurrentCarrier: "agy", CurrentTier: "standard", CurrentEffort: "medium",
				FailureKind: "capability", HopCount: 1,
			},
			wantOK: true, wantAction: "EFFORT_UP", wantNextHop: 2,
			wantCarrier: "codex", wantTier: "standard", wantEffort: "high",
		},
		{
			name: "capability step 2 prevents same-carrier regression loop (standard:high -> standard:medium) and stops",
			setupPolicy: func(p *ModelPolicy) {
				agy := p.Carriers["agy"]
				agy.Status = "low"
				agy.MaxTierWhenLow = "standard"
				agy.MaxEffortWhenLow = "medium"
				p.Carriers["agy"] = agy
				for _, other := range []string{"codex", "claude"} {
					cfg := p.Carriers[other]
					cfg.Status = "exhausted"
					p.Carriers[other] = cfg
				}
			},
			input: EscalationInput{
				CurrentCarrier: "agy", CurrentTier: "standard", CurrentEffort: "high",
				FailureKind: "capability", HopCount: 1,
			},
			wantOK: false, wantAction: "STOP_AND_ASK_USER", wantNextHop: 1,
		},
		{
			name: "capability frontier:high tries one other carrier",
			input: EscalationInput{
				CurrentCarrier: "agy", CurrentTier: "frontier", CurrentEffort: "high",
				FailureKind: "capability", HopCount: 1,
			},
			wantOK: true, wantAction: "SECOND_OPINION_OTHER_CARRIER", wantNextHop: 2,
			wantCarrier: "codex", wantTier: "frontier", wantEffort: "high",
		},
		{
			name: "capability frontier:high stops after 2 carriers tried",
			input: EscalationInput{
				CurrentCarrier: "codex", CurrentTier: "frontier", CurrentEffort: "high",
				FailureKind: "capability", TriedCarriers: []string{"agy"}, HopCount: 2,
			},
			wantOK: false, wantAction: "STOP_AND_ASK_USER", wantNextHop: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pol := defaultModelPolicy(now)
			if tc.setupPolicy != nil {
				tc.setupPolicy(pol)
			}
			got := decideEscalation(cat, pol, tc.input)
			if got.OK != tc.wantOK || got.Action != tc.wantAction || got.NextHop != tc.wantNextHop {
				t.Fatalf("got OK=%v Action=%q NextHop=%d (reason=%q), want OK=%v Action=%q NextHop=%d",
					got.OK, got.Action, got.NextHop, got.Reason, tc.wantOK, tc.wantAction, tc.wantNextHop)
			}
			if !tc.wantOK {
				if got.Route != nil {
					t.Fatalf("expected nil Route on STOP_AND_ASK_USER, got %+v", got.Route)
				}
				return
			}
			if got.Route == nil {
				t.Fatal("expected non-nil Route")
			}
			if got.Route.Carrier != tc.wantCarrier || got.Route.EffectiveTier != tc.wantTier || got.Route.EffectiveEffort != tc.wantEffort {
				t.Fatalf("route = (%s %s:%s), want (%s %s:%s)",
					got.Route.Carrier, got.Route.EffectiveTier, got.Route.EffectiveEffort,
					tc.wantCarrier, tc.wantTier, tc.wantEffort)
			}
		})
	}
}

func TestClassifyOutputQuotaAvoidsFalsePositivesAndSeparatesKinds(t *testing.T) {
	cases := []struct {
		text     string
		wantKind string
	}{
		// Must NOT false-trigger.
		{"compile error at line 429 in main.go", ""},
		{"listening on port 4290", ""},
		{"func test_quota(t *testing.T) {}", ""},
		{"rate_limit_config = 100", ""},
		{"all tests passed (429 assertions)", ""},
		// Transient rate limits.
		{"Error: HTTP 429 from upstream", "rate_limit"},
		{"HTTP/1.1 429 Too Many Requests", "rate_limit"},
		{"status=429 retrying", "rate_limit"},
		{"error: 429", "rate_limit"},
		{"Too Many Requests - slow down", "rate_limit"},
		{"API error: rate limit exceeded", "rate_limit"},
		{"rate_limit_hit on endpoint", "rate_limit"},
		// Hard quota exhaustion.
		{"Error: quota exceeded for this account", "quota"},
		{"You have reached your weekly limit", "quota"},
		{"daily limit reached", "quota"},
		{"usage limit exceeded", "quota"},
		{"Your credit balance is too low to access the API", "quota"},
		{"利用上限に達しました", "quota"},
		{"上限に達しています", "quota"},
		{"クォータ超過のため実行できません", "quota"},
		// Both present -> hard quota wins over transient 429.
		{"HTTP 429: weekly limit exceeded", "quota"},
	}
	for _, tc := range cases {
		gotKind, _ := classifyOutputQuota(tc.text)
		if gotKind != tc.wantKind {
			t.Errorf("classifyOutputQuota(%q) = %q, want %q", tc.text, gotKind, tc.wantKind)
		}
	}
}

func TestModelsCheckOutputCooldownPreservesResetAtAndDryRun(t *testing.T) {
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	_, dpHome := setupModelRouterTestEnv(t, now)

	// Initialize catalog.
	code, _, _, err := runModelsCLI(t, "", "snapshot")
	if err != nil || code != 0 {
		t.Fatalf("snapshot failed: code=%d err=%v", code, err)
	}

	// 1. Dry-run must NOT mutate policy file.
	code, out, _, err := runModelsCLI(t, "HTTP 429 Too Many Requests", "check-output", "agy", "--dry-run")
	if err != nil || code != 0 {
		t.Fatalf("check-output --dry-run failed: code=%d err=%v", code, err)
	}
	var dryResp struct {
		Tripped bool   `json:"tripped"`
		DryRun  bool   `json:"dry_run"`
		Kind    string `json:"kind"`
		ResetAt string `json:"reset_at"`
	}
	if err := json.Unmarshal([]byte(out), &dryResp); err != nil {
		t.Fatalf("unmarshal dry-run: %v\n%s", err, out)
	}
	if !dryResp.Tripped || !dryResp.DryRun || dryResp.Kind != "rate_limit" || dryResp.ResetAt != "2026-11-28T10:15:00Z" {
		t.Fatalf("unexpected dry-run response: %+v", dryResp)
	}
	pol, _, _, err := loadPolicyIn(dpHome, now)
	if err != nil {
		t.Fatal(err)
	}
	if pol.Carriers["agy"].Status != "ok" {
		t.Fatalf("dry-run mutated policy status to %q", pol.Carriers["agy"].Status)
	}

	// 2. Real transient 429 sets reset_at = now + 30m.
	code, out, _, err = runModelsCLI(t, "status=429", "check-output", "agy", "--cooldown", "30m")
	if err != nil || code != 0 {
		t.Fatalf("check-output 429 failed: code=%d err=%v", code, err)
	}
	pol, _, _, err = loadPolicyIn(dpHome, now)
	if err != nil {
		t.Fatal(err)
	}
	if pol.Carriers["agy"].Status != "exhausted" || pol.Carriers["agy"].ResetAt != "2026-11-28T10:30:00Z" {
		t.Fatalf("expected agy exhausted with reset_at 10:30Z, got %+v", pol.Carriers["agy"])
	}

	// 3. Re-running hard quota check-output MUST NOT wipe existing valid reset_at!
	code, out, _, err = runModelsCLI(t, "weekly limit reached", "check-output", "agy")
	if err != nil || code != 0 {
		t.Fatalf("check-output hard quota failed: code=%d err=%v", code, err)
	}
	pol, _, _, err = loadPolicyIn(dpHome, now)
	if err != nil {
		t.Fatal(err)
	}
	if pol.Carriers["agy"].ResetAt != "2026-11-28T10:30:00Z" {
		t.Fatalf("existing reset_at was wiped or altered: got %q, want 2026-11-28T10:30:00Z", pol.Carriers["agy"].ResetAt)
	}

	// 4. Exhaust remaining carriers -> check-output returns exit 2 when fallback is exhausted.
	_, _, _, _ = runModelsCLI(t, "", "budget", "codex", "exhausted")
	code, _, _, err = runModelsCLI(t, "credit balance is too low", "check-output", "claude")
	if err != nil || code != 2 {
		t.Fatalf("expected exit 2 when all carriers exhausted after trip, got code=%d err=%v", code, err)
	}
}

func TestModelsCLISubcommandsEndToEndAndValidation(t *testing.T) {
	now := time.Date(2026, 11, 28, 2, 0, 0, 0, time.UTC)
	setupModelRouterTestEnv(t, now)
	altHome := filepath.Join(t.TempDir(), "alt-home")

	// Help / no args -> exit 0.
	code, out, _, err := runModelsCLI(t, "")
	if err != nil || code != 0 || !strings.Contains(out, "deckpilot models") {
		t.Fatalf("help output failed: code=%d err=%v out=%q", code, err, out)
	}

	// Snapshot with --home at end.
	code, out, _, err = runModelsCLI(t, "", "snapshot", "--home", altHome)
	if err != nil || code != 0 {
		t.Fatalf("snapshot failed: code=%d err=%v", code, err)
	}
	var snapResp struct {
		Snapshot SnapshotResult `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(out), &snapResp); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if !snapResp.Snapshot.Changed || snapResp.Snapshot.SnapshotFile == "" {
		t.Fatalf("first snapshot should record change and file: %+v", snapResp.Snapshot)
	}
	firstSnapName := filepath.Base(snapResp.Snapshot.SnapshotFile)

	// Second snapshot without diff should NOT save a new snapshot.
	deckpilotNow = func() time.Time { return now.Add(time.Minute) }
	code, out, _, err = runModelsCLI(t, "", "--home", altHome, "snapshot")
	if err != nil || code != 0 {
		t.Fatalf("second snapshot failed: code=%d err=%v", code, err)
	}
	snapResp = struct {
		Snapshot SnapshotResult `json:"snapshot"`
	}{}
	if err := json.Unmarshal([]byte(out), &snapResp); err != nil {
		t.Fatal(err)
	}
	if snapResp.Snapshot.Changed || snapResp.Snapshot.SnapshotFile != "" {
		t.Fatalf("unchanged snapshot should not write snapshot file: %+v", snapResp.Snapshot)
	}

	// Force snapshot writes a second file.
	deckpilotNow = func() time.Time { return now.Add(2 * time.Minute) }
	code, out, _, err = runModelsCLI(t, "", "--home", altHome, "snapshot", "--force")
	if err != nil || code != 0 {
		t.Fatalf("force snapshot failed: code=%d err=%v", code, err)
	}
	// History lists both snapshots.
	code, out, _, err = runModelsCLI(t, "", "--home", altHome, "history")
	if err != nil || code != 0 {
		t.Fatalf("history failed: code=%d err=%v", code, err)
	}
	var histResp struct {
		Snapshots []SnapshotHistoryItem `json:"snapshots"`
	}
	if err := json.Unmarshal([]byte(out), &histResp); err != nil {
		t.Fatal(err)
	}
	if len(histResp.Snapshots) != 2 {
		t.Fatalf("expected 2 snapshots in history, got %d", len(histResp.Snapshots))
	}

	// Rollback blocks path traversal attempts.
	for _, bad := range []string{"../current.json", `..\current.json`, "sub/snap.json", "..", "."} {
		code, _, _, err := runModelsCLI(t, "", "--home", altHome, "rollback", bad)
		if code != 1 || err == nil {
			t.Fatalf("rollback(%q) should fail with exit 1, got code=%d err=%v", bad, code, err)
		}
	}
	// Valid rollback succeeds.
	code, _, _, err = runModelsCLI(t, "", "--home", altHome, "rollback", firstSnapName)
	if err != nil || code != 0 {
		t.Fatalf("valid rollback failed: code=%d err=%v", code, err)
	}

	// Order rejects unknown carrier and deduplicates valid carriers.
	code, _, _, err = runModelsCLI(t, "", "--home", altHome, "order", "codex", "gemini")
	if code != 1 || err == nil {
		t.Fatalf("order with unknown carrier must error, got code=%d err=%v", code, err)
	}
	code, out, _, err = runModelsCLI(t, "", "--home", altHome, "set-order", "codex", "agy", "codex", "claude")
	if err != nil || code != 0 {
		t.Fatalf("set-order failed: code=%d err=%v", code, err)
	}
	var orderResp struct {
		CarrierOrder []string `json:"carrier_order"`
	}
	if err := json.Unmarshal([]byte(out), &orderResp); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(orderResp.CarrierOrder, []string{"codex", "agy", "claude"}) {
		t.Fatalf("unexpected deduped order: %v", orderResp.CarrierOrder)
	}

	// Budget validates RFC3339 strictly and clears note/reset_at on ok.
	code, _, _, err = runModelsCLI(t, "", "--home", altHome, "budget", "agy", "exhausted", "--reset-at", "not-a-timestamp")
	if code != 1 || err == nil {
		t.Fatalf("invalid --reset-at must fail, got code=%d err=%v", code, err)
	}
	code, _, _, err = runModelsCLI(t, "", "--home", altHome, "set-budget", "agy", "exhausted", "--reset-at", "2026-12-01T00:00:00Z", "--note", "weekly cap")
	if err != nil || code != 0 {
		t.Fatalf("set-budget exhausted failed: code=%d err=%v", code, err)
	}
	code, out, _, err = runModelsCLI(t, "", "--home", altHome, "budget", "agy", "ok")
	if err != nil || code != 0 {
		t.Fatalf("budget ok failed: code=%d err=%v", code, err)
	}
	var budResp struct {
		Budget CarrierBudget `json:"budget"`
	}
	if err := json.Unmarshal([]byte(out), &budResp); err != nil {
		t.Fatal(err)
	}
	if budResp.Budget.Status != "ok" || budResp.Budget.ResetAt != "" || budResp.Budget.Note != "" {
		t.Fatalf("budget ok must clear reset_at and old note when --note omitted, got %+v", budResp.Budget)
	}

	// Pin validates model existence unless --force, and auto clears.
	code, _, _, err = runModelsCLI(t, "", "--home", altHome, "pin", "agy", "standard", "nonexistent-model")
	if code != 1 || err == nil {
		t.Fatalf("pin unknown model without --force must fail, got code=%d err=%v", code, err)
	}
	code, _, _, err = runModelsCLI(t, "", "--home", altHome, "pin", "agy", "standard", "nonexistent-model", "--force")
	if err != nil || code != 0 {
		t.Fatalf("pin with --force failed: code=%d err=%v", code, err)
	}
	code, out, _, err = runModelsCLI(t, "", "--home", altHome, "pin", "agy", "standard", "auto")
	if err != nil || code != 0 {
		t.Fatalf("pin auto failed: code=%d err=%v", code, err)
	}
	var pinResp struct {
		TierPins map[string]string `json:"tier_pins"`
	}
	if err := json.Unmarshal([]byte(out), &pinResp); err != nil {
		t.Fatal(err)
	}
	if len(pinResp.TierPins) != 0 {
		t.Fatalf("pin auto should clear pin, got %v", pinResp.TierPins)
	}

	// Status human table & JSON output + auto-recovery sync.
	code, _, _, err = runModelsCLI(t, "", "--home", altHome, "budget", "codex", "exhausted", "--reset-at", "2026-11-28T03:00:00Z")
	if err != nil || code != 0 {
		t.Fatal(err)
	}
	// Advance clock past reset_at -> status must auto-recover codex and re-sync agents.json!
	deckpilotNow = func() time.Time { return time.Date(2026, 11, 28, 3, 5, 0, 0, time.UTC) }
	code, tableOut, _, err := runModelsCLI(t, "", "--home", altHome, "status")
	if err != nil || code != 0 {
		t.Fatalf("status table failed: code=%d err=%v", code, err)
	}
	if !strings.Contains(tableOut, "Presets:") || !strings.Contains(tableOut, "Carriers:") {
		t.Fatalf("unexpected status table output:\n%s", tableOut)
	}
	code, jsonOut, _, err := runModelsCLI(t, "", "--home", altHome, "status", "--json")
	if err != nil || code != 0 {
		t.Fatalf("status --json failed: code=%d err=%v", code, err)
	}
	var st statusPayload
	if err := json.Unmarshal([]byte(jsonOut), &st); err != nil {
		t.Fatalf("unmarshal status --json: %v\n%s", err, jsonOut)
	}
	if st.Carriers["codex"].Budget.Status != "ok" {
		t.Fatalf("expected codex to auto-recover to ok, got %+v", st.Carriers["codex"].Budget)
	}
	// Verify agents.json was synced to codex after auto-recovery!
	rawAgents, err := os.ReadFile(agentProfilesPathIn(altHome))
	if err != nil {
		t.Fatal(err)
	}
	var diskAgents map[string]agentProfileEntry
	if err := json.Unmarshal(rawAgents, &diskAgents); err != nil {
		t.Fatal(err)
	}
	if diskAgents["auto-l2"].Cmd != "codex" {
		t.Fatalf("expected agents.json auto-l2 to resync to codex after auto-recovery, got %+v", diskAgents["auto-l2"])
	}

	// Escalate CLI unified JSON + quota preserves existing reset_at + STOP_AND_ASK_USER returns exit 2.
	code, _, _, err = runModelsCLI(t, "", "--home", altHome, "budget", "codex", "low", "--reset-at", "2026-12-05T00:00:00Z")
	if err != nil || code != 0 {
		t.Fatal(err)
	}
	code, escOut, _, err := runModelsCLI(t, "", "--home", altHome, "escalate", "--carrier", "codex", "--gear", "L2", "--kind", "quota")
	if err != nil || code != 0 {
		t.Fatalf("escalate quota failed: code=%d err=%v", code, err)
	}
	var escResp EscalationDecision
	if err := json.Unmarshal([]byte(escOut), &escResp); err != nil {
		t.Fatalf("unmarshal escalate: %v\n%s", err, escOut)
	}
	if !escResp.OK || escResp.Action != "FAILOVER_CARRIER" || escResp.Route == nil || escResp.Route.Carrier != "agy" {
		t.Fatalf("unexpected escalate quota response: %+v", escResp)
	}
	polAfterEsc, _, _, err := loadPolicyIn(altHome, deckpilotNow())
	if err != nil {
		t.Fatal(err)
	}
	if polAfterEsc.Carriers["codex"].ResetAt != "2026-12-05T00:00:00Z" {
		t.Fatalf("escalate --kind quota must preserve existing reset_at, got %q", polAfterEsc.Carriers["codex"].ResetAt)
	}

	code, escBlockedOut, _, err := runModelsCLI(t, "", "--home", altHome, "escalate", "--carrier", "agy", "--kind", "blocked")
	if err != nil || code != 2 {
		t.Fatalf("escalate blocked must exit 2, got code=%d err=%v", code, err)
	}
	var blockedResp EscalationDecision
	if err := json.Unmarshal([]byte(escBlockedOut), &blockedResp); err != nil {
		t.Fatal(err)
	}
	if blockedResp.OK || blockedResp.Action != "STOP_AND_ASK_USER" {
		t.Fatalf("unexpected blocked response: %+v", blockedResp)
	}
}

func TestSyncPreservesUserEntriesAndResolveAgentDynamicAndManagedFlag(t *testing.T) {
	now := time.Date(2026, 11, 28, 4, 0, 0, 0, time.UTC)
	setupModelRouterTestEnv(t, now)

	// Save a manual/learned user profile first.
	manualDef := AgentDef{Cmd: "mytool", Args: []string{"--flag"}, ReadyStr: "MY>"}
	if err := saveAgentProfile("mytool", manualDef, "MY>"); err != nil {
		t.Fatalf("saveAgentProfile: %v", err)
	}

	// Run snapshot (which syncs agents.json).
	code, _, _, err := runModelsCLI(t, "", "snapshot")
	if err != nil || code != 0 {
		t.Fatalf("snapshot failed: code=%d err=%v", code, err)
	}

	// Load agents.json via loadAgentProfiles and verify Managed flag separation.
	profiles, err := loadAgentProfiles()
	if err != nil {
		t.Fatalf("loadAgentProfiles: %v", err)
	}
	mytool, ok := profiles["mytool"]
	if !ok {
		t.Fatal("sync deleted user-defined profile 'mytool'")
	}
	if mytool.Managed || !mytool.Learned {
		t.Fatalf("user profile must have Learned=true Managed=false, got %+v", mytool)
	}
	autoL2, ok := profiles["auto-l2"]
	if !ok || !autoL2.Managed || !autoL2.Learned {
		t.Fatalf("managed profile auto-l2 must have Learned=true Managed=true, got %+v", autoL2)
	}

	// Dynamic resolution handles extended efforts (xhigh/ultra) and auto-light-high directly!
	def, err := resolveAgent("auto-light-high")
	if err != nil {
		t.Fatalf("resolveAgent(auto-light-high): %v", err)
	}
	if !def.Managed || !def.Learned || def.Cmd != "agy" || !hasArg(def.Args, "gemini-4-hydrogen-high") {
		t.Fatalf("unexpected dynamic auto-light-high def: %+v", def)
	}

	defUltra, err := resolveAgent("codex-frontier-ultra")
	if err != nil {
		t.Fatalf("resolveAgent(codex-frontier-ultra): %v", err)
	}
	if !defUltra.Managed || defUltra.Cmd != "codex" || !hasArg(defUltra.Args, "model_reasoning_effort=ultra") {
		t.Fatalf("unexpected dynamic codex-frontier-ultra def: %+v", defUltra)
	}

	// Exhaust all carriers -> resolveAgent("auto-l2") MUST return explicit error, NOT genericAgent!
	for _, c := range defaultCarrierOrder {
		code, _, _, err := runModelsCLI(t, "", "budget", c, "exhausted")
		if err != nil || code != 0 {
			t.Fatalf("budget %s exhausted failed: code=%d err=%v", c, code, err)
		}
	}
	gotErrDef, err := resolveAgent("auto-l2")
	if err == nil {
		t.Fatalf("expected explicit error when all carriers exhausted, got %+v", gotErrDef)
	}

	// Auto-recovery via reset_at works dynamically inside resolveAgent!
	code, _, _, err = runModelsCLI(t, "", "budget", "codex", "exhausted", "--reset-at", "2026-11-28T05:00:00Z")
	if err != nil || code != 0 {
		t.Fatal(err)
	}
	deckpilotNow = func() time.Time { return time.Date(2026, 11, 28, 5, 1, 0, 0, time.UTC) }
	recoveredDef, err := resolveAgent("auto-l2")
	if err != nil {
		t.Fatalf("expected resolveAgent to auto-recover codex, got err=%v", err)
	}
	if recoveredDef.Cmd != "codex" || !recoveredDef.Managed {
		t.Fatalf("unexpected recovered def: %+v", recoveredDef)
	}
}

func TestResolveAgentFallsBackToAgentsJSONOnlyWhenCatalogMissing(t *testing.T) {
	now := time.Date(2026, 11, 28, 4, 0, 0, 0, time.UTC)
	_, dpHome := setupModelRouterTestEnv(t, now)

	// No current.json exists yet; seed an agents.json entry for auto-l2.
	entries := map[string]agentProfileEntry{
		"auto-l2": {
			Cmd:         "codex",
			Args:        []string{"--dangerously-bypass-approvals-and-sandbox", "-m", "gpt-6.2-sol"},
			ReadyStr:    "›",
			SubmitKey:   "\r",
			LearnedFrom: "deckpilot-model-router: fallback test",
		},
	}
	if err := atomicWriteJSON(agentProfilesPathIn(dpHome), entries); err != nil {
		t.Fatal(err)
	}

	def, err := resolveAgent("auto-l2")
	if err != nil {
		t.Fatalf("expected fallback to agents.json when catalog missing: %v", err)
	}
	if def.Cmd != "codex" || !def.Managed {
		t.Fatalf("unexpected fallback def: %+v", def)
	}

	// Missing both catalog and agents.json entry -> explicit error (never genericAgent).
	if _, err := resolveAgent("auto-l3"); err == nil {
		t.Fatal("expected error for auto-l3 when both catalog and agents.json entry are missing")
	}
}

func TestModelsCheckOutputFileAndBudgetClearResetAtAndRouteExit2(t *testing.T) {
	now := time.Date(2026, 11, 28, 6, 0, 0, 0, time.UTC)
	_, dpHome := setupModelRouterTestEnv(t, now)

	code, _, _, err := runModelsCLI(t, "", "snapshot")
	if err != nil || code != 0 {
		t.Fatalf("snapshot failed: code=%d err=%v", code, err)
	}

	// check-output reading from --file with custom --tier/--effort.
	logPath := filepath.Join(t.TempDir(), "session.log")
	if err := os.WriteFile(logPath, []byte("something\nError: HTTP/1.1 429 Too Many Requests\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _, err := runModelsCLI(t, "", "check-output", "agy", "--file", logPath, "--tier", "frontier", "--effort", "high")
	if err != nil || code != 0 {
		t.Fatalf("check-output --file failed: code=%d err=%v", code, err)
	}
	var resp struct {
		Tripped  bool        `json:"tripped"`
		Kind     string      `json:"kind"`
		Fallback RouteResult `json:"fallback"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal check-output: %v", err)
	}
	if !resp.Tripped || resp.Kind != "rate_limit" || !resp.Fallback.OK || resp.Fallback.Carrier != "codex" || resp.Fallback.EffectiveTier != "frontier" || resp.Fallback.EffectiveEffort != "high" {
		t.Fatalf("unexpected check-output --file response: %+v", resp)
	}

	// budget --clear-reset-at clears reset_at without changing status to ok.
	code, _, _, err = runModelsCLI(t, "", "budget", "agy", "exhausted", "--clear-reset-at")
	if err != nil || code != 0 {
		t.Fatalf("budget --clear-reset-at failed: code=%d err=%v", code, err)
	}
	pol, _, _, err := loadPolicyIn(dpHome, now)
	if err != nil {
		t.Fatal(err)
	}
	if pol.Carriers["agy"].Status != "exhausted" || pol.Carriers["agy"].ResetAt != "" {
		t.Fatalf("expected agy exhausted with cleared reset_at, got %+v", pol.Carriers["agy"])
	}

	// sync subcommand works standalone.
	code, syncOut, _, err := runModelsCLI(t, "", "sync")
	if err != nil || code != 0 || !strings.Contains(syncOut, "agents_file") {
		t.Fatalf("sync failed: code=%d err=%v out=%q", code, err, syncOut)
	}

	// Exhaust all carriers -> `models route` returns ok:false and exit 2.
	for _, c := range defaultCarrierOrder {
		_, _, _, _ = runModelsCLI(t, "", "budget", c, "exhausted")
	}
	code, routeOut, _, err := runModelsCLI(t, "", "route", "--gear", "L2")
	if err != nil || code != 2 {
		t.Fatalf("route when all exhausted must exit 2, got code=%d err=%v", code, err)
	}
	var rr RouteResult
	if err := json.Unmarshal([]byte(routeOut), &rr); err != nil {
		t.Fatal(err)
	}
	if rr.OK || rr.Action != "STOP_AND_ASK_USER" {
		t.Fatalf("unexpected exhausted route result: %+v", rr)
	}
}

func TestLegacyV1CatalogCompatibilityInHistoryAndUpgrade(t *testing.T) {
	now := time.Date(2026, 11, 28, 7, 0, 0, 0, time.UTC)
	_, dpHome := setupModelRouterTestEnv(t, now)

	legacyV1 := `{
  "schema_version": 1,
  "captured_at": "2026-11-27T23:24:18Z",
  "carriers": {
    "claude": {
      "available": true,
      "models": [
        {"id": "sonnet", "display_name": "Claude Sonnet", "efforts": ["low", "medium", "high"]}
      ]
    }
  },
  "fingerprint": "fea30437906e472b"
}`
	sdir := modelSnapshotsDirIn(dpHome)
	if err := os.MkdirAll(sdir, 0o700); err != nil {
		t.Fatal(err)
	}
	v1Name := "2026-11-27_232418_fea30437906e472b.json"
	if err := os.WriteFile(filepath.Join(sdir, v1Name), []byte(legacyV1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelCurrentCatalogPathIn(dpHome), []byte(legacyV1), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. history must parse legacy v1 snapshot headers cleanly.
	code, histOut, _, err := runModelsCLI(t, "", "history")
	if err != nil || code != 0 {
		t.Fatalf("history failed on v1 snapshot: code=%d err=%v", code, err)
	}
	var histResp struct {
		Snapshots []SnapshotHistoryItem `json:"snapshots"`
	}
	if err := json.Unmarshal([]byte(histOut), &histResp); err != nil {
		t.Fatal(err)
	}
	if len(histResp.Snapshots) != 1 || histResp.Snapshots[0].SchemaVersion != 1 || histResp.Snapshots[0].Fingerprint != "fea30437906e472b" || histResp.Snapshots[0].CapturedAt != "2026-11-27T23:24:18Z" {
		t.Fatalf("unexpected v1 history entry: %+v", histResp.Snapshots)
	}

	// 2. rollback to legacy v1 snapshot must fail clearly rather than writing broken schema.
	code, _, _, err = runModelsCLI(t, "", "rollback", v1Name)
	if code != 1 || err == nil || !errors.Is(err, errLegacyCatalogSchema) {
		t.Fatalf("expected rollback to v1 snapshot to fail with errLegacyCatalogSchema, got code=%d err=%v", code, err)
	}

	// 3. snapshot must smoothly upgrade legacy v1 current.json to v2.
	code, snapOut, _, err := runModelsCLI(t, "", "snapshot")
	if err != nil || code != 0 {
		t.Fatalf("snapshot upgrade from v1 failed: code=%d err=%v", code, err)
	}
	var snapResp struct {
		Snapshot SnapshotResult `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(snapOut), &snapResp); err != nil {
		t.Fatal(err)
	}
	if !snapResp.Snapshot.Changed || snapResp.Snapshot.PreviousFingerprint != "fea30437906e472b" {
		t.Fatalf("unexpected v1->v2 upgrade snapshot result: %+v", snapResp.Snapshot)
	}
}
