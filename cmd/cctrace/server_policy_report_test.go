package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"cctrace/internal/configtxn"
	"cctrace/internal/profile"
	"cctrace/internal/store"
)

// HOME and the signing-key injection are process-wide; these tests stay serial,
// like the rest of the policy tests.

// policyReportProfile saves a logged-in profile that talks to endpoint, in a
// temp home. name is "" for the default profile.
func policyReportProfile(t *testing.T, name, endpoint string) *profile.Profile {
	t.Helper()
	p := profile.NewDefault()
	p.User.ID = "alice"
	p.User.Email = "alice@example.com"
	p.Server.SyncEndpoint = endpoint
	p.Server.Endpoint = endpoint
	p.Server.Protocol = "grpc"
	p.Server.AuthToken = "cli-token"
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	p.Options.CodexSyncDeclined = true
	var err error
	if name == "" {
		err = profile.Save(p)
	} else {
		err = profile.SaveNamed(p, name)
	}
	if err != nil {
		t.Fatalf("save profile %q: %v", name, err)
	}
	return p
}

type receivedPolicyReport struct {
	auth string
	body store.ClientPolicyReport
}

// policyReportServer answers `policyStatus` to the policy fetch and records what
// arrives on the report route. The returned function gives what has arrived.
func policyReportServer(t *testing.T, policyStatus, reportStatus int) (*httptest.Server, func() []receivedPolicyReport) {
	t.Helper()
	var mu sync.Mutex
	var got []receivedPolicyReport
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/client-policy-report" {
			w.WriteHeader(policyStatus)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body store.ClientPolicyReport
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		got = append(got, receivedPolicyReport{r.Header.Get("Authorization"), body})
		mu.Unlock()
		w.WriteHeader(reportStatus)
	}))
	t.Cleanup(ts.Close)
	return ts, func() []receivedPolicyReport {
		mu.Lock()
		defer mu.Unlock()
		return append([]receivedPolicyReport(nil), got...)
	}
}

func usePolicySigningKey(t *testing.T) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	original := updatePublicKeyBase64
	updatePublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { updatePublicKeyBase64 = original })
}

// A run that declines to touch settings it does not own alone ends in an error
// whose text ends "policy deferred". That is the one outcome an operator has to
// act on per device -- nothing else about it is visible off the machine -- so the
// report names it apart from other failures.
func TestPolicyCheckResult_tellsDeferredFromFailed(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, store.ClientPolicyCheckOK},
		{errors.New(`shared settings with profiles "home" (codex); policy deferred`), store.ClientPolicyCheckDeferred},
		{errors.New(`shared settings with profiles "" (claude), "home" (codex); policy deferred`), store.ClientPolicyCheckDeferred},
		{errors.New(`shared settings with profiles "a" (claude), "b" (codex) and 31 more; policy deferred`), store.ClientPolicyCheckDeferred},
		{errors.New("managed CA was edited; policy deferred"), store.ClientPolicyCheckDeferred},
		{errors.New("managed CA was changed; policy deferred"), store.ClientPolicyCheckDeferred},
		// internal/codexconfig's, as the plan wraps it with the file it was reading.
		{errors.New("config.toml: unmanaged Codex OTEL settings differ from the profile; policy deferred"), store.ClientPolicyCheckDeferred},
		{errors.New("running sync lacks restart metadata; restart it with the current binary first"), store.ClientPolicyCheckFailed},
		{errors.New("current endpoints are not authorized by policy"), store.ClientPolicyCheckFailed},
	} {
		if got := policyCheckResult(tc.err); got != tc.want {
			t.Errorf("policyCheckResult(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// The classification above reads the error text, so every place that defers has
// to keep the wording it relies on. This reads the package source for them: a new
// deferral spelled differently would otherwise reach the server as a plain
// failure, among the transient ones, and never be looked at.
func TestPolicyDeferralMessagesKeepTheSuffixTheReportReads(t *testing.T) {
	t.Parallel()
	// This package and internal/codexconfig: every place a policy run declines.
	var files []string
	for _, pattern := range []string{"*.go", filepath.Join("..", "..", "internal", "codexconfig", "*.go")} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	literal := regexp.MustCompile(`"([^"\n]*(?i:deferred)[^"\n]*)"`)
	found := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue // prose about deferrals, not a message
			}
			for _, m := range literal.FindAllStringSubmatch(line, -1) {
				text := m[1]
				if text == policyDeferredSuffix {
					continue // the constant itself
				}
				found++
				if !strings.HasSuffix(text, policyDeferredSuffix) {
					t.Errorf("%s: %q mentions a deferral but does not end with %q", file, text, policyDeferredSuffix)
				}
			}
		}
	}
	if found < 5 {
		t.Fatalf("found %d deferral messages, want at least the 5 known ones; the scan no longer sees them", found)
	}
}

