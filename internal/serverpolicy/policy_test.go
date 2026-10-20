package serverpolicy

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (Policy, ed25519.PrivateKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	digest := sha256.Sum256([]byte(ca))
	return Policy{Schema: 1, Sequence: 1, ServerHost: "localhost", NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Targets: []Target{{InstallationID: "test-install"}}, From: []Endpoints{{Sync: "http://localhost:1234", OTEL: "http://localhost:1235", Protocol: "grpc"}}, To: Endpoints{Sync: "https://localhost:1236", OTEL: "https://localhost:1237", Protocol: "http/protobuf"}, CAPEM: ca, CASHA256: hex.EncodeToString(digest[:])}, key
}
func TestPolicyTrustAndMatch(t *testing.T) {
	t.Parallel()
	p, key := fixture(t)
	raw, err := Sign(p, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(raw, []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Match(got, p.Targets[0], p.From[0]); err != nil {
		t.Fatal(err)
	}
	if err := Match(got, Target{InstallationID: "another"}, p.From[0]); err == nil {
		t.Fatal("accepted other installation")
	}
	if err := Match(got, p.Targets[0], p.To); err == nil {
		t.Fatal("accepted unlisted old tuple")
	}
	if _, err := Verify(raw, nil, time.Now()); err == nil {
		t.Fatal("accepted unknown key")
	}
	if _, err := Verify(raw, []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, p.ExpiresAt); err == nil {
		t.Fatal("accepted expired policy")
	}
	if _, err := Verify(raw, []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, p.NotBefore.Add(-time.Second)); err == nil {
		t.Fatal("accepted future policy")
	}
	altered := strings.Replace(string(raw), "test-install", "evil-install", 1)
	if _, err := Verify([]byte(altered), []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, time.Now()); err == nil {
		t.Fatal("accepted tampering")
	}
}
func TestPolicyRejectsUnsafeInput(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Policy){"schema": func(p *Policy) { p.Schema = 2 }, "sequence": func(p *Policy) { p.Sequence = 0 }, "target": func(p *Policy) { p.Targets = nil }, "empty identity": func(p *Policy) { p.Targets[0].InstallationID = "" }, "digest": func(p *Policy) { p.CASHA256 = strings.Repeat("0", 64) }, "host": func(p *Policy) { p.To.Sync = "https://cctrace.company.example:8443" }, "implicit port": func(p *Policy) { p.To.Sync = "https://localhost" }, "port range": func(p *Policy) { p.To.Sync = "https://localhost:99999" }, "http": func(p *Policy) { p.To.Sync = "http://localhost:1236" }, "userinfo": func(p *Policy) { p.To.Sync = "https://user@localhost:1236" }, "query": func(p *Policy) { p.To.Sync += "?secret" }, "fragment": func(p *Policy) { p.To.Sync += "#frag" }, "path": func(p *Policy) { p.To.Sync += "/api" }, "grpc destination": func(p *Policy) { p.To.Protocol = "grpc" }, "old host": func(p *Policy) { p.From[0].OTEL = "http://cctrace.company.example:4317" }, "empty from": func(p *Policy) { p.From = nil }, "ca garbage": func(p *Policy) { p.CAPEM += "garbage" }, "validity": func(p *Policy) { p.ExpiresAt = p.NotBefore }}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, key := fixture(t)
			change(&p)
			if _, err := Sign(p, key); err == nil {
				t.Fatal("accepted invalid policy")
			}
		})
	}
}
func TestPolicyStrictEnvelope(t *testing.T) {
	t.Parallel()
	p, key := fixture(t)
	raw, _ := Sign(p, key)
	keys := []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}
	for _, bad := range []string{string(raw) + " {}", strings.Replace(string(raw), `{"policy":`, `{"extra":1,"policy":`, 1), strings.Repeat(" ", MaxBundleBytes+1)} {
		if _, err := Verify([]byte(bad), keys, time.Now()); err == nil {
			t.Fatal("accepted malformed envelope")
		}
	}
	body, _ := json.Marshal(p)
	env, _ := json.Marshal(map[string]any{"policy": json.RawMessage(body), "signature": base64.StdEncoding.EncodeToString(ed25519.Sign(key, body))})
	if _, err := Verify(env, keys, time.Now()); err == nil {
		t.Fatal("accepted wrong signature domain")
	}
}
func TestPolicyHandlerSelectionAndReload(t *testing.T) {
	t.Parallel()
	p, key := fixture(t)
	raw, _ := Sign(p, key)
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	h := Handler(path)
	check := func(query string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("GET", "/api/server-policy"+query, nil))
		if w.Code != want {
			t.Fatalf("status %d want %d", w.Code, want)
		}
		if want == 200 && w.Body.String() != string(raw) {
			t.Fatal("changed signed bytes")
		}
	}
	check("?installation_id=test-install", 200)
	check("?installation_id=other", 204)
	check("?installation_id=test-install&profile=named", 204)
	if err := os.WriteFile(path, []byte("garbage"), 0600); err != nil {
		t.Fatal(err)
	}
	check("?installation_id=test-install", 503)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check("?installation_id=test-install", 503)
	w := httptest.NewRecorder()
	Handler("")(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}

