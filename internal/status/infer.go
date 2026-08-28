package status

import (
	"time"

	"agenthub.local/agenthub/internal/model"
)

const (
	defaultActiveWindow = 2 * time.Minute
	defaultIdleWindow   = 15 * time.Minute
	defaultHeartbeatTTL = 30 * time.Second
)

type Evidence struct {
	Management     model.Management
	ReportedStatus model.LifecycleStatus
	HeartbeatAt    time.Time
	MetadataAt     time.Time
	ProcessKnown   bool
	ProcessRunning bool
}

type Policy struct {
	Now          time.Time
	ActiveWindow time.Duration
	IdleWindow   time.Duration
	HeartbeatTTL time.Duration
}

func Infer(evidence Evidence, policy Policy) (model.LifecycleStatus, string) {
	policy = withDefaults(policy)
	if evidence.Management == model.Managed {
		if evidence.HeartbeatAt.IsZero() || policy.Now.Sub(evidence.HeartbeatAt) > policy.HeartbeatTTL {
			return model.StatusInactive, "expired_heartbeat"
		}
		if evidence.ReportedStatus == model.StatusActive || evidence.ReportedStatus == model.StatusIdle {
			return evidence.ReportedStatus, "managed_heartbeat"
		}
		return model.StatusUnknown, "invalid_managed_report"
	}

	if !evidence.ProcessKnown {
		return model.StatusUnknown, "process_unavailable"
	}
	if !evidence.ProcessRunning || evidence.MetadataAt.IsZero() {
		return model.StatusInactive, "metadata_process_heuristic"
	}

	age := policy.Now.Sub(evidence.MetadataAt)
	if age < 0 {
		age = 0
	}
	if age <= policy.ActiveWindow {
		return model.StatusActive, "metadata_process_heuristic"
	}
	if age <= policy.IdleWindow {
		return model.StatusIdle, "metadata_process_heuristic"
	}
	return model.StatusInactive, "metadata_process_heuristic"
}

func withDefaults(policy Policy) Policy {
	if policy.Now.IsZero() {
		policy.Now = time.Now().UTC()
	}
	if policy.ActiveWindow <= 0 {
		policy.ActiveWindow = defaultActiveWindow
	}
	if policy.IdleWindow <= 0 {
		policy.IdleWindow = defaultIdleWindow
	}
	if policy.HeartbeatTTL <= 0 {
		policy.HeartbeatTTL = defaultHeartbeatTTL
	}
	return policy
}
