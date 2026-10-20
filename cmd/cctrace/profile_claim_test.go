package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// claimWorld is a running daemon, as far as `profile claim` can tell: its lock,
// runtime, routes, allocations and snapshot, with profile "holder" holding the
// shared Codex folder and profile "work" left out of it.
type claimWorld struct {
	t       *testing.T
	root    string
	shared  string
	holder  profile.Entry
	work    profile.Entry
	routeID string
	sent    []localdaemon.ClaimRequest
	claimFn func(localdaemon.ClaimRequest) error
	lock    *flock.Flock
}

const claimRouteID = "4c7e19ab"

func newClaimWorld(t *testing.T, editHolder ...func(*profile.Profile)) *claimWorld {
	t.Helper()
	w := &claimWorld{t: t, routeID: claimRouteID}
	home := useTempSyncHome(t)
	w.root = profile.DefaultDir()
	w.shared = filepath.Join(home, "shared-codex")
	if err := os.MkdirAll(filepath.Join(w.shared, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.holder, _ = codexProfile(t, "holder", w.shared, editHolder...)
	w.work, _ = codexProfile(t, "work", w.shared)
	if err := os.MkdirAll(w.root, 0o700); err != nil {
		t.Fatal(err)
	}
	w.publish(t, "named:holder", "active")
	return w
}

// publish writes the daemon's files: the route is owned by owner, and work is
// told its holder is in state.
func (w *claimWorld) publish(t *testing.T, owner, state string) {
	t.Helper()
	spec := profileJobSpec(w.work, daemonOverrides{})
	route := localdaemon.Route{ID: w.routeID, Rev: 3, Kind: "codex", Path: w.shared, Destination: spec.Destination, Owner: owner, State: localdaemon.RouteActive, Since: time.Now().UTC().Add(-time.Hour)}
	if owner == "" {
		route.State, route.From = localdaemon.RouteUnowned, "named:holder"
	}
	file, err := localdaemon.WriteRoutes(w.root, localdaemon.RouteFile{Version: 1, Rev: 6, Routes: []localdaemon.Route{route}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := localdaemon.Runtime{PID: os.Getpid(), InstanceID: "inst-claim", StartedAt: time.Now().UTC().Add(-time.Minute), Version: "dev"}
	writeJSONFile0600(t, filepath.Join(w.root, "daemon-runtime.json"), runtime)
	writeJSONFile0600(t, filepath.Join(w.root, "daemon-allocations.json"), localdaemon.AllocationsFile{InstanceID: runtime.InstanceID, RoutesRev: file.Rev, Jobs: map[string]localdaemon.JobAllocation{
		"named:work": {Revision: w.work.Revision, Allowed: []string{}, Excluded: []localdaemon.ExcludedLine{{Line: "codex:" + w.shared, Route: w.routeID}}},
	}})
	holderName, holderState := "holder", state
	switch owner {
	case "":
		holderName, holderState = "", "unowned"
	case "named:gone":
		holderName, holderState = "", "removed"
	}
	writeJSONFile0600(t, filepath.Join(w.root, "daemon-status.json"), localdaemon.Snapshot{Runtime: runtime, ObservedAt: time.Now().UTC(), Complete: true, Jobs: []localdaemon.JobStatus{{
		ID: "named:work", Name: "work", Enabled: true, State: "blocked", ErrorCode: "duplicate_route",
		SharedSources: []localdaemon.SharedSource{{Source: w.routeID, Kind: "codex", Label: "configured", Reason: "same", Holder: owner, HolderName: holderName, HolderState: holderState}},
	}}})
	if w.lock == nil {
		w.lock = flock.New(filepath.Join(w.root, "daemon.lock"))
		if ok, err := w.lock.TryLock(); err != nil || !ok {
			t.Fatalf("fixture daemon lock: %v %v", ok, err)
		}
		t.Cleanup(func() { _ = w.lock.Unlock() })
	}
}

func writeJSONFile0600(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (w *claimWorld) stopDaemon() {
	w.t.Helper()
	if err := w.lock.Unlock(); err != nil {
		w.t.Fatal(err)
	}
}

// run runs the command with the given input and returns what it printed.
func (w *claimWorld) run(input string, tty bool, args claimArgs) (string, error) {
	w.t.Helper()
	var out bytes.Buffer
	env := profileClaimEnv{root: w.root, registry: profile.Registry{Root: profile.DefaultDir()}, in: strings.NewReader(input), out: &out, tty: tty,
		claim: func(_ context.Context, _ string, req localdaemon.ClaimRequest) error {
			w.sent = append(w.sent, req)
			if w.claimFn != nil {
				return w.claimFn(req)
			}
			return nil
		}}
	err := runProfileClaim(env, args)
	return out.String(), err
}

func TestProfileClaimShowsThePlanAndSendsOnYes(t *testing.T) {
	w := newClaimWorld(t, func(p *profile.Profile) {
		p.Options.CollectRepositoryPrefixes = []string{"host/org/one", "host/org/two"}
	})
	holderState, err := syncer.LoadState(filepath.Join(w.holder.RuntimeDir, "codex-sync-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	holderState.SetOffset(filepath.Join(w.shared, "sessions", "x.jsonl"), 3)
	if err := holderState.Save(); err != nil {
		t.Fatal(err)
	}

	out, err := w.run("y\n", true, claimArgs{profile: "work", sources: []string{claimRouteID}})
	if err != nil {
		t.Fatal(err)
	}
	want := `Claim codex source 4c7e19ab (configured folder) for profile "work"
  current holder : holder (active)
  cursor         : continue from "holder"
  filters        : repository allowlist: holder 2 entries, "work" none (collects every repository)
                   prompt redaction: holder off, "work" off
                   tool redaction: holder off, "work" off
                   excluded accounts: holder 0, "work" 0
                   note: "work" would send more than "holder" does
  effect         : "holder" stops reading this source; "work" starts after "holder" has released it
Proceed? [y/N] Claim 4c7e19ab accepted: "holder" lets go of it first, then "work" starts reading it.
`
	if out != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
	if len(w.sent) != 1 {
		t.Fatalf("sent %d requests", len(w.sent))
	}
	got := w.sent[0]
	want2 := localdaemon.ClaimRequest{Profile: "named:work", Source: claimRouteID, Holder: "named:holder", RouteRev: 3, TargetRev: w.work.Revision, HolderRev: w.holder.Revision}
	if got != want2 {
		t.Fatalf("request = %+v, want %+v", got, want2)
	}
	if strings.Contains(out, "host/org") || strings.Contains(out, w.shared) {
		t.Fatalf("the plan prints a filter value or a path: %s", out)
	}
}

func TestProfileClaimDoesNothingUnlessTheAnswerIsYes(t *testing.T) {
	for _, input := range []string{"\n", "n\n", "no\n", "", "yes please\n"} {
		w := newClaimWorld(t)
		out, err := w.run(input, true, claimArgs{profile: "work", sources: []string{claimRouteID}})
		if err != nil || len(w.sent) != 0 || !strings.HasSuffix(out, "Proceed? [y/N] Aborted.\n") {
			t.Fatalf("input %q: sent %d, err %v, out %q", input, len(w.sent), err, out)
		}
	}
	w := newClaimWorld(t)
	if _, err := w.run("Y\n", true, claimArgs{profile: "work", sources: []string{claimRouteID}}); err != nil || len(w.sent) != 1 {
		t.Fatalf("a capital Y: sent %d, %v", len(w.sent), err)
	}
}

func TestProfileClaimRefusesWithoutATerminalUnlessForced(t *testing.T) {
	w := newClaimWorld(t)
	out, err := w.run("y\n", false, claimArgs{profile: "work", sources: []string{claimRouteID}})
	if err == nil || !strings.Contains(err.Error(), "--force") || len(w.sent) != 0 {
		t.Fatalf("a script must say it means it: err %v, sent %d", err, len(w.sent))
	}
	if strings.Contains(out, "Proceed?") {
		t.Fatalf("prompted on a stream nobody reads: %s", out)
	}
	out, err = w.run("", false, claimArgs{profile: "work", sources: []string{claimRouteID}, force: true})
	if err != nil || len(w.sent) != 1 || strings.Contains(out, "Proceed?") || !strings.Contains(out, "Claim codex source 4c7e19ab") {
		t.Fatalf("--force: err %v, sent %d, out %s", err, len(w.sent), out)
	}
}

func TestProfileClaimNeedsARunningDaemon(t *testing.T) {
	w := newClaimWorld(t)
	w.stopDaemon()
	_, err := w.run("y\n", true, claimArgs{profile: "work", sources: []string{claimRouteID}, force: true})
	if err == nil || !strings.Contains(err.Error(), "daemon is not running") || !strings.Contains(err.Error(), "cctrace sync --daemon") || len(w.sent) != 0 {
		t.Fatalf("err = %v, sent %d", err, len(w.sent))
	}
}

func TestProfileClaimFindsTheSourceByIDOrByFolder(t *testing.T) {
	alias := filepath.Join(t.TempDir(), "alias")
	for _, tc := range []struct {
		name  string
		pick  func(w *claimWorld) string
		found bool
	}{
		{"by id", func(*claimWorld) string { return claimRouteID }, true},
		{"by folder", func(w *claimWorld) string { return w.shared }, true},
		{"by line", func(w *claimWorld) string { return "codex:" + w.shared }, true},
		{"by a link to the folder", func(w *claimWorld) string {
			if err := os.Symlink(w.shared, alias); err != nil {
				t.Skip("symlinks unavailable")
			}
			return alias
		}, true},
		{"an unknown id", func(*claimWorld) string { return "00000000" }, false},
		{"another folder", func(w *claimWorld) string { return filepath.Join(filepath.Dir(w.shared), "elsewhere") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newClaimWorld(t)
			_, err := w.run("y\n", true, claimArgs{profile: "work", sources: []string{tc.pick(w)}})
			if tc.found && (err != nil || len(w.sent) != 1) {
				t.Fatalf("err %v, sent %d", err, len(w.sent))
			}
			if !tc.found && (err == nil || len(w.sent) != 0 || !strings.Contains(err.Error(), "not held back from") || !strings.Contains(err.Error(), "--all")) {
				t.Fatalf("err %v, sent %d", err, len(w.sent))
			}
		})
	}
}

func TestProfileClaimNeedsToBeToldWhatToClaim(t *testing.T) {
	w := newClaimWorld(t)
	_, err := w.run("", true, claimArgs{profile: "work"})
	if err == nil || !strings.Contains(err.Error(), "--source") || !strings.Contains(err.Error(), "--all") || !strings.Contains(err.Error(), claimRouteID) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), w.shared) {
		t.Fatalf("the hint names a folder path: %v", err)
	}
	out, err := w.run("y\n", true, claimArgs{profile: "work", all: true})
	if err != nil || len(w.sent) != 1 || !strings.Contains(out, "Claim codex source 4c7e19ab") {
		t.Fatalf("--all: %v %d %s", err, len(w.sent), out)
	}
	w2 := newClaimWorld(t)
	w2.stopDaemon()
	if _, err := w2.run("", true, claimArgs{profile: "nobody", all: true}); err == nil {
		t.Fatal("an unknown profile is an error")
	}
}

func TestProfileClaimWithNothingHeldBackSaysSo(t *testing.T) {
	w := newClaimWorld(t)
	_, err := w.run("", true, claimArgs{profile: "holder", all: true})
	if err == nil || !strings.Contains(err.Error(), "nothing to claim") || len(w.sent) != 0 {
		t.Fatalf("err = %v, sent %d", err, len(w.sent))
	}
}

func TestProfileClaimTellsWhatTheDaemonRefused(t *testing.T) {
	for code, want := range map[string]string{
		"route_changed":           "changed since the plan",
		"profile_changed":         "changed since the plan",
		"handoff_pending":         "hand-over of this source is still in progress",
		"invalid_source":          "no longer held back",
		"line_busy":               "still being read",
		"daemon_instance_changed": "was restarted",
		"invalid_action":          "too old",
		"state_write_failed":      "could not save",
	} {
		w := newClaimWorld(t)
		w.claimFn = func(localdaemon.ClaimRequest) error { return &localdaemon.ControlError{Code: code} }
		_, err := w.run("y\n", true, claimArgs{profile: "work", all: true})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want it to say %q", code, err, want)
		}
	}
	w := newClaimWorld(t)
	w.claimFn = func(localdaemon.ClaimRequest) error { return errors.New("pipe closed") }
	if _, err := w.run("y\n", true, claimArgs{profile: "work", all: true}); err == nil || !strings.Contains(err.Error(), "pipe closed") {
		t.Fatalf("err = %v", err)
	}
}

func TestProfileClaimNamesAHolderThatIsGoneOrNobody(t *testing.T) {
	for _, tc := range []struct{ owner, holder, effect string }{
		{"named:gone", "current holder : gone (removed profile)", `"work" starts reading this source at once`},
		{"", "current holder : (nobody)", `"work" starts reading this source at once`},
	} {
		w := newClaimWorld(t)
		w.publish(t, tc.owner, "")
		out, err := w.run("y\n", true, claimArgs{profile: "work", all: true})
		if err != nil || !strings.Contains(out, tc.holder) || !strings.Contains(out, tc.effect) {
			t.Fatalf("owner %q: err %v\n%s", tc.owner, err, out)
		}
		if got := w.sent[0]; got.Holder != tc.owner || (tc.owner != "" && got.HolderRev != "") {
			t.Fatalf("owner %q: request %+v", tc.owner, got)
		}
	}
}

// A source nobody holds goes on from the last profile that held it: the daemon
// takes the cursor over from the route's From, and the plan says so.
func TestProfileClaimOfASourceNobodyHoldsContinuesFromItsLastHolder(t *testing.T) {
	w := newClaimWorld(t)
	w.publish(t, "", "")
	state, err := syncer.LoadState(filepath.Join(w.holder.RuntimeDir, "codex-sync-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffset(filepath.Join(w.shared, "sessions", "x.jsonl"), 3)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	out, err := w.run("y\n", true, claimArgs{profile: "work", all: true})
	if err != nil || !strings.Contains(out, "current holder : (nobody)") || !strings.Contains(out, `cursor         : continue from "holder"`) {
		t.Fatalf("err %v\n%s", err, out)
	}
}

// With no usable routes file the daemon has no ids: the line is the name, and the
// first claim has the daemon rebuild the file. What is claimed is what a running
// daemon published, and every line --all names is claimed, the later ones by the
// ids the rebuilt file gives them: the command `cctrace status` points to works.
func TestProfileClaimWhenTheRoutesFileCannotBeUsedRebuildsIt(t *testing.T) {
	home := useTempSyncHome(t)
	resetSyncControlHooks(t)
	shared, second := filepath.Join(home, "shared-codex"), filepath.Join(home, "second-codex")
	for _, dir := range []string{shared, second} {
		if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	both := func(p *profile.Profile) {
		p.User = profile.UserInfo{ID: "user-1", Name: "one", Email: "one@fixture.invalid", Team: "fixture"}
		p.Options.CodexDirs = []string{shared, second}
	}
	codexProfile(t, "holder", shared, both)
	codexProfile(t, "work", shared, both)
	root := profile.DefaultDir()
	if err := os.WriteFile(filepath.Join(root, "daemon-routes.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	startSupervisor(t, localdaemon.New(localdaemon.Config{Root: root, Refresh: 5 * time.Millisecond, Load: func() ([]localdaemon.Spec, error) {
		entries, err := (profile.Registry{Root: root}).List()
		if err != nil {
			return nil, err
		}
		var specs []localdaemon.Spec
		for _, entry := range entries {
			spec := profileJobSpec(entry, daemonOverrides{})
			spec.Quiesce = nil
			spec.Acquire = func(ctx context.Context, alloc localdaemon.Allocation) (func(), error) {
				return localdaemon.LockLines(ctx, root, alloc.Routes, 0)
			}
			spec.Run = func(_ context.Context, report func(localdaemon.Progress)) error {
				report(localdaemon.Progress{State: "healthy"})
				return nil
			}
			specs = append(specs, spec)
		}
		return specs, nil
	}}))
	managedEventually(t, func() bool {
		alloc, err := localdaemon.ReadAllocations(root)
		return err == nil && len(alloc.Jobs["named:work"].Excluded) == 2
	})
	// A run outside the daemon keeps off what the daemon closed.
	gate := newLineGate("work", false)
	if got := applyOnce(t, gate, "codex", shared, second); len(got) != 0 || !gate.allHeld() {
		t.Fatalf("work's run read %v, allHeld %v", got, gate.allHeld())
	}
	gate.close()
	// `cctrace status` says to claim them all, and that is the command run below.
	managedEventually(t, func() bool {
		return strings.Contains(routesNoticeFor("work"), "cctrace profile claim <프로필> --all")
	})

	var out bytes.Buffer
	err := runProfileClaim(profileClaimEnv{root: root, registry: profile.Registry{Root: root}, in: strings.NewReader(""), out: &out, claim: localdaemon.Claim}, claimArgs{profile: "work", all: true, force: true})
	if err != nil {
		t.Fatalf("claim: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "current holder : (unknown; the routes file could not be read)") {
		t.Fatalf("output:\n%s", out.String())
	}
	routes, err := localdaemon.ReadRoutes(root)
	if err != nil {
		t.Fatalf("the rebuilt routes file: %v", err)
	}
	owned := 0
	for _, r := range routes.Routes {
		if r.Owner == "named:work" && r.State == localdaemon.RouteActive {
			owned++
		}
	}
	if owned != 2 {
		t.Fatalf("work owns %d of the two sources: %+v", owned, routes.Routes)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "daemon-routes.json.invalid-*")); len(matches) != 1 {
		t.Fatalf("the damaged file is kept aside once: %v", matches)
	}
}

func TestProfileClaimRefusesAStaleDaemonRecord(t *testing.T) {
	w := newClaimWorld(t)
	writeJSONFile0600(t, filepath.Join(w.root, "daemon-allocations.json"), localdaemon.AllocationsFile{InstanceID: "an-earlier-daemon", Jobs: map[string]localdaemon.JobAllocation{}})
	_, err := w.run("y\n", true, claimArgs{profile: "work", all: true})
	if err == nil || !strings.Contains(err.Error(), "try again") || len(w.sent) != 0 {
		t.Fatalf("err = %v, sent %d", err, len(w.sent))
	}
}
