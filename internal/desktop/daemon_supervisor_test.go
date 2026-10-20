package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

func privateDaemonFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDaemonSupervisorRecoveryUsesJobIDAndWholeScope(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(map[bool]string{false: "profile", true: "whole"}[whole], func(t *testing.T) {
			root := privateDaemonFixture(t)
			writeDaemonProfile(t, root, true, "")
			writeEmptyDaemonStates(t, root)
			writeRawDaemonState(t, filepath.Join(root, "daemon-runtime.json"), `{"pid":123,"instance_id":"fixture-owner","started_at":"2026-10-12T11:00:00Z","version":"dev"}`)
			writeRawDaemonState(t, filepath.Join(root, "daemon-status.json"), `{"pid":123,"instance_id":"fixture-owner","observed_at":"2026-10-12T12:00:00Z","complete":true,"jobs":[{"id":"default","name":"default","enabled":true,"state":"healthy","generation":1}]}`)
			calls := 0
			run := func(ctx context.Context, executable string, args, env []string) error {
				calls++
				want := []string{"sync", "restart", "--profile-id", "default"}
				if whole {
					want = []string{"sync", "restart"}
				}
				if strings.Join(args, " ") != strings.Join(want, " ") {
					t.Fatalf("wrong restart scope: %v", args)
				}
				if whole {
					writeRawDaemonState(t, filepath.Join(root, "daemon-runtime.json"), `{"pid":124,"instance_id":"fixture-new","started_at":"2026-10-12T11:59:59Z","version":"dev"}`)
				}
				instance, pid := "fixture-owner", 123
				if whole {
					instance, pid = "fixture-new", 124
				}
				writeRawDaemonState(t, filepath.Join(root, "daemon-status.json"), fmt.Sprintf(`{"pid":%d,"instance_id":%q,"observed_at":"2026-10-12T12:00:00Z","complete":true,"jobs":[{"id":"default","name":"default","enabled":true,"state":"collecting","generation":2}]}`, pid, instance))
				return nil
			}
			svc := newDaemonControlTestService(t, root, root, func(string) daemonLockState { return daemonLockHeld }, run, nil, time.Second)
			svc.now = func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) }
			if _, err := (profile.Registry{Root: root}).List(); err != nil {
				info, _ := os.Stat(root)
				file, _ := os.Stat(filepath.Join(root, "profile.json"))
				t.Fatalf("fixture registry: %v root=%v file=%v", err, info.Mode(), file.Mode())
			}
			payload := map[string]any{"profile_id": "default"}
			if whole {
				payload = map[string]any{"scope": "daemon"}
			}
			response := call(t, svc, "restart", "daemon.restart", payload)
			if response.Error != nil || calls != 1 {
				t.Fatalf("restart failed/duplicated: %+v calls=%d", response.Error, calls)
			}
		})
	}
}

