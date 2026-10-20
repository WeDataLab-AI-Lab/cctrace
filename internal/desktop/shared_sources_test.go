package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
)

const sharedSourcesTestID = "0a1b2c3d"

// sharedSourcesRoot writes a daemon root whose snapshot holds one default job.
func sharedSourcesRoot(t *testing.T, job localdaemon.JobStatus) string {
	t.Helper()
	root := privateDaemonFixture(t)
	writeDaemonProfile(t, root, true, "")
	writeEmptyDaemonStates(t, root)
	writeSharedSourcesSnapshot(t, root, 123, "fixture-owner", job)
	writeRawDaemonState(t, filepath.Join(root, "daemon-runtime.json"), `{"pid":123,"instance_id":"fixture-owner","started_at":"2026-10-12T11:00:00Z","version":"dev"}`)
	return root
}

func writeSharedSourcesSnapshot(t *testing.T, root string, pid int, instance string, job localdaemon.JobStatus) {
	t.Helper()
	snapshot := localdaemon.Snapshot{
		Runtime:    localdaemon.Runtime{PID: pid, InstanceID: instance, StartedAt: time.Date(2026, 10, 12, 11, 0, 0, 0, time.UTC), Version: "dev"},
		ObservedAt: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC),
		Complete:   true,
		Jobs:       []localdaemon.JobStatus{job},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	writeRawDaemonState(t, filepath.Join(root, "daemon-status.json"), string(data))
}

func sharedSourcesService(t *testing.T, job localdaemon.JobStatus) *Service {
	t.Helper()
	svc := newDaemonStatusService(t, sharedSourcesRoot(t, job))
	svc.daemonLockProbe = func(string) daemonLockState { return daemonLockHeld }
	return svc
}

func sharedSourcesJob(items ...localdaemon.SharedSource) localdaemon.JobStatus {
	return localdaemon.JobStatus{ID: "default", Name: "default", Enabled: true, State: "healthy", Generation: 1, Sent: 4, SharedSources: items}
}

func sharedSourceItem() localdaemon.SharedSource {
	return localdaemon.SharedSource{Source: sharedSourcesTestID, Kind: "codex", Label: "default", Reason: localdaemon.ShareSame,
		Holder: "named:work", HolderName: "work", HolderState: localdaemon.HolderActive}
}

type sharedSourcesWire struct {
	Complete bool `json:"complete"`
	Profiles []struct {
		Job map[string]json.RawMessage `json:"job"`
	} `json:"profiles"`
}

func readSharedSourcesWire(t *testing.T, svc *Service, payload map[string]any) (sharedSourcesWire, []map[string]string) {
	t.Helper()
	response := call(t, svc, "read", "daemon.status.read", payload)
	if response.Error != nil {
		t.Fatalf("status read rejected: %+v", response.Error)
	}
	var wire sharedSourcesWire
	if err := json.Unmarshal(response.Result, &wire); err != nil || len(wire.Profiles) != 1 || wire.Profiles[0].Job == nil {
		t.Fatalf("status shape: %v %s", err, response.Result)
	}
	var items []map[string]string
	if raw, ok := wire.Profiles[0].Job["shared_sources"]; ok {
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatalf("shared_sources shape: %v %s", err, raw)
		}
	}
	return wire, items
}

func TestSharedSourcesStayOffTheWireUnlessRequested(t *testing.T) {
	svc := sharedSourcesService(t, sharedSourcesJob(sharedSourceItem()))
	for _, payload := range []map[string]any{{}, {"diagnostics_version": 1}} {
		wire, items := readSharedSourcesWire(t, svc, payload)
		if len(wire.Profiles[0].Job) != 6 || items != nil {
			t.Fatalf("payload %v changed the job's six keys: %v", payload, wire.Profiles[0].Job)
		}
	}
	for _, payload := range []map[string]any{{"shared_sources": true}, {"shared_sources": true, "diagnostics_version": 1}} {
		wire, items := readSharedSourcesWire(t, svc, payload)
		if len(wire.Profiles[0].Job) != 7 || len(items) != 1 {
			t.Fatalf("payload %v lost the warning: %v", payload, wire.Profiles[0].Job)
		}
		want := map[string]string{"source": sharedSourcesTestID, "kind": "codex", "label": "default", "reason": "same", "holder_name": "work", "holder_state": "active"}
		for key, value := range want {
			if items[0][key] != value {
				t.Fatalf("item %v, want %v", items[0], want)
			}
		}
		if len(items[0]) != len(want) {
			t.Fatalf("the holder ID or another key leaked: %v", items[0])
		}
	}
	for _, payload := range []map[string]any{{"shared_sources": false}, {"shared_sources": "true"}, {"shared_sources": true, "extra": true}, {"diagnostics_version": 2, "shared_sources": true}} {
		if response := call(t, svc, "bad", "daemon.status.read", payload); response.Error == nil || response.Error.Code != "invalid_payload" {
			t.Fatalf("payload %v accepted: %+v", payload, response.Error)
		}
	}
}