func TestBuildPolicyReport_describesAnUntouchedProfile(t *testing.T) {
	// Given: a profile no policy has ever reached.
	useTempSyncHome(t)
	p := policyReportProfile(t, "work", "http://cctrace.company.example:4318")
	target, err := serverPolicyTarget("work")
	if err != nil {
		t.Fatal(err)
	}

	// When
	report, _, err := buildPolicyReport("work", nil)

	// Then: it is a target a policy can name, with the endpoints a `from` list needs.
	if err != nil {
		t.Fatalf("buildPolicyReport: %v", err)
	}
	want := store.ClientPolicyReport{
		ProfileEmail: "alice@example.com", UserID: "alice",
		InstallationID: target.InstallationID, Profile: "work",
		SyncEndpoint: policyEndpoints(p).Sync, OTELEndpoint: policyEndpoints(p).OTEL, Protocol: "grpc",
		CheckResult: store.ClientPolicyCheckOK,
	}
	if report != want {
		t.Fatalf("report = %+v, want %+v", report, want)
	}
}

func TestBuildPolicyReport_afterApplyCarriesWhatTheProfileUsesNow(t *testing.T) {
	// Given: a policy that moves the default profile to new endpoints.
	policy, _ := policyStateFixture(t, false)
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// When
	report, _, err := buildPolicyReport("", nil)

	// Then: the report is read after the run, so it shows the new endpoints and
	// the sequence that put them there. The default profile reports an empty name.
	if err != nil {
		t.Fatalf("buildPolicyReport: %v", err)
	}
	if report.SyncEndpoint != policy.To.Sync || report.OTELEndpoint != policy.To.OTEL || report.Protocol != policy.To.Protocol {
		t.Errorf("endpoints = %q %q %q, want the policy's destination", report.SyncEndpoint, report.OTELEndpoint, report.Protocol)
	}
	if report.AppliedSequence != policy.Sequence || report.CheckResult != store.ClientPolicyCheckOK || report.CheckSequence != 0 || report.CheckReason != "" {
		t.Errorf("state = %+v, want applied %d, ok, no failed attempt", report, policy.Sequence)
	}
	if report.Profile != "" || report.InstallationID == "" {
		t.Errorf("target = (%q, %q), want the default profile with an installation id", report.InstallationID, report.Profile)
	}
}

// An applied policy can carry a note on what it left alone (the default Codex
// config). The run is a success, so the report says ok -- and carries the note,
// which the server keeps for any result; it is how the operator sees it.
func TestBuildPolicyReport_okCarriesTheNoteOfAnAppliedPolicy(t *testing.T) {
	// Given
	useTempSyncHome(t)
	policyReportProfile(t, "", "http://cctrace.company.example:4318")
	statePath, err := policyPath("", "server-policy-state.json")
	if err != nil {
		t.Fatal(err)
	}
	const note = "default Codex config kept: /home/user/.codex/config.toml"
	state := serverPolicyState{}
	state.Sequence = 3
	appendPolicyRecord(&state, 3, "applied", note)
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, raw, 0600); err != nil {
		t.Fatal(err)
	}

	// When
	report, _, err := buildPolicyReport("", nil)

	// Then
	if err != nil {
		t.Fatalf("buildPolicyReport: %v", err)
	}
	if report.CheckResult != store.ClientPolicyCheckOK || report.AppliedSequence != 3 || report.CheckReason != note {
		t.Errorf("report = %+v, want ok at sequence 3 with the note", report)
	}

	// And: a failed run reports its own failure, not the note.
	report, _, err = buildPolicyReport("", errors.New("this run's failure"))
	if err != nil {
		t.Fatalf("buildPolicyReport: %v", err)
	}
	if report.CheckReason != "this run's failure" {
		t.Errorf("reason = %q, want this run's failure", report.CheckReason)
	}
}

func TestBuildPolicyReport_namesThePolicyAFailedRunWasWorkingOn(t *testing.T) {
	// Given: a policy the device fetches but fails to apply.
	policy, p := policyStateFixture(t, false)
	original := applyServerPolicyChangesFn
	t.Cleanup(func() { applyServerPolicyChangesFn = original })
	applyServerPolicyChangesFn = func(string, []configtxn.Change) error {
		return errors.New(`shared settings with profiles "home" (codex); policy deferred`)
	}
	_, runErr := runServerPolicy(context.Background(), "", false)
	if runErr == nil {
		t.Fatal("apply succeeded; the fixture no longer fails")
	}

	// When
	report, _, err := buildPolicyReport("", runErr)

	// Then: applied_sequence did not move, and the attempt is named separately --
	// without it "failed at 0" cannot be told from "never saw a policy".
	if err != nil {
		t.Fatalf("buildPolicyReport: %v", err)
	}
	if report.AppliedSequence != 0 || report.CheckSequence != policy.Sequence {
		t.Errorf("applied = %d, attempted = %d; want 0 and %d", report.AppliedSequence, report.CheckSequence, policy.Sequence)
	}
	if report.CheckResult != store.ClientPolicyCheckDeferred || report.CheckReason != runErr.Error() {
		t.Errorf("result = %q, reason = %q; want deferred with the run's error", report.CheckResult, report.CheckReason)
	}
	if report.SyncEndpoint != policyEndpoints(p).Sync {
		t.Errorf("sync endpoint = %q, want the unchanged %q", report.SyncEndpoint, policyEndpoints(p).Sync)
	}
}

