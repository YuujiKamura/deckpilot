package daemon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type stubLister struct {
	Pids []ProcessInfo
	Err  error
}

func (s *stubLister) List() ([]ProcessInfo, error) {
	return s.Pids, s.Err
}

type stubSampler struct {
	Percent float64
	Err     error
	Calls   int
	GotPids [][]uint32
}

func (s *stubSampler) Sample(pids []uint32, interval time.Duration) (float64, error) {
	s.Calls++
	s.GotPids = append(s.GotPids, pids)
	return s.Percent, s.Err
}

func makeConfig(overrides func(*HangConfig)) HangConfig {
	cfg := HangConfig{
		SessionName:    "test-session",
		RootPID:        1234,
		Lister:         &stubLister{Pids: []ProcessInfo{{PID: 1234, PPID: 0}}},
		Sampler:        &stubSampler{Percent: 0.1},
		LastActOf:      func() time.Time { return time.Now().Add(-100 * time.Second) },
		Action:         func(ctx context.Context, info HangInfo) error { return nil },
		CPUThreshold:   1.0,
		StallSeconds:   60 * time.Second,
		ProbeInterval:  5 * time.Second,
		SampleInterval: 500 * time.Millisecond,
		IncludeChildren: true,
	}
	if overrides != nil {
		overrides(&cfg)
	}
	return cfg
}

// T1 Validate_RejectsEmpty
func TestValidate_RejectsEmpty(t *testing.T) {
	fields := []string{"SessionName", "RootPID", "Lister", "Sampler", "LastActOf", "Action", "StallSeconds", "ProbeInterval", "SampleInterval"}
	for _, field := range fields {
		cfg := makeConfig(func(c *HangConfig) {
			switch field {
			case "SessionName": c.SessionName = ""
			case "RootPID": c.RootPID = 0
			case "Lister": c.Lister = nil
			case "Sampler": c.Sampler = nil
			case "LastActOf": c.LastActOf = nil
			case "Action": c.Action = nil
			case "StallSeconds": c.StallSeconds = 0
			case "ProbeInterval": c.ProbeInterval = 0
			case "SampleInterval": c.SampleInterval = 0
			}
		})
		err := cfg.Validate()
		if err == nil {
			t.Errorf("expected error for missing %s", field)
		} else if !strings.Contains(err.Error(), field) {
			t.Errorf("error message for %s should contain field name: %v", field, err)
		}
	}
}

// T2 Validate_RejectsSampleGTProbe
func TestValidate_RejectsSampleGTProbe(t *testing.T) {
	cfg := makeConfig(func(c *HangConfig) {
		c.SampleInterval = 500 * time.Millisecond
		c.ProbeInterval = 100 * time.Millisecond
	})
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "SampleInterval must be < ProbeInterval") {
		t.Errorf("expected SampleInterval < ProbeInterval error, got: %v", err)
	}
}

// T3 Validate_AcceptsFull
func TestValidate_AcceptsFull(t *testing.T) {
	cfg := makeConfig(nil)
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected valid config, got error: %v", err)
	}
}

// T4 NewHangDetector_DefaultsCooldown
func TestNewHangDetector_DefaultsCooldown(t *testing.T) {
	cfg := makeConfig(func(c *HangConfig) {
		c.Cooldown = 0
		c.StallSeconds = 30 * time.Second
	})
	d, err := NewHangDetector(cfg)
	if err != nil {
		t.Fatalf("NewHangDetector failed: %v", err)
	}
	expected := 2 * cfg.StallSeconds
	if d.cfg.Cooldown != expected {
		t.Errorf("expected default cooldown %v, got %v", expected, d.cfg.Cooldown)
	}
}

// T5 ProbeOnce_NoHangWhenBusy
func TestProbeOnce_NoHangWhenBusy(t *testing.T) {
	calls := 0
	sampler := &stubSampler{Percent: 50.0}
	cfg := makeConfig(func(c *HangConfig) {
		c.Sampler = sampler
		c.CPUThreshold = 1.0
		c.Action = func(ctx context.Context, info HangInfo) error {
			calls++
			return nil
		}
	})
	d, _ := NewHangDetector(cfg)
	if err := d.probeOnce(context.Background()); err != nil {
		t.Fatalf("probeOnce failed: %v", err)
	}
	if calls != 0 {
		t.Error("Action should not fire when CPU is high")
	}
}

