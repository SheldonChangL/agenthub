package status

import (
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

func TestInferManagedUsesFreshReportedStatus(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)

	got, source := Infer(Evidence{
		Management:     model.Managed,
		ReportedStatus: model.StatusIdle,
		HeartbeatAt:    now.Add(-5 * time.Second),
	}, Policy{Now: now, HeartbeatTTL: 30 * time.Second})

	if got != model.StatusIdle || source != "managed_heartbeat" {
		t.Fatalf("Infer() = %q, %q; want idle, managed_heartbeat", got, source)
	}
}

func TestInferManagedExpiresWithoutHeartbeat(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)

	got, source := Infer(Evidence{
		Management:     model.Managed,
		ReportedStatus: model.StatusActive,
		HeartbeatAt:    now.Add(-31 * time.Second),
	}, Policy{Now: now, HeartbeatTTL: 30 * time.Second})

	if got != model.StatusInactive || source != "expired_heartbeat" {
		t.Fatalf("Infer() = %q, %q; want inactive, expired_heartbeat", got, source)
	}
}

func TestInferUnmanagedRequiresKnownProcessState(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)

	got, source := Infer(Evidence{
		Management: model.Unmanaged,
		MetadataAt: now,
	}, Policy{Now: now})

	if got != model.StatusUnknown || source != "process_unavailable" {
		t.Fatalf("Infer() = %q, %q; want unknown, process_unavailable", got, source)
	}
}

func TestInferUnmanagedUsesRecencyOnlyWhenProviderRuns(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	policy := Policy{Now: now, ActiveWindow: 2 * time.Minute, IdleWindow: 15 * time.Minute}

	tests := []struct {
		name     string
		modified time.Time
		want     model.LifecycleStatus
	}{
		{name: "active", modified: now.Add(-time.Minute), want: model.StatusActive},
		{name: "idle", modified: now.Add(-10 * time.Minute), want: model.StatusIdle},
		{name: "inactive", modified: now.Add(-time.Hour), want: model.StatusInactive},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, source := Infer(Evidence{
				Management:   model.Unmanaged,
				MetadataAt:   tt.modified,
				ProcessKnown: true,
				ProcessRunning: true,
			}, policy)
			if got != tt.want || source != "metadata_process_heuristic" {
				t.Fatalf("Infer() = %q, %q; want %q, metadata_process_heuristic", got, source, tt.want)
			}
		})
	}
}