// A device whose state file cannot be read is still a policy target, and the
// operator is better served by a row that says so than by no row: an absent row
// reads as "too old to report". The run itself fails on that file first, with a
// bare decode error, so the report has to add what it was that could not be read.
func TestBuildPolicyReport_reportsAnUnreadableStateInsteadOfGoingSilent(t *testing.T) {
	// Given
	useTempSyncHome(t)
	usePolicySigningKey(t)
	policyReportProfile(t, "", "http://cctrace.company.example:4318")
	statePath, err := policyPath("", "server-policy-state.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	_, runErr := runServerPolicy(context.Background(), "", false)
	if runErr == nil {
		t.Fatal("apply succeeded over an unreadable state file; the fixture no longer fails")
	}

	// When
	report, _, err := buildPolicyReport("", runErr)

	// Then
	if err != nil {
		t.Fatalf("buildPolicyReport: %v", err)
	}
	if report.InstallationID == "" || report.SyncEndpoint == "" {
		t.Errorf("report lost the target or its endpoints: %+v", report)
	}
	if report.CheckResult != store.ClientPolicyCheckFailed || !strings.HasPrefix(report.CheckReason, "read policy state: ") {
		t.Errorf("result = %q, reason = %q; want failed, naming the state file as what could not be read", report.CheckResult, report.CheckReason)
	}
}

// The failed attempt's sequence is taken from the last history record only when
// that record is this run's. A run that fails before it can record anything --
// recovery pending, the state file unwritable -- must not borrow the number of an
// older failure.
func TestBuildPolicyReport_doesNotBorrowAnEarlierFailuresSequence(t *testing.T) {
	// Given: a state whose last record is some earlier, different failure.
	useTempSyncHome(t)
	policyReportProfile(t, "", "http://cctrace.company.example:4318")
	statePath, err := policyPath("", "server-policy-state.json")
	if err != nil {
		t.Fatal(err)
	}
	state := serverPolicyState{Sequence: 2, Status: "failed", Reason: "an earlier failure"}
	appendPolicyRecord(&state, 5, "failed", "an earlier failure")
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, raw, 0600); err != nil {
		t.Fatal(err)
	}

	// When
	report, _, err := buildPolicyReport("", errors.New("this run's failure"))

	// Then
	if err != nil {
		t.Fatalf("buildPolicyReport: %v", err)
	}
	if report.AppliedSequence != 2 || report.CheckSequence != 0 || report.CheckReason != "this run's failure" {
		t.Errorf("report = %+v, want applied 2, no borrowed sequence, this run's reason", report)
	}
}

// A daemon started with --profile-email files its uploads under that account. The
// report names the same one; otherwise the operator's list shows the syncing
// account as silent and the reporting account as one that never synced.
func TestBuildPolicyReport_namesTheAccountTheRunningDaemonSyncsAs(t *testing.T) {
	// Given
	useTempSyncHome(t)
	policyReportProfile(t, "", "http://cctrace.company.example:4318")
	if err := writeJSONFile(runtimeFilePath(""), &syncRuntime{ProfileEmail: "legacy@example.com"}); err != nil {
		t.Fatal(err)
	}

	// When
	report, _, err := buildPolicyReport("", nil)

	// Then
	if err != nil {
		t.Fatalf("buildPolicyReport: %v", err)
	}
	if report.ProfileEmail != "legacy@example.com" {
		t.Errorf("profile_email = %q, want the daemon's legacy@example.com over the profile's own", report.ProfileEmail)
	}
}

// The route's body limit is sized for a well-formed report. An error message long
// enough to pass it would cost the whole report, so the client cuts the reason to
// what the server keeps anyway -- on runes, as the server does.
func TestBuildPolicyReport_cutsAnOverlongReason(t *testing.T) {
	// Given
	useTempSyncHome(t)
	policyReportProfile(t, "", "http://cctrace.company.example:4318")

	// When
	report, _, err := buildPolicyReport("", errors.New(strings.Repeat("가", store.ClientPolicyReasonMax+100)))

	// Then
	if err != nil {
		t.Fatalf("buildPolicyReport: %v", err)
	}
	if n := len([]rune(report.CheckReason)); n != store.ClientPolicyReasonMax {
		t.Errorf("reason is %d runes, want %d", n, store.ClientPolicyReasonMax)
	}
}

