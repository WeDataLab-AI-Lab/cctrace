package desktop

import (
	"context"
	"time"
)

func (s *Service) restartSupervisor(ctx context.Context, current daemonStatusResult, profileID string, whole bool) (any, *ErrorObject) {
	if !current.Complete || current.Daemon == nil || current.Daemon.State != "running" && current.Daemon.State != "stopped" {
		return nil, &ErrorObject{Code: "daemon_state_unknown", Message: "local daemon state could not be verified"}
	}
	var beforeGeneration int
	if !whole {
		profile, err := s.controlProfileStatus(profileID)
		if err != nil {
			return nil, err
		}
		if profile.Job == nil {
			return nil, &ErrorObject{Code: "daemon_state_unknown", Message: "profile job state is unknown"}
		}
		if profile.Job.ErrorCode != nil && (*profile.Job.ErrorCode == "duplicate_route" || *profile.Job.ErrorCode == "migration_conflict") {
			return nil, &ErrorObject{Code: "daemon_profile_unavailable", Message: "profile route requires repair before restart"}
		}
		if profile.Job.NextRetryAt != nil && profile.Job.ErrorCode != nil && *profile.Job.ErrorCode == "rate_limited" {
			if retry, err := time.Parse(time.RFC3339Nano, *profile.Job.NextRetryAt); err == nil && retry.After(s.now()) {
				return nil, &ErrorObject{Code: "daemon_server_rate_limited", Message: "server rate limit is active; wait before restarting"}
			}
		}
		beforeGeneration = profile.Job.Generation
	}
	args := []string{"sync", "restart"}
	if !whole {
		args = append(args, "--profile-id", profileID)
	}
	if err := s.runDaemonControlCommand(ctx, args); err != nil {
		return nil, daemonControlCommandFailure(ctx, "daemon_start_failed", "local daemon restart was not acknowledged")
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		confirmed := s.readLiveDaemonStatus()
		if confirmed.Complete && confirmed.Daemon != nil && confirmed.Daemon.State == "running" && confirmed.Daemon.Evidence == "lock_held" {
			if whole && (current.Daemon.State == "stopped" || confirmed.Daemon.StartedAt != nil && current.Daemon.StartedAt != nil && *confirmed.Daemon.StartedAt != *current.Daemon.StartedAt) {
				return daemonRestartResult{State: "restarted"}, nil
			}
			for _, profile := range confirmed.Profiles {
				if !whole && profile.ID == profileID && profile.Job != nil && profile.Job.Generation > beforeGeneration {
					return daemonRestartResult{ProfileID: profileID, State: "restarted"}, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, daemonControlCommandFailure(ctx, "daemon_start_unconfirmed", "restart could not be confirmed")
		case <-ticker.C:
		}
	}
}