func TestDaemonSupervisorStatusDistinguishesProcessAndJobs(t *testing.T) {
	root := privateDaemonFixture(t)
	writeDaemonProfile(t, root, true, "")
	writeDaemonProfile(t, filepath.Join(root, "profiles", "one"), true, "")
	writeEmptyDaemonStates(t, root)
	writeEmptyDaemonStates(t, filepath.Join(root, "profiles", "one"))
	status := `{"pid":123,"instance_id":"fixture-owner","started_at":"2026-10-12T11:00:00Z","version":"dev","observed_at":"2026-10-12T12:00:00Z","complete":true,"jobs":[{"id":"default","name":"default","enabled":true,"state":"healthy","generation":1,"sent":4,"blocked_files":0,"last_pass_at":"2026-10-12T11:59:59Z"},{"id":"named:one","name":"one","enabled":true,"state":"stopped","generation":1,"sent":0,"blocked_files":0}]}`
	writeRawDaemonState(t, filepath.Join(root, "daemon-status.json"), status)
	writeRawDaemonState(t, filepath.Join(root, "daemon-runtime.json"), `{"pid":123,"instance_id":"fixture-owner","started_at":"2026-10-12T11:00:00Z","version":"dev"}`)
	if err := os.WriteFile(filepath.Join(root, "daemon.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	svc := newDaemonStatusService(t, root)
	if _, err := (profile.Registry{Root: root}).List(); err != nil {
		t.Fatalf("fixture registry: %v", err)
	}
	svc.daemonLockProbe = func(path string) daemonLockState {
		if path == filepath.Join(root, "daemon.lock") {
			return daemonLockHeld
		}
		return daemonLockFree
	}
	result := svc.readLiveDaemonStatus()
	if result.Daemon == nil || result.Daemon.State != "running" {
		t.Fatalf("overall singleton not running: %+v", result)
	}
	if len(result.Profiles) != 2 || result.Profiles[0].Collection.State != "healthy" || result.Profiles[1].Collection.State != "stopped" {
		t.Fatalf("job states collapsed into process state: %+v", result)
	}
	if result.Profiles[1].Job == nil || result.Profiles[1].ID != "named:one" || result.Profiles[1].Daemon.State != "running" {
		t.Fatalf("stopped job lost running process/identity: %+v", result.Profiles[1])
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("managed status: %s", encoded)
}

func TestDaemonSupervisorStaleJobStateIsUnknown(t *testing.T) {
	root := privateDaemonFixture(t)
	writeDaemonProfile(t, root, true, "")
	writeEmptyDaemonStates(t, root)
	writeRawDaemonState(t, filepath.Join(root, "daemon-runtime.json"), `{"pid":123,"instance_id":"fixture-owner","started_at":"2026-10-12T11:00:00Z","version":"dev"}`)
	writeRawDaemonState(t, filepath.Join(root, "daemon-status.json"), `{"pid":123,"instance_id":"fixture-owner","started_at":"2026-10-12T11:00:00Z","version":"dev","observed_at":"2026-10-12T11:00:00Z","complete":true,"jobs":[{"id":"default","name":"default","enabled":true,"state":"healthy"}]}`)
	svc := newDaemonStatusService(t, root)
	svc.daemonLockProbe = func(string) daemonLockState { return daemonLockHeld }
	result := svc.readLiveDaemonStatus()
	if result.Complete || result.Daemon == nil || result.Daemon.State != "running" || result.Profiles[0].Collection.State != "unknown" {
		t.Fatalf("stale snapshot claimed collection health: %+v", result)
	}
}

// These fixtures are private synthetic roots; no installed CLI or provider home is used.
func supervisorCursorFixture(t *testing.T) (string, localdaemon.Snapshot) {
	t.Helper()
	root := privateDaemonFixture(t)
	snapshot := localdaemon.Snapshot{Runtime: localdaemon.Runtime{PID: 123, InstanceID: "fixture-owner"}, ObservedAt: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC), Complete: true}
	for i := 0; i < 6; i++ {
		name, id, dir := "default", "default", root
		if i > 0 {
			name = fmt.Sprintf("peer%d", i)
			id = "named:" + name
			dir = filepath.Join(root, "profiles", name)
		}
		writeDaemonProfile(t, dir, true, "")
		writeEmptyDaemonStates(t, dir)
		job := localdaemon.JobStatus{ID: id, Name: name, Enabled: true, State: "healthy", Generation: 1, Sent: 4}
		if i == 1 {
			job.State = "blocked"
			job.BlockedFiles = 3
		}
		snapshot.Jobs = append(snapshot.Jobs, job)
	}
	writeRawDaemonState(t, filepath.Join(root, "daemon-runtime.json"), `{"pid":123,"instance_id":"fixture-owner","started_at":"2026-10-12T11:00:00Z","version":"dev"}`)
	writeSupervisorCursorSnapshot(t, root, snapshot)
	writeDaemonStates(t, filepath.Join(root, "profiles", "peer2"), daemonStateFile{
		Files:            map[string]daemonFileState{"synthetic": {LastSynced: snapshot.ObservedAt.Add(-time.Minute)}},
		TransportFailure: &daemonTransportFailure{Class: "server", FirstFailedAt: snapshot.ObservedAt.Add(-time.Hour), LastFailedAt: snapshot.ObservedAt.Add(-time.Minute), FilesFailed: 1},
	})
	// Sparse file exceeds the unchanged 4 MiB cap without allocating a large buffer.
	f, err := os.Create(filepath.Join(root, "codex-sync-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(maxDaemonInputFile + 1); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return root, snapshot
}
func writeSupervisorCursorSnapshot(t *testing.T, root string, snapshot localdaemon.Snapshot) {
	t.Helper()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	writeRawDaemonState(t, filepath.Join(root, "daemon-status.json"), string(data))
}
func TestDaemonSupervisorOversizedOptionalCursorPreservesCensusAndControls(t *testing.T) {
	t.Parallel()
	for _, whole := range []bool{false, true} {
		t.Run(fmt.Sprint(whole), func(t *testing.T) {
			root, snapshot := supervisorCursorFixture(t)
			calls := 0
			svc := newDaemonControlTestService(t, root, root, func(string) daemonLockState { return daemonLockHeld }, func(_ context.Context, _ string, args, env []string) error {
				calls++
				want := "sync restart --profile-id named:peer1"
				if whole {
					want = "sync restart"
				}
				if strings.Join(args, " ") != want {
					t.Fatalf("scope: %v", args)
				}
				snapshot.Jobs[1].Generation++
				snapshot.Jobs[1].State = "healthy"
				snapshot.Jobs[1].BlockedFiles = 0
				if whole {
					writeRawDaemonState(t, filepath.Join(root, "daemon-runtime.json"), `{"pid":123,"instance_id":"fixture-owner","started_at":"2026-10-12T11:59:59Z","version":"dev"}`)
				}
				writeSupervisorCursorSnapshot(t, root, snapshot)
				return nil
			}, nil, time.Second)
			svc.now = func() time.Time { return snapshot.ObservedAt }
			got := svc.readLiveDaemonStatus()
			if !got.Complete || len(got.Profiles) != 6 {
				t.Fatalf("optional cursor invalidated census: complete=%v profiles=%d error=%v", got.Complete, len(got.Profiles), got.Profiles[0].ErrorCode)
			}
			if got.Profiles[0].Collection.State != "healthy" || got.Profiles[1].Collection.State != "blocked" || got.Profiles[2].Collection.State != "healthy" {
				t.Fatalf("independent jobs lost: %+v", got.Profiles)
			}
			advance := got.Profiles[2].Collection.LastStateAdvanceAt
			if advance == nil || *advance != "2026-10-12T11:59:00Z" || got.Profiles[2].Collection.Failure != nil {
				t.Fatalf("valid legacy activity lost or stale failure promoted: %+v", got.Profiles[2])
			}
			if _, err := svc.controlProfileStatus("named:peer2"); err != nil {
				t.Fatalf("legacy failure blocked authoritative healthy peer: %+v", err)
			}
			diagnostic := got.Profiles[0].Collection.Counters.ErrorCode
			if diagnostic == nil || *diagnostic != "input_too_large" || got.Profiles[0].ErrorCode != nil {
				t.Fatalf("optional diagnostic lost or promoted: %+v", got.Profiles[0])
			}
			blocked := got.Profiles[1].Collection.Counters.BodyLimitBlockedFiles
			if blocked == nil || *blocked != 3 || got.Profiles[0].Collection.Counters.KnownFiles != nil {
				t.Fatalf("wrong counters: %+v", got.Profiles)
			}
			payload := map[string]any{"profile_id": "named:peer1"}
			if whole {
				payload = map[string]any{"scope": "daemon"}
			}
			response := call(t, svc, "restart", "daemon.restart", payload)
			if response.Error != nil || calls != 1 {
				t.Fatalf("recovery denied: %+v calls=%d", response.Error, calls)
			}
		})
	}
}
func TestDaemonSupervisorOptionalCursorCannotRelaxRequiredInputs(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"malformed", "stale", "nonce", "missing_job", "duplicate_job", "invalid_job", "config", "snapshot_cap", "snapshot_symlink", "config_symlink", "lock", "runtime_malformed", "runtime_future", "runtime_symlink", "extra_job", "duplicate_keys"} {
		t.Run(kind, func(t *testing.T) {
			root, snapshot := supervisorCursorFixture(t)
			switch kind {
			case "extra_job":
				snapshot.Jobs = append(snapshot.Jobs, localdaemon.JobStatus{ID: "named:absent", Name: "absent", Enabled: true, State: "healthy"})
			case "stale":
				snapshot.ObservedAt = snapshot.ObservedAt.Add(-time.Hour)
			case "nonce":
				snapshot.InstanceID = "other"
			case "missing_job":
				snapshot.Jobs = snapshot.Jobs[:5]
			case "duplicate_job":
				snapshot.Jobs[5] = snapshot.Jobs[0]
			case "invalid_job":
				snapshot.Jobs[1].Generation = -1
			}
			writeSupervisorCursorSnapshot(t, root, snapshot)
			switch kind {
			case "runtime_malformed":
				writeRawDaemonState(t, filepath.Join(root, "daemon-runtime.json"), `{"pid":123,"instance_id":"fixture-owner","started_at":"invalid"}`)
			case "runtime_future":
				writeRawDaemonState(t, filepath.Join(root, "daemon-runtime.json"), `{"pid":123,"instance_id":"fixture-owner","started_at":"2027-10-12T11:00:00Z"}`)
			case "duplicate_keys":
				data, _ := json.Marshal(snapshot)
				writeRawDaemonState(t, filepath.Join(root, "daemon-status.json"), strings.Replace(string(data), `"complete":true`, `"complete":false,"complete":true`, 1))
			case "malformed":
				writeRawDaemonState(t, filepath.Join(root, "daemon-status.json"), `{"jobs":`)
			case "config":
				writeRawDaemonState(t, filepath.Join(root, "profile.json"), `{"options":`)
			case "snapshot_cap":
				f, err := os.OpenFile(filepath.Join(root, "daemon-status.json"), os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.Truncate(maxDaemonInputFile + 1); err != nil {
					t.Fatal(err)
				}
				f.Close()
			case "snapshot_symlink", "config_symlink", "runtime_symlink":
				name := "daemon-status.json"
				if kind == "runtime_symlink" {
					name = "daemon-runtime.json"
				}
				if kind == "config_symlink" {
					name = "profile.json"
				}
				path := filepath.Join(root, name)
				if err := os.Rename(path, path+".target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".target", path); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			svc := newDaemonControlTestService(t, root, root, func(string) daemonLockState {
				if kind == "lock" {
					return daemonLockUnknown
				}
				return daemonLockHeld
			}, func(context.Context, string, []string, []string) error { calls++; return nil }, nil, time.Second)
			svc.now = func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) }
			got := svc.readLiveDaemonStatus()
			if kind != "lock" && got.Complete {
				t.Fatalf("required %s accepted", kind)
			}
			for _, payload := range []map[string]any{{"profile_id": "named:peer1"}, {"scope": "daemon"}} {
				response := call(t, svc, "restart", "daemon.restart", payload)
				if response.Error == nil || calls != 0 {
					t.Fatalf("unsafe %s control accepted: %+v calls=%d", kind, response, calls)
				}
			}
		})
	}
}

// A job that could not start for want of the policy lock is a transient error of
// its own: the census stays complete and the job's code reaches the app as it is.
func TestDaemonSupervisorCarriesPolicyLockBusy(t *testing.T) {
	root := privateDaemonFixture(t)
	writeDaemonProfile(t, root, true, "")
	writeEmptyDaemonStates(t, root)
	writeRawDaemonState(t, filepath.Join(root, "daemon-runtime.json"), `{"pid":123,"instance_id":"fixture-owner","started_at":"2026-10-12T11:00:00Z","version":"dev"}`)
	writeRawDaemonState(t, filepath.Join(root, "daemon-status.json"), `{"pid":123,"instance_id":"fixture-owner","started_at":"2026-10-12T11:00:00Z","version":"dev","observed_at":"2026-10-12T12:00:00Z","complete":true,"jobs":[{"id":"default","name":"default","enabled":true,"state":"error","generation":1,"error_code":"policy_lock_busy","next_retry_at":"2026-10-12T12:00:05Z"}]}`)
	svc := newDaemonStatusService(t, root)
	svc.daemonLockProbe = func(string) daemonLockState { return daemonLockHeld }

	result := svc.readLiveDaemonStatus()

	if !result.Complete || len(result.Profiles) != 1 {
		t.Fatalf("a job waiting on the policy lock made the census incomplete: %+v", result)
	}
	profile := result.Profiles[0]
	if profile.Collection.State != "error" || profile.Job == nil || profile.Job.ErrorCode == nil || *profile.Job.ErrorCode != "policy_lock_busy" {
		t.Fatalf("job %+v collection %q; want the error carried with its own code", profile.Job, profile.Collection.State)
	}
}
