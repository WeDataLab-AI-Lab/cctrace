package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

func (s *Service) readSupervisorStatus() (daemonStatusResult, bool) {
	_, runtimeErr := os.Lstat(filepath.Join(s.daemonRoot, "daemon-runtime.json"))
	_, statusErr := os.Lstat(filepath.Join(s.daemonRoot, "daemon-status.json"))
	if os.IsNotExist(runtimeErr) && os.IsNotExist(statusErr) {
		return daemonStatusResult{}, false
	}
	now := s.now().UTC()
	result := daemonStatusResult{ObservedAt: now.Format(time.RFC3339Nano), Complete: true, Profiles: []daemonProfileStatus{}}
	budget := daemonReadBudget{remaining: maxDaemonTotalInput, complete: true}
	entries, listErr := (profile.Registry{Root: s.daemonRoot}).List()
	var paths []daemonProfilePath
	for _, entry := range entries {
		paths = append(paths, daemonProfilePath{id: entry.ID, name: entry.Name, dir: entry.RuntimeDir})
	}
	result.Complete = listErr == nil && len(paths) <= maxDaemonProfiles
	if len(paths) > maxDaemonProfiles {
		paths = paths[:maxDaemonProfiles]
	}
	overall := daemonOperationStatus{State: "unknown", Evidence: "insufficient"}
	probe := s.daemonLockProbe
	if probe == nil {
		probe = probeDaemonLock
	}
	data, code := s.readInput(filepath.Join(s.daemonRoot, "daemon-runtime.json"), &budget)
	var rt localdaemon.Runtime
	runtimeValid := code == "" && lifecycleJSONHasUniqueKeys(data) && json.Unmarshal(data, &rt) == nil && rt.InstanceID != "" && rt.PID > 0 && !rt.StartedAt.After(now) && !rt.StartedAt.IsZero()
	if runtimeValid {
		if safeDaemonVersion(rt.Version) {
			overall.Version = &rt.Version
		}
		started := rt.StartedAt.UTC().Format(time.RFC3339Nano)
		overall.StartedAt = &started
	}
	switch probe(filepath.Join(s.daemonRoot, "daemon.lock")) {
	case daemonLockHeld:
		if runtimeValid {
			overall.State = "running"
			overall.Evidence = "lock_held"
		}
	case daemonLockFree, daemonLockMissing:
		overall.State = "stopped"
		overall.Evidence = "lock_free"
	}
	result.Daemon = &overall
	data, code = s.readInput(filepath.Join(s.daemonRoot, "daemon-status.json"), &budget)
	var snapshot localdaemon.Snapshot
	fresh := code == "" && lifecycleJSONHasUniqueKeys(data) && json.Unmarshal(data, &snapshot) == nil && (runtimeValid && snapshot.InstanceID == rt.InstanceID && snapshot.PID == rt.PID || overall.State == "stopped" && os.IsNotExist(runtimeErr)) && snapshot.ObservedAt.Before(now.Add(time.Nanosecond)) && now.Sub(snapshot.ObservedAt) <= daemonControlFreshness && len(snapshot.Jobs) <= maxDaemonProfiles && len(snapshot.Jobs) == len(paths)
	if !fresh || !snapshot.Complete {
		result.Complete = false
	}
	jobs := map[string]localdaemon.JobStatus{}
	for _, job := range snapshot.Jobs {
		if _, duplicate := jobs[job.ID]; duplicate {
			fresh = false
			result.Complete = false
		}
		jobs[job.ID] = job
	}
	for _, path := range paths {
		// Singleton ownership and collection health come from the verified snapshot.
		// Only profile configuration is required; legacy cursor data is optional.
		status := daemonProfileStatus{ID: path.id, Name: path.name}
		if !s.daemonProfileDirectorySafe(path) {
			status.setError("unsafe_path")
		} else {
			enabled, configCode := s.readDaemonConfig(path.dir, &budget)
			status.SyncEnabled = enabled
			if configCode != "" {
				status.setError(configCode)
			}
		}
		status.Daemon = overall
		job, ok := jobs[path.id]
		status.Collection.State = "unknown"
		if !fresh || !ok || job.Name != path.name {
			result.Complete = false
		} else if !validSupervisorJob(job, now) {
			result.Complete = false
		} else {
			status.SyncEnabled = &job.Enabled
			status.Job = &daemonJobStatus{State: job.State, Generation: job.Generation, Sent: job.Sent, LastPassAt: statusTime(job.LastPassAt), NextRetryAt: statusTime(job.NextRetryAt), SharedSources: sharedSourcesFromJob(job.SharedSources)}
			if job.ErrorCode != "" {
				status.Job.ErrorCode = &job.ErrorCode
			}
			switch {
			case overall.State != "running":
				status.Collection.State = "stopped"
			case !job.Enabled:
				status.Collection.State = "disabled"
			case job.ErrorCode == "duplicate_route" || job.State == "error":
				status.Collection.State = "error"
			case job.State == "healthy":
				status.Collection.State = "healthy"
			case job.State == "collecting":
				status.Collection.State = "collecting"
			case job.State == "stopped" || job.State == "stopping":
				status.Collection.State = "stopped"
			case job.State == "blocked":
				status.Collection.State = "blocked"
			case job.State == "retrying":
				status.Collection.State = "failing"
			}
			if job.BlockedFiles > 0 {
				blocked := job.BlockedFiles
				status.Collection.Counters.BodyLimitBlockedFiles = &blocked
				if job.ErrorCode != "duplicate_route" {
					status.Collection.State = "blocked"
				}
			}
		}
		if status.ErrorCode != nil {
			result.Complete = false
		}
		result.Profiles = append(result.Profiles, status)
	}
	if !budget.complete {
		result.Complete = false
	}
	// Spend only the remaining bounded budget on optional legacy counters after
	// every required input has been read, so cursors cannot starve the census.
	optionalBudget := daemonReadBudget{remaining: budget.remaining, complete: true}
	for i, path := range paths {
		if result.Profiles[i].ErrorCode != nil {
			continue
		}
		_, legacy := s.readDaemonStates(path.dir, &optionalBudget, now)
		blocked := result.Profiles[i].Collection.Counters.BodyLimitBlockedFiles
		result.Profiles[i].Collection.Counters = legacy.counters
		if legacy.errorCode != "" {
			result.Profiles[i].Collection.Counters.ErrorCode = &legacy.errorCode
		} else {
			result.Profiles[i].Collection.LastStateAdvanceAt = legacy.lastAdvance
		}
		// A verified job's body-limit count takes precedence, including zero.
		if result.Profiles[i].Job != nil {
			count := jobs[path.id].BlockedFiles
			blocked = &count
		}
		result.Profiles[i].Collection.Counters.BodyLimitBlockedFiles = blocked
	}
	return result, true
}

func validSupervisorJob(job localdaemon.JobStatus, now time.Time) bool {
	if job.Generation < 0 || job.Sent < 0 || job.BlockedFiles < 0 || job.LastPassAt.After(now) || job.ErrorCode != "" && !validSafeErrorCode(job.ErrorCode) {
		return false
	}
	switch job.State {
	case "unknown", "healthy", "collecting", "retrying", "stopped", "stopping", "disabled", "blocked", "error":
		return true
	}
	return false
}
func statusTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	value := t.UTC().Format(time.RFC3339Nano)
	return &value
}
