package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

func statusFixture() (localdaemon.Snapshot, time.Time) {
	started := time.Date(2026, 10, 15, 9, 12, 3, 0, time.UTC)
	now := started.Add(19*time.Minute + 10*time.Second)
	shared := func(kind, id, label, holder, name, state string) localdaemon.SharedSource {
		return localdaemon.SharedSource{Source: id, Kind: kind, Label: label, Reason: localdaemon.ShareSame, Holder: holder, HolderName: name, HolderState: state}
	}
	return localdaemon.Snapshot{
		Runtime:    localdaemon.Runtime{PID: 4121, InstanceID: "inst", StartedAt: started, Version: "0.7.66"},
		ObservedAt: now.Add(-3 * time.Second),
		Complete:   true,
		Jobs: []localdaemon.JobStatus{
			{ID: "default", Name: "default", State: "healthy", Sent: 1204, LastPassAt: time.Date(2026, 10, 15, 9, 31, 10, 0, time.UTC)},
			{ID: "named:work", Name: "work", State: "healthy", Sent: 311, LastPassAt: time.Date(2026, 10, 15, 9, 31, 8, 0, time.UTC), SharedSources: []localdaemon.SharedSource{
				shared("codex", "3fa91c2d", "default", "default", "default", "active"),
				shared("codex", "9b01d7e4", "explicit", "named:work2", "work2", "stopped"),
			}},
			{ID: "named:travel", Name: "travel", State: "blocked", ErrorCode: "duplicate_route", SharedSources: []localdaemon.SharedSource{
				shared("claude", "77aa10c2", "home", "default", "default", "active"),
			}},
		},
	}, now
}

func TestSyncStatusTableGolden(t *testing.T) {
	snapshot, now := statusFixture()
	var out bytes.Buffer
	renderSyncStatus(&out, snapshot, []downgradeRow{{Source: "9b01d7e4", Reason: "holder split across profiles; resolve with: cctrace profile claim <profile> --source 9b01d7e4"}}, now)
	want := `daemon   pid 4121   version 0.7.66   started 2026-10-15T09:12:03Z   observed 3s ago   complete yes

PROFILE  STATE    SENT  LAST PASS             NEXT RETRY  CODE
default  healthy  1204  2026-10-15T09:31:10Z  -           -
work     healthy   311  2026-10-15T09:31:08Z  -           -
travel   blocked     0  -                     -           duplicate_route

SHARED SOURCES (collected by another profile, not by the one on the left)
PROFILE  KIND    SOURCE    LABEL     REASON  HELD BY  HOLDER STATE
work     codex   3fa91c2d  default   same    default  active
work     codex   9b01d7e4  explicit  same    work2    stopped
travel   claude  77aa10c2  home      same    default  active

DOWNGRADE (routes older develop builds cannot represent; v0.7.65 is not affected)
SOURCE    REASON
9b01d7e4  holder split across profiles; resolve with: cctrace profile claim <profile> --source 9b01d7e4
`
	if out.String() != want {
		t.Fatalf("table:\n%s\nwant:\n%s", out.String(), want)
	}
}

// Nothing shared and nothing unrepresentable: those sections are not there at
// all, and a time that is not set, a name that is not set, are dashes.
func TestSyncStatusTableOmitsEmptySections(t *testing.T) {
	snapshot, now := statusFixture()
	for i := range snapshot.Jobs {
		snapshot.Jobs[i].SharedSources = nil
	}
	snapshot.Complete = false
	snapshot.Jobs[0].NextRetryAt = time.Date(2026, 10, 15, 9, 40, 0, 0, time.UTC)
	var out bytes.Buffer
	renderSyncStatus(&out, snapshot, nil, now)
	got := out.String()
	if strings.Contains(got, "SHARED SOURCES") || strings.Contains(got, "DOWNGRADE") {
		t.Fatalf("empty sections are printed:\n%s", got)
	}
	if !strings.Contains(got, "complete no") || !strings.Contains(got, "2026-10-15T09:40:00Z") {
		t.Fatalf("table:\n%s", got)
	}
}

// A holder that is gone or nobody's has no name, and a source without a route id
// has none either; the table says so with a dash and never invents one.
func TestSyncStatusTableDashesWhatIsNotKnown(t *testing.T) {
	snapshot, now := statusFixture()
	snapshot.Jobs = snapshot.Jobs[:1]
	snapshot.Jobs[0].SharedSources = []localdaemon.SharedSource{
		{Kind: "codex", Label: "configured", Reason: localdaemon.ShareNested, HolderState: localdaemon.HolderUnowned},
		{Source: "00000002", Kind: "gjc", Label: "default", Reason: localdaemon.ShareSame, Holder: "named:gone", HolderState: localdaemon.HolderRemoved},
	}
	var out bytes.Buffer
	renderSyncStatus(&out, snapshot, nil, now)
	for _, want := range []string{
		"default  codex  -         configured  nested  -        unowned",
		"default  gjc    00000002  default     same    gone     removed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("table lacks %q:\n%s", want, out.String())
		}
	}
}

func route(id, owner, state string, edit ...func(*localdaemon.Route)) localdaemon.Route {
	r := localdaemon.Route{ID: id, Rev: 1, Kind: "codex", Path: "p-" + id, Destination: strings.Repeat("a", 64), Owner: owner, State: state, Since: time.Now().UTC()}
	for _, e := range edit {
		e(&r)
	}
	return r
}

