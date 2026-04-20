//go:build windows

package daemon

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// T8 Sample_EmptyPIDs
func TestSample_EmptyPIDs(t *testing.T) {
	sampler := SystemCPUSampler{}
	percent, err := sampler.Sample([]uint32{}, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if percent != 0 {
		t.Errorf("expected 0%%, got %f%%", percent)
	}
}

// T9 Sample_ZeroInterval
func TestSample_ZeroInterval(t *testing.T) {
	sampler := SystemCPUSampler{}
	_, err := sampler.Sample([]uint32{uint32(os.Getpid())}, 0)
	if err == nil || err.Error() != "interval must be > 0" {
		t.Errorf("expected 'interval must be > 0' error, got: %v", err)
	}
}

// T10 Sample_IdleProcess
func TestSample_IdleProcess(t *testing.T) {
	sampler := SystemCPUSampler{}
	// Sleep to ensure we are "idle-ish"
	time.Sleep(50 * time.Millisecond)
	percent, err := sampler.Sample([]uint32{uint32(os.Getpid())}, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// We expect very low CPU for the current process during a 100ms sample if we just sleep.
	// 50% is a very safe upper bound.
	if percent > 50.0 {
		t.Errorf("CPU usage unexpectedly high: %f%%", percent)
	}
}

// T11 Sample_DeadPID
func TestSample_DeadPID(t *testing.T) {
	sampler := SystemCPUSampler{}
	// PID 999999 is highly unlikely to exist.
	percent, err := sampler.Sample([]uint32{999999}, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if percent != 0 {
		t.Errorf("expected 0%% for dead PID, got %f%%", percent)
	}
}

// T12 filetimeToDuration_Unit
func TestFiletimeToDuration_Unit(t *testing.T) {
	ft := windows.Filetime{
		HighDateTime: 0,
		LowDateTime:  10_000_000,
	}
	d := filetimeToDuration(ft)
	if d != 1*time.Second {
		t.Errorf("expected 1s, got %v", d)
	}
}

// T13 filetimeToDuration_HighBitsConsidered
func TestFiletimeToDuration_HighBitsConsidered(t *testing.T) {
	ft := windows.Filetime{
		HighDateTime: 1,
		LowDateTime:  0,
	}
	d := filetimeToDuration(ft)
	// (1 << 32) * 100ns = 4294967296 * 100ns = 429.4967296s
	expected := time.Duration(1<<32) * 100 * time.Nanosecond
	if d != expected {
		t.Errorf("expected %v, got %v", expected, d)
	}
}