// T6 ProbeOnce_NoHangWhenFresh
func TestProbeOnce_NoHangWhenFresh(t *testing.T) {
	now := time.Now()
	calls := 0
	cfg := makeConfig(func(c *HangConfig) {
		c.Now = func() time.Time { return now }
		c.LastActOf = func() time.Time { return now.Add(-5 * time.Second) }
		c.StallSeconds = 60 * time.Second
		c.Action = func(ctx context.Context, info HangInfo) error {
			calls++
			return nil
		}
	})
	d, _ := NewHangDetector(cfg)
	if err := d.probeOnce(context.Background()); err != nil {
		t.Fatalf("probeOnce failed: %v", err)
	}
	if calls != 0 {
		t.Error("Action should not fire when last-act is fresh")
	}
}

// T7 ProbeOnce_HangWhenIdleAndStale
func TestProbeOnce_HangWhenIdleAndStale(t *testing.T) {
	now := time.Now()
	calls := 0
	var gotInfo HangInfo
	cfg := makeConfig(func(c *HangConfig) {
		c.Now = func() time.Time { return now }
		c.LastActOf = func() time.Time { return now.Add(-90 * time.Second) }
		c.StallSeconds = 60 * time.Second
		c.Sampler = &stubSampler{Percent: 0.5}
		c.CPUThreshold = 1.0
		c.Action = func(ctx context.Context, info HangInfo) error {
			calls++
			gotInfo = info
			return nil
		}
	})
	d, _ := NewHangDetector(cfg)
	if err := d.probeOnce(context.Background()); err != nil {
		t.Fatalf("probeOnce failed: %v", err)
	}
	if calls != 1 {
		t.Error("Action should fire when idle and stale")
	}
	if gotInfo.CPUPercent != 0.5 {
		t.Errorf("expected CPU 0.5, got %f", gotInfo.CPUPercent)
	}
	if gotInfo.StallSince < 90*time.Second {
		t.Errorf("expected StallSince >= 90s, got %v", gotInfo.StallSince)
	}
}

// T8 ProbeOnce_CooldownSuppressesRepeat
func TestProbeOnce_CooldownSuppressesRepeat(t *testing.T) {
	now := time.Now()
	calls := 0
	cfg := makeConfig(func(c *HangConfig) {
		c.Now = func() time.Time { return now }
		c.LastActOf = func() time.Time { return now.Add(-90 * time.Second) }
		c.StallSeconds = 60 * time.Second
		c.Cooldown = 300 * time.Second
		c.Action = func(ctx context.Context, info HangInfo) error {
			calls++
			return nil
		}
	})
	d, _ := NewHangDetector(cfg)
	_ = d.probeOnce(context.Background()) // 1st fire
	_ = d.probeOnce(context.Background()) // should be suppressed
	if calls != 1 {
		t.Errorf("Action fired %d times, expected 1 (cooldown should suppress)", calls)
	}
}

// T9 ProbeOnce_CooldownExpiresReTrigger
func TestProbeOnce_CooldownExpiresReTrigger(t *testing.T) {
	startTime := time.Now()
	now := startTime
	calls := 0
	cfg := makeConfig(func(c *HangConfig) {
		c.Now = func() time.Time { return now }
		c.LastActOf = func() time.Time { return startTime.Add(-90 * time.Second) }
		c.StallSeconds = 60 * time.Second
		c.Cooldown = 300 * time.Second
		c.Action = func(ctx context.Context, info HangInfo) error {
			calls++
			return nil
		}
	})
	d, _ := NewHangDetector(cfg)
	_ = d.probeOnce(context.Background()) // 1st fire
	
	// Advance time past cooldown
	now = now.Add(cfg.Cooldown + 1*time.Second)
	_ = d.probeOnce(context.Background()) // 2nd fire
	
	if calls != 2 {
		t.Errorf("Action fired %d times, expected 2", calls)
	}
}

// T10 ProbeOnce_IncludeChildrenGathersPids
func TestProbeOnce_IncludeChildrenGathersPids(t *testing.T) {
	sampler := &stubSampler{Percent: 0.1}
	lister := &stubLister{
		Pids: []ProcessInfo{
			{PID: 1234, PPID: 0},
			{PID: 5678, PPID: 1234},
			{PID: 9012, PPID: 5678},
		},
	}
	cfg := makeConfig(func(c *HangConfig) {
		c.Lister = lister
		c.Sampler = sampler
		c.IncludeChildren = true
	})
	d, _ := NewHangDetector(cfg)
	_ = d.probeOnce(context.Background())
	
	if len(sampler.GotPids) == 0 {
		t.Fatal("sampler was not called")
	}
	pids := sampler.GotPids[0]
	if len(pids) != 3 {
		t.Errorf("expected 3 pids sampled, got %v", pids)
	}
}