func TestDowngradeRowsNameWhatOlderBuildsCannotRepresent(t *testing.T) {
	const resolve = "resolve with: cctrace profile claim <profile> --source "
	known := map[string]bool{"default": true, "named:work": true, "named:work2": true}
	file := localdaemon.RouteFile{Version: 1, Rev: 5, Routes: []localdaemon.Route{
		route("00000001", "default", localdaemon.RouteActive),
		route("00000002", "named:work2", localdaemon.RouteActive),                                                    // work is left out of it and owns 00000003
		route("00000003", "named:work", localdaemon.RouteActive),                                                     // owned and clean for default
		route("00000004", "", localdaemon.RouteUnowned),                                                              // nobody
		route("00000005", "named:gone", localdaemon.RouteActive),                                                     // holder removed
		route("00000006", "default", localdaemon.RouteReleasing, func(r *localdaemon.Route) { r.To = "named:work" }), // handing over
		route("00000007", "default", localdaemon.RouteActive, func(r *localdaemon.Route) { r.From, r.Handoff = "named:work", "abcd" }),
		route("00000008", "default", localdaemon.RouteActive, func(r *localdaemon.Route) { r.Aliases = []string{strings.Repeat("b", 64)} }),
	}}
	snapshot := localdaemon.Snapshot{Jobs: []localdaemon.JobStatus{
		{ID: "default"},
		{ID: "named:work", SharedSources: []localdaemon.SharedSource{{Source: "00000002", Holder: "named:work2"}}},
		{ID: "named:work2"},
	}}
	got := downgradeRows(file, known, snapshot)
	want := []downgradeRow{
		{"00000002", "holder split across profiles; " + resolve + "00000002"},
		{"00000004", "no profile holds it; " + resolve + "00000004"},
		{"00000005", "its holder profile no longer exists; " + resolve + "00000005"},
		{"00000006", "a hand-over is in progress; check again in a minute"},
		{"00000007", "a hand-over is in progress; check again in a minute"},
		{"00000008", "it keeps the destination a policy move left behind; it clears when the other profiles move too, or " + resolve + "00000008"},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// With the profile list unknown, a holder is not called removed.
	for _, row := range downgradeRows(file, nil, snapshot) {
		if row.Source == "00000005" {
			t.Fatalf("a holder was called removed with no profile list to say so: %+v", row)
		}
	}
	// Plain routes alone: nothing to say.
	if rows := downgradeRows(localdaemon.RouteFile{Version: 1, Routes: file.Routes[:1]}, known, localdaemon.Snapshot{Jobs: snapshot.Jobs[:1]}); len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

// writeDaemonFiles puts the files a running daemon leaves where the command reads them.
func writeDaemonFiles(t *testing.T, snapshot localdaemon.Snapshot, file *localdaemon.RouteFile) string {
	t.Helper()
	root := profile.DefaultDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSONFile0600(t, filepath.Join(root, "daemon-status.json"), snapshot)
	if file != nil {
		if _, err := localdaemon.WriteRoutes(root, *file); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestSyncStatusCommandPrintsTheTableAndKeepsTheJSONForScripts(t *testing.T) {
	useTempSyncHome(t)
	snapshot, now := statusFixture()
	writeDaemonFiles(t, snapshot, nil)

	var table bytes.Buffer
	if err := runManagedStatus(&table, false, now); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(table.String(), "daemon   pid 4121") || strings.Contains(table.String(), "DOWNGRADE") {
		t.Fatalf("table:\n%s", table.String())
	}

	var asJSON bytes.Buffer
	if err := runManagedStatus(&asJSON, true, now); err != nil {
		t.Fatal(err)
	}
	var back localdaemon.Snapshot
	if err := json.Unmarshal(asJSON.Bytes(), &back); err != nil {
		t.Fatalf("--json is not the snapshot: %v\n%s", err, asJSON.String())
	}
	if back.PID != 4121 || len(back.Jobs) != 3 || len(back.Jobs[1].SharedSources) != 2 {
		t.Fatalf("snapshot = %+v", back)
	}
	want, _ := json.MarshalIndent(back, "", "  ")
	if strings.TrimSpace(asJSON.String()) != string(want) {
		t.Fatalf("--json changed shape:\n%s", asJSON.String())
	}

	// No snapshot at all is an error either way, as it was.
	if err := os.Remove(filepath.Join(profile.DefaultDir(), "daemon-status.json")); err != nil {
		t.Fatal(err)
	}
	if err := runManagedStatus(&table, false, now); err == nil {
		t.Fatal("a missing snapshot is not an error")
	}
}

func TestSyncStatusFlagJSONIsOnlyForStatus(t *testing.T) {
	cmd := syncCmd()
	cmd.SetArgs([]string{"--json"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--json applies only to 'cctrace sync status'") {
		t.Fatalf("err = %v", err)
	}
}

// Lines of the table and the downgrade section carry no folder: a log or a
// pasted screen must not leak what a profile is configured with.
func TestSyncStatusTableNeverShowsAPath(t *testing.T) {
	useTempSyncHome(t)
	snapshot, now := statusFixture()
	secret := filepath.Join(t.TempDir(), "secret-folder")
	file := &localdaemon.RouteFile{Version: 1, Rev: 2, Routes: []localdaemon.Route{
		route("00000004", "", localdaemon.RouteUnowned, func(r *localdaemon.Route) { r.Path = secret }),
	}}
	writeDaemonFiles(t, snapshot, file)
	var table bytes.Buffer
	if err := runManagedStatus(&table, false, now); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(table.String(), "secret-folder") || strings.Contains(table.String(), secret) || !strings.Contains(table.String(), "DOWNGRADE") {
		t.Fatalf("table:\n%s", table.String())
	}
}