func TestSharedSourcesCapabilityIsRequestedByName(t *testing.T) {
	svc := newDaemonStatusService(t, t.TempDir())
	var hello struct {
		Capabilities map[string]bool `json:"capabilities"`
	}
	asked := call(t, svc, "asked", "hello", map[string]any{"daemon_shared_sources": true})
	if asked.Error != nil || json.Unmarshal(asked.Result, &hello) != nil || !hello.Capabilities["daemon_shared_sources"] {
		t.Fatalf("capability not advertised: %s %+v", asked.Result, asked.Error)
	}
	hello.Capabilities = nil
	for _, payload := range []map[string]any{{}, {"daemon_status": true}, {"daemon_diagnostics": true}} {
		other := call(t, svc, "other", "hello", payload)
		if other.Error != nil || strings.Contains(string(other.Result), "daemon_shared_sources") {
			t.Fatalf("capability advertised without a request for %v: %s", payload, other.Result)
		}
	}
}

func TestSharedSourcesAreValidatedItemByItem(t *testing.T) {
	good := sharedSourceItem()
	badSource := func(source string) localdaemon.SharedSource { s := good; s.Source = source; return s }
	odd := good
	odd.Kind, odd.Label, odd.Reason, odd.HolderState, odd.HolderName = "mystery", "mystery", "mystery", "mystery", "../secret"
	items := []localdaemon.SharedSource{badSource(""), badSource("ABCDEF12"), badSource("0a1b2c3"), badSource("0a1b2c3d4"), badSource("0a1b2c3z"), odd, good}
	svc := sharedSourcesService(t, sharedSourcesJob(items...))
	wire, got := readSharedSourcesWire(t, svc, map[string]any{"shared_sources": true})
	if !wire.Complete || len(got) != 2 {
		t.Fatalf("invalid items were not dropped one by one: complete=%v %v", wire.Complete, got)
	}
	for _, key := range []string{"kind", "label", "reason", "holder_state"} {
		if got[0][key] != "unknown" {
			t.Fatalf("%s not normalized: %v", key, got[0])
		}
	}
	if _, named := got[0]["holder_name"]; named || got[1]["holder_name"] != "work" {
		t.Fatalf("holder name not checked against the profile name rule: %v", got)
	}
}

func TestSharedSourcesAreTruncated(t *testing.T) {
	items := make([]localdaemon.SharedSource, 0, 40)
	for i := 0; i < 40; i++ {
		item := sharedSourceItem()
		item.Source = fmt.Sprintf("%08x", i)
		items = append(items, item)
	}
	svc := sharedSourcesService(t, sharedSourcesJob(items...))
	_, got := readSharedSourcesWire(t, svc, map[string]any{"shared_sources": true})
	if len(got) != 16 || got[0]["source"] != "00000000" || got[15]["source"] != "0000000f" {
		t.Fatalf("not cut to the first 16: %d %v", len(got), got)
	}
}

func TestSharedSourcesNeverChangeComplete(t *testing.T) {
	garbage := sharedSourceItem()
	garbage.Source, garbage.Kind, garbage.HolderName = "not-an-id", "mystery", "../secret"
	plain := sharedSourcesService(t, sharedSourcesJob())
	for name, job := range map[string]localdaemon.JobStatus{
		"valid":   sharedSourcesJob(sharedSourceItem()),
		"garbage": sharedSourcesJob(garbage, garbage),
	} {
		svc := sharedSourcesService(t, job)
		want, got := plain.readLiveDaemonStatus(), svc.readLiveDaemonStatus()
		if !got.Complete || got.Complete != want.Complete || got.Profiles[0].Job == nil || got.Profiles[0].ErrorCode != nil ||
			got.Profiles[0].Collection.State != want.Profiles[0].Collection.State {
			t.Fatalf("%s: a diagnostic changed the verdict: %+v vs %+v", name, got, want)
		}
	}
}

func TestSharedSourcesDoNotBlockPartialExclusionRestart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		job      localdaemon.JobStatus
		wantCode string
	}{
		{name: "partial", job: sharedSourcesJob(sharedSourceItem())},
		{name: "all excluded", job: func() localdaemon.JobStatus {
			job := sharedSourcesJob(sharedSourceItem())
			job.State, job.ErrorCode = "error", "duplicate_route"
			return job
		}(), wantCode: "daemon_profile_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := sharedSourcesRoot(t, tc.job)
			calls := 0
			run := func(context.Context, string, []string, []string) error {
				calls++
				next := tc.job
				next.Generation = 2
				writeSharedSourcesSnapshot(t, root, 123, "fixture-owner", next)
				return nil
			}
			svc := newDaemonControlTestService(t, root, root, func(string) daemonLockState { return daemonLockHeld }, run, nil, time.Second)
			svc.now = func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) }
			response := call(t, svc, "restart", "daemon.restart", map[string]any{"profile_id": "default"})
			if tc.wantCode == "" {
				if response.Error != nil || calls != 1 {
					t.Fatalf("a profile that only shares some sources was not restarted: %+v calls=%d", response.Error, calls)
				}
				return
			}
			if response.Error == nil || response.Error.Code != tc.wantCode || calls != 0 {
				t.Fatalf("a profile that reads nothing was restarted: %+v calls=%d", response.Error, calls)
			}
		})
	}
}