func TestReportServerPolicy_sendsAsTheProfileItDescribes(t *testing.T) {
	// Given
	useTempSyncHome(t)
	ts, got := policyReportServer(t, http.StatusNoContent, http.StatusNoContent)
	policyReportProfile(t, "work", ts.URL)

	// When
	reportServerPolicy(context.Background(), "work", errors.New("managed CA was edited; policy deferred"))

	// Then
	if len(got()) != 1 {
		t.Fatalf("reports received = %d, want 1", len(got()))
	}
	r := got()[0]
	if r.auth != "Bearer cli-token" {
		t.Errorf("Authorization = %q, want the profile's token", r.auth)
	}
	if r.body.Profile != "work" || r.body.ProfileEmail != "alice@example.com" || r.body.SyncEndpoint != ts.URL {
		t.Errorf("body = %+v, want profile work, its account and its endpoint", r.body)
	}
	if r.body.CheckResult != store.ClientPolicyCheckDeferred || r.body.CheckReason != "managed CA was edited; policy deferred" {
		t.Errorf("body = %+v, want the deferred run", r.body)
	}
}

// The report is a courtesy to the operator: a profile that is not logged in has
// nothing to authenticate it with, and one that does not exist has nothing to
// describe. (That a server refusing the report leaves the command's result
// alone is TestPolicyCommand_reportsAfterApplyOnly's to show.)
func TestReportServerPolicy_neverGetsInTheWay(t *testing.T) {
	t.Run("not logged in: nothing is sent", func(t *testing.T) {
		// Given
		useTempSyncHome(t)
		ts, got := policyReportServer(t, http.StatusNoContent, http.StatusNoContent)
		p := policyReportProfile(t, "", ts.URL)
		p.Server.AuthToken = ""
		if err := profile.Save(p); err != nil {
			t.Fatal(err)
		}

		// When
		reportServerPolicy(context.Background(), "", nil)

		// Then
		if len(got()) != 0 {
			t.Fatalf("reports received = %d from a profile without a token", len(got()))
		}
	})
	t.Run("no profile: nothing is sent", func(t *testing.T) {
		// Given: a home with a profile under another name only.
		useTempSyncHome(t)
		ts, got := policyReportServer(t, http.StatusNoContent, http.StatusNoContent)
		policyReportProfile(t, "work", ts.URL)

		// When: the default profile, which does not exist, is reported.
		reportServerPolicy(context.Background(), "", nil)

		// Then
		if len(got()) != 0 {
			t.Fatalf("reports received = %d for a profile that does not exist", len(got()))
		}
	})
}

// `apply` reports, whatever it found; `check` and `status` do not. A check is the
// read-only way to look, and one that phones home would not be.
func TestPolicyCommand_reportsAfterApplyOnly(t *testing.T) {
	for _, tc := range []struct {
		name         string
		args         []string
		policyStatus int
		reportStatus int
		wantReports  int
		wantErr      bool
	}{
		{"apply with no policy for this target", []string{"apply", "--automatic"}, http.StatusNoContent, http.StatusNoContent, 1, false},
		{"apply that fails still reports, and still fails", []string{"apply", "--automatic"}, http.StatusInternalServerError, http.StatusNoContent, 1, true},
		{"apply succeeds though the server refuses the report", []string{"apply", "--automatic"}, http.StatusNoContent, http.StatusInternalServerError, 1, false},
		{"check", []string{"check"}, http.StatusNoContent, http.StatusNoContent, 0, false},
		{"status", []string{"status"}, http.StatusNoContent, http.StatusNoContent, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			useTempSyncHome(t)
			usePolicySigningKey(t)
			ts, got := policyReportServer(t, tc.policyStatus, tc.reportStatus)
			policyReportProfile(t, "", ts.URL)
			name := ""
			cmd := serverPolicyCmd(&name)
			cmd.SetArgs(tc.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true

			// When
			err := cmd.Execute()

			// Then
			if (err != nil) != tc.wantErr {
				t.Fatalf("command error = %v, want error: %v", err, tc.wantErr)
			}
			if len(got()) != tc.wantReports {
				t.Fatalf("reports received = %d, want %d", len(got()), tc.wantReports)
			}
			if tc.wantReports == 1 {
				wantResult := store.ClientPolicyCheckOK
				if tc.wantErr {
					wantResult = store.ClientPolicyCheckFailed
				}
				if r := got()[0].body; r.CheckResult != wantResult || r.Profile != "" || r.InstallationID == "" {
					t.Errorf("report = %+v, want %s from the default profile", r, wantResult)
				}
			}
		})
	}
}