func TestPolicyRejectsDuplicateJSONKeys(t *testing.T) {
	t.Parallel()
	p, key := fixture(t)
	raw, _ := Sign(p, key)
	duplicate := strings.Replace(string(raw), `"schema":1`, `"schema":1,"schema":1`, 1)
	if _, err := Verify([]byte(duplicate), []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, time.Now()); err == nil {
		t.Fatal("accepted duplicate policy key")
	}
	// A duplicate envelope key must be rejected even if it repeats valid bytes.
	var e map[string]json.RawMessage
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	duplicate = `{"policy":` + string(e["policy"]) + `,"policy":` + string(e["policy"]) + `,"signature":` + string(e["signature"]) + `}`
	if _, err := Verify([]byte(duplicate), []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, time.Now()); err == nil {
		t.Fatal("accepted duplicate envelope key")
	}
}

func TestPolicyCARotationAndNonCARejection(t *testing.T) {
	t.Parallel()
	p, key := fixture(t)
	second, otherKey := fixture(t)
	p.CAPEM += second.CAPEM
	sum := sha256.Sum256([]byte(p.CAPEM))
	p.CASHA256 = hex.EncodeToString(sum[:])
	raw, err := Sign(p, key)
	if err != nil {
		t.Fatalf("overlapping CA bundle: %v", err)
	}
	if _, err := Verify(raw, []ed25519.PublicKey{otherKey.Public().(ed25519.PublicKey), key.Public().(ed25519.PublicKey)}, time.Now()); err != nil {
		t.Fatalf("rotating signing key: %v", err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	p.CAPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	sum = sha256.Sum256([]byte(p.CAPEM))
	p.CASHA256 = hex.EncodeToString(sum[:])
	if _, err := Sign(p, key); err == nil {
		t.Fatal("accepted leaf certificate as CA")
	}
	p.CAPEM = second.CAPEM + "garbage"
	sum = sha256.Sum256([]byte(p.CAPEM))
	p.CASHA256 = hex.EncodeToString(sum[:])
	if _, err := Sign(p, key); err == nil {
		t.Fatal("accepted non PEM trailing content")
	}
}

func allTargetsFixture(t *testing.T) (Policy, ed25519.PrivateKey) {
	t.Helper()
	p, key := fixture(t)
	p.Targets, p.AllTargets = nil, true
	return p, key
}

// A policy for every profile on the server names no one, and an explicitly
// targeted one still needs its targets.
func TestPolicyAllTargetsValidation(t *testing.T) {
	t.Parallel()
	p, key := allTargetsFixture(t)
	if _, err := Sign(p, key); err != nil {
		t.Fatalf("all targets without a list: %v", err)
	}
	p.Targets = []Target{{InstallationID: "test-install"}}
	if _, err := Sign(p, key); err == nil {
		t.Fatal("accepted all targets with a target list")
	}
	p.Targets, p.AllTargets = nil, false
	if _, err := Sign(p, key); err == nil {
		t.Fatal("accepted no targets without all targets")
	}
}

// Every installation and profile is a target; the endpoints still have to be
// ones the policy moves from.
func TestPolicyAllTargetsMatch(t *testing.T) {
	t.Parallel()
	p, key := allTargetsFixture(t)
	raw, err := Sign(p, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(raw, []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []Target{{InstallationID: "any-install"}, {InstallationID: "other-install", Profile: "work"}} {
		if err := Match(got, target, p.From[0]); err != nil {
			t.Fatalf("%+v: %v", target, err)
		}
	}
	if err := Match(got, Target{InstallationID: "any-install"}, p.To); err == nil {
		t.Fatal("accepted endpoints the policy does not move from")
	}
}

// Clients that predate all targets decode the policy strictly and would fail
// on the field every run, so only a client that says it applies such a policy
// gets one. An explicitly targeted policy is served as before.
func TestPolicyHandlerAllTargetsNeedsCapability(t *testing.T) {
	t.Parallel()
	serve := func(p Policy, key ed25519.PrivateKey) (http.HandlerFunc, []byte) {
		raw, err := Sign(p, key)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "policy.json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return Handler(path), raw
	}
	check := func(h http.HandlerFunc, raw []byte, query string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("GET", "/api/client-config-policy"+query, nil))
		if w.Code != want || (want == 200 && w.Body.String() != string(raw)) {
			t.Fatalf("%s: status %d want %d", query, w.Code, want)
		}
	}
	all, raw := serve(allTargetsFixture(t))
	check(all, raw, "?installation_id=any-install&accept=all_targets", 200)
	check(all, raw, "?installation_id=any-install&profile=work&accept=all_targets", 200)
	check(all, raw, "?installation_id=any-install", 204)
	check(all, raw, "?installation_id=any-install&accept=other", 204)
	explicit, raw := serve(fixture(t))
	check(explicit, raw, "?installation_id=test-install&accept=all_targets", 200)
	check(explicit, raw, "?installation_id=test-install", 200)
	check(explicit, raw, "?installation_id=other&accept=all_targets", 204)
}

// An explicitly targeted policy keeps the bytes clients that predate the field
// verify and decode strictly.
func TestPolicyExplicitTargetsOmitAllTargets(t *testing.T) {
	t.Parallel()
	p, _ := fixture(t)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "all_targets") {
		t.Fatalf("explicit policy carries the field: %s", raw)
	}
}

// policyV0763 is the Policy that v0.7.61 to v0.7.63 clients decode: the current
// one without AllTargets, with Target and Endpoints as they were. Those clients
// never ask for a policy for all targets, so the server withholds it; this copy
// keeps the reason it must. It is frozen: do not edit it to follow Policy.
type policyV0763 struct {
	Schema     int              `json:"schema"`
	Sequence   uint64           `json:"sequence"`
	ServerHost string           `json:"server_host"`
	NotBefore  time.Time        `json:"not_before"`
	ExpiresAt  time.Time        `json:"expires_at"`
	Targets    []targetV0763    `json:"targets"`
	From       []endpointsV0763 `json:"from"`
	To         endpointsV0763   `json:"to"`
	CAPEM      string           `json:"ca_pem"`
	CASHA256   string           `json:"ca_sha256"`
}
type targetV0763 struct {
	InstallationID string `json:"installation_id"`
	Profile        string `json:"profile"`
}
type endpointsV0763 struct {
	Sync     string `json:"sync"`
	OTEL     string `json:"otel"`
	Protocol string `json:"protocol"`
}

// decodeV0763 follows decode as v0.7.63 ran it, up to the strict policy decode.
func decodeV0763(raw []byte) (policyV0763, error) {
	var e envelope
	var p policyV0763
	if err := strictJSON(raw, &e); err != nil {
		return p, err
	}
	err := strictJSON(e.Policy, &p)
	return p, err
}

// A client that predates all targets rejects the signed field, so handing it
// such a policy would fail every run. Explicitly targeted policies still decode.
func TestPolicyV0763_RejectsAllTargets(t *testing.T) {
	t.Parallel()
	p, key := allTargetsFixture(t)
	raw, err := Sign(p, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeV0763(raw); err == nil || !strings.Contains(err.Error(), `unknown field "all_targets"`) {
		t.Fatalf("v0.7.63 decode of a policy for all targets: %v", err)
	}
	explicit, key := fixture(t)
	raw, err = Sign(explicit, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeV0763(raw)
	if err != nil {
		t.Fatalf("v0.7.63 decode of an explicitly targeted policy: %v", err)
	}
	if got.Sequence != explicit.Sequence || len(got.Targets) != 1 || got.Targets[0].InstallationID != explicit.Targets[0].InstallationID {
		t.Fatalf("v0.7.63 decoded %+v from %+v", got, explicit)
	}
}

// The copy above stands for the old shape only while it differs from Policy by
// exactly all_targets. Types are compared by the JSON they decode, field by
// field down to Target and Endpoints, not by Go name.
func TestPolicyV0763_IsPolicyWithoutAllTargets(t *testing.T) {
	t.Parallel()
	var shape func(reflect.Type) string
	shape = func(typ reflect.Type) string {
		switch typ.Kind() {
		case reflect.Struct:
			if typ == reflect.TypeOf(time.Time{}) {
				return typ.String()
			}
			var b strings.Builder
			b.WriteString("{")
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				fmt.Fprintf(&b, " %s %s `%s`;", f.Name, shape(f.Type), f.Tag.Get("json"))
			}
			b.WriteString(" }")
			return b.String()
		case reflect.Slice, reflect.Pointer:
			return "[]" + shape(typ.Elem())
		default:
			return typ.Kind().String()
		}
	}
	fields := func(typ reflect.Type) (out []string) {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			out = append(out, f.Name+" "+shape(f.Type)+" `"+f.Tag.Get("json")+"`")
		}
		return out
	}
	old, current := fields(reflect.TypeOf(policyV0763{})), fields(reflect.TypeOf(Policy{}))
	const added = "AllTargets bool `all_targets,omitempty`"
	i := slices.Index(current, added)
	if i < 0 {
		t.Fatalf("Policy has no %q: %v", added, current)
	}
	current = slices.Delete(current, i, i+1)
	if !slices.Equal(old, current) {
		t.Fatalf("the v0.7.61-63 shape is frozen: do not edit policyV0763 to match. "+
			"Make a new Policy field omitempty and withhold it from clients that do not ask for it, as all_targets is.\nfrozen:  %v\ncurrent: %v", old, current)
	}
}
