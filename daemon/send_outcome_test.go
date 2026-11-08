package daemon

import "testing"

func TestClassifySendOutcome(t *testing.T) {
	tests := []struct {
		name   string
		status string
		submit SubmitStatus
		want   string
	}{
		{"idle accepted", "idle", SubmitOKCleared, "accepted"},
		{"active queued", "active", SubmitOKCleared, "queued"},
		{"stuck no effect", "idle", SubmitFailedStuck, "no-effect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifySendOutcome(tt.status, tt.submit); got != tt.want {
				t.Fatalf("classifySendOutcome = %q, want %q", got, tt.want)
			}
		})
	}
}