// T11 ProbeOnce_SkipChildrenOnlyRootSampled
func TestProbeOnce_SkipChildrenOnlyRootSampled(t *testing.T) {
	sampler := &stubSampler{Percent: 0.1}
	cfg := makeConfig(func(c *HangConfig) {
		c.Sampler = sampler
		c.IncludeChildren = false
	})
	d, _ := NewHangDetector(cfg)
	_ = d.probeOnce(context.Background())
	
	if len(sampler.GotPids) == 0 {
		t.Fatal("sampler was not called")
	}
	pids := sampler.GotPids[0]
	if len(pids) != 1 || pids[0] != cfg.RootPID {
		t.Errorf("expected only root PID %d, got %v", cfg.RootPID, pids)
	}
}

// T12 ProbeOnce_ListerErrorIsTolerated
func TestProbeOnce_ListerErrorIsTolerated(t *testing.T) {
	cfg := makeConfig(func(c *HangConfig) {
		c.Lister = &stubLister{Err: fmt.Errorf("list failed")}
	})
	d, _ := NewHangDetector(cfg)
	err := d.probeOnce(context.Background())
	if err != nil {
		t.Errorf("probeOnce should tolerate lister error, got: %v", err)
	}
}

// T13 ProbeOnce_SamplerErrorIsTolerated
func TestProbeOnce_SamplerErrorIsTolerated(t *testing.T) {
	calls := 0
	cfg := makeConfig(func(c *HangConfig) {
		c.Sampler = &stubSampler{Err: fmt.Errorf("sample failed")}
		c.Action = func(ctx context.Context, info HangInfo) error {
			calls++
			return nil
		}
	})
	d, _ := NewHangDetector(cfg)
	err := d.probeOnce(context.Background())
	if err != nil {
		t.Errorf("probeOnce should tolerate sampler error, got: %v", err)
	}
	if calls != 0 {
		t.Error("Action should not fire when sampler fails")
	}
}

// T14 Run_CancelsOnContext
func TestRun_CancelsOnContext(t *testing.T) {
	cfg := makeConfig(func(c *HangConfig) {
		c.ProbeInterval = 100 * time.Millisecond
		c.SampleInterval = 10 * time.Millisecond
	})
	d, err := NewHangDetector(cfg)
	if err != nil {
		t.Fatalf("NewHangDetector failed: %v", err)
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	
	err = d.Run(ctx)
	if err != context.DeadlineExceeded && err != context.Canceled {
		t.Errorf("expected context error, got: %v", err)
	}
}

// T15 HangInfo_Shape
func TestHangInfo_Shape(t *testing.T) {
	now := time.Now()
	var gotInfo HangInfo
	cfg := makeConfig(func(c *HangConfig) {
		c.SessionName = "my-session"
		c.RootPID = 99
		c.Now = func() time.Time { return now }
		c.LastActOf = func() time.Time { return now.Add(-120 * time.Second) }
		c.StallSeconds = 60 * time.Second
		c.Sampler = &stubSampler{Percent: 0.2}
		c.Action = func(ctx context.Context, info HangInfo) error {
			gotInfo = info
			return nil
		}
		c.IncludeChildren = false
	})
	d, _ := NewHangDetector(cfg)
	_ = d.probeOnce(context.Background())
	
	if gotInfo.SessionName != "my-session" {
		t.Errorf("SessionName mismatch: %q", gotInfo.SessionName)
	}
	if gotInfo.RootPID != 99 {
		t.Errorf("RootPID mismatch: %d", gotInfo.RootPID)
	}
	if gotInfo.ProcessCount != 1 {
		t.Errorf("ProcessCount mismatch: %d", gotInfo.ProcessCount)
	}
	if gotInfo.CPUPercent != 0.2 {
		t.Errorf("CPUPercent mismatch: %f", gotInfo.CPUPercent)
	}
	if gotInfo.StallSince != 120*time.Second {
		t.Errorf("StallSince mismatch: %v", gotInfo.StallSince)
	}
	if !gotInfo.DetectedAt.Equal(now) {
		t.Errorf("DetectedAt mismatch: %v", gotInfo.DetectedAt)
	}
}
