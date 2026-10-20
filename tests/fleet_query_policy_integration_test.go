package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"cctrace/internal/serverpolicy"
	"cctrace/internal/store"
)

// fleetQuerySQL returns the SQL of one named scripts/fleet-query.sh query, with
// psql's own backslash commands dropped so it can run over an ordinary connection.
func fleetQuerySQL(t *testing.T, name string) string {
	t.Helper()
	script := filepath.Join(rootDir(t), "scripts", "fleet-query.sh")
	out, err := exec.Command("bash", script, "--print-sql", "--days", "7", name).Output()
	if err != nil {
		t.Fatalf("fleet-query.sh --print-sql %s: %v", name, err)
	}
	var sql []string
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, `\`) {
			sql = append(sql, line)
		}
	}
	return strings.Join(sql, "\n")
}

// The policy-* queries are what an operator writes a policy from, so they run
// here against a migrated database rather than being read and believed: a column
// renamed in the table, or a join that drops the accounts that have not
// reported, would otherwise surface while someone is preparing a rollout.
func TestFleetQueryPolicyQueries_listTargetsAndWhatAPolicyNeeds(t *testing.T) {
	// tests/ is exported to the public mirror and scripts/fleet-query.sh is not:
	// the script queries our own deployment. Its absence is legitimate only
	// there, and "there" is identified the way update_publickey_test does it --
	// by deploy/push.sh, the release path, which is never exported either.
	if _, err := os.Stat(filepath.Join(rootDir(t), "scripts", "fleet-query.sh")); os.IsNotExist(err) {
		if _, statErr := os.Stat(filepath.Join(rootDir(t), "deploy", "push.sh")); statErr == nil {
			t.Fatal("scripts/fleet-query.sh is missing but deploy/push.sh is present: a broken internal checkout")
		}
		t.Skip("no scripts/fleet-query.sh and no deploy/push.sh: mirror checkout, which does not carry the fleet queries")
	}
	// Given: one account that has moved its default profile and is deferred on a
	// second one, one that has not reported at all, and a target last seen a month
	// ago.
	s := acquireSyncStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, "TRUNCATE client_policy_reports, client_versions"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for _, v := range []store.ClientVersionUpdate{
		{UserID: "alice", ProfileEmail: "alice@example.com", ClientVersion: "v0.7.62"},
		{UserID: "legacy", ProfileEmail: "legacy@example.com", ClientVersion: "v0.7.30"},
	} {
		if err := s.UpsertClientVersion(ctx, v); err != nil {
			t.Fatalf("seed client version: %v", err)
		}
	}
	device, gone := uuid.NewString(), uuid.NewString()
	plain := serverpolicy.Endpoints{Sync: "http://cctrace.company.example:4318", OTEL: "http://cctrace.company.example:4317", Protocol: "grpc"}
	secure := serverpolicy.Endpoints{Sync: "https://cctrace.company.example:8443", OTEL: "https://cctrace.company.example:4318", Protocol: "http/protobuf"}
	for _, r := range []store.ClientPolicyReport{
		{ProfileEmail: "alice@example.com", InstallationID: device, Profile: "", SyncEndpoint: secure.Sync, OTELEndpoint: secure.OTEL, Protocol: secure.Protocol, AppliedSequence: 1, CheckResult: store.ClientPolicyCheckOK, ClientVersion: "v0.7.62"},
		{ProfileEmail: "alice@example.com", InstallationID: device, Profile: "work", SyncEndpoint: plain.Sync, OTELEndpoint: plain.OTEL, Protocol: plain.Protocol, CheckResult: store.ClientPolicyCheckDeferred, CheckSequence: 1, CheckReason: `shared Codex settings with profile ""; policy deferred`, ClientVersion: "v0.7.64"},
		{ProfileEmail: "alice@example.com", InstallationID: gone, Profile: "old", SyncEndpoint: plain.Sync, OTELEndpoint: plain.OTEL, Protocol: plain.Protocol, CheckResult: store.ClientPolicyCheckOK},
	} {
		if err := s.UpsertClientPolicyReport(ctx, r); err != nil {
			t.Fatalf("seed report: %v", err)
		}
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE client_policy_reports SET last_seen_at = now() - interval '30 days' WHERE installation_id = $1`, gone); err != nil {
		t.Fatalf("age report: %v", err)
	}

	// When / Then: policy-targets shows both live targets with their state, and the
	// account without a report rather than leaving it out.
	rows, err := s.Pool().Query(ctx, fleetQuerySQL(t, "policy-targets"))
	if err != nil {
		t.Fatalf("policy-targets: %v", err)
	}
	type target struct{ email, id, profile, result string }
	var targets []target
	for rows.Next() {
		v, err := rows.Values()
		if err != nil {
			t.Fatalf("policy-targets row: %v", err)
		}
		result, _ := v[7].(string)
		targets = append(targets, target{v[0].(string), v[1].(string), v[2].(string), result})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("policy-targets rows: %v", err)
	}
	wantTargets := []target{
		{"alice@example.com", device, "", "ok"},
		{"alice@example.com", device, "work", "deferred"},
		{"legacy@example.com", "(보고없음)", "", ""},
	}
	if len(targets) != len(wantTargets) {
		t.Fatalf("policy-targets = %+v, want %+v (the month-old target must be outside --days 7)", targets, wantTargets)
	}
	for i, want := range wantTargets {
		if targets[i] != want {
			t.Errorf("policy-targets[%d] = %+v, want %+v", i, targets[i], want)
		}
	}

	// policy-from counts each endpoint combination, and how many have applied.
	rows, err = s.Pool().Query(ctx, fleetQuerySQL(t, "policy-from"))
	if err != nil {
		t.Fatalf("policy-from: %v", err)
	}
	applied := map[string]int64{}
	for rows.Next() {
		v, err := rows.Values()
		if err != nil {
			t.Fatalf("policy-from row: %v", err)
		}
		if v[3].(int64) != 1 {
			t.Errorf("policy-from: %v has %d targets, want 1", v[0], v[3])
		}
		applied[v[0].(string)] = v[4].(int64)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("policy-from rows: %v", err)
	}
	if len(applied) != 2 || applied[secure.Sync] != 1 || applied[plain.Sync] != 0 {
		t.Errorf("policy-from applied counts = %v, want the secure combination applied once and the plain one not", applied)
	}

	// policy-json decodes as the two lists a policy file carries. Targets are the
	// ones a policy can still move: the one that already applied one stays out,
	// though its combination remains a "from" a later policy must list.
	doc := policyJSON(t, ctx, s)
	if len(doc.Targets) != 1 || doc.Targets[0] != (serverpolicy.Target{InstallationID: device, Profile: "work"}) {
		t.Errorf("policy-json targets = %+v, want only the unapplied v0.7.64 target", doc.Targets)
	}
	seen := map[serverpolicy.Endpoints]bool{}
	for _, e := range doc.From {
		seen[e] = true
	}
	if len(doc.From) != 2 || !seen[plain] || !seen[secure] {
		t.Errorf("policy-json from = %+v, want the plain and the secure combination, spelled as reported", doc.From)
	}

	// Which versions count is a number, not a string: "v0.7.9" sorts after
	// "v0.7.64" and "v0.7.100" before it. A build made after the v0.7.64 tag
	// counts, dirty or not, and one made after v0.7.63 does not. A build that
	// sends no version, a value that is no version number and a number too long
	// to compare are left out.
	if _, err := s.Pool().Exec(ctx, "TRUNCATE client_policy_reports"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	type versioned struct {
		version string
		applied uint64
		listed  bool
	}
	cases := []versioned{
		{"v0.7.64", 0, true},
		{"v0.7.64", 1, false},
		{"v0.7.65", 0, true},
		{"v0.7.100", 0, true},
		{"v0.8.0", 0, true},
		{"v1.0.0", 0, true},
		{"0.7.64", 0, true},
		{"v0.7.64-3-gabc1234-dirty", 0, true},
		{"v0.7.9", 0, false},
		{"v0.7.63", 0, false},
		{"v0.7.63-3-gabc1234", 0, false},
		{"v0.6.99", 0, false},
		{"", 0, false},
		{"dev-abc1234", 0, false},
		{"V0.7.64", 0, false},
		{"v0.7.99999999999", 0, false},
	}
	label := map[string]string{}
	want := map[string]bool{}
	for _, c := range cases {
		id := uuid.NewString()
		r := store.ClientPolicyReport{ProfileEmail: "fleet@example.com", InstallationID: id, SyncEndpoint: plain.Sync, OTELEndpoint: plain.OTEL, Protocol: plain.Protocol, AppliedSequence: c.applied, CheckResult: store.ClientPolicyCheckOK, ClientVersion: c.version}
		if err := s.UpsertClientPolicyReport(ctx, r); err != nil {
			t.Fatalf("seed %q: %v", c.version, err)
		}
		label[id] = fmt.Sprintf("version %q applied %d", c.version, c.applied)
		want[id] = c.listed
	}
	got := map[string]bool{}
	for _, target := range policyJSON(t, ctx, s).Targets {
		got[target.InstallationID] = true
	}
	for id, listed := range want {
		if got[id] != listed {
			t.Errorf("policy-json lists %s = %v, want %v", label[id], got[id], listed)
		}
	}
}

// policyJSON runs the policy-json query and decodes it as a policy file's lists.
func policyJSON(t *testing.T, ctx context.Context, s *store.PgStore) (doc struct {
	Targets []serverpolicy.Target    `json:"targets"`
	From    []serverpolicy.Endpoints `json:"from"`
}) {
	t.Helper()
	var raw string
	if err := s.Pool().QueryRow(ctx, fleetQuerySQL(t, "policy-json")).Scan(&raw); err != nil {
		t.Fatalf("policy-json: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("policy-json is not the shape of a policy's lists: %v\n%s", err, raw)
	}
	return doc
}
