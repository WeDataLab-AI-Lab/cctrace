//go:build darwin || linux

package desktop

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"cctrace/internal/profile"
)

func trustService(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return &Service{registry: &profile.Registry{Root: root}}
}
func trustProfile(t *testing.T, s *Service, name, endpoint, ca string) string {
	t.Helper()
	p := &profile.Profile{Server: profile.ServerInfo{SyncEndpoint: endpoint, CACertFile: ca, AuthToken: "secret-token"}}
	path, err := s.registry.Path(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func trustCert(t *testing.T, serial int64) []byte {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "synthetic CA"}, NotBefore: time.Unix(0, 0), NotAfter: time.Unix(2000000000, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, c, c, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
func trustFile(t *testing.T, data []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	return path
}
func trustRead(s *Service, origin string) (any, *ErrorObject) {
	raw, _ := json.Marshal(map[string]string{"origin": origin})
	return s.updateTrust(raw)
}

func TestUpdateTrustMergeAndIsolation(t *testing.T) {
	t.Parallel()
	s := trustService(t)
	a, b := trustCert(t, 1), trustCert(t, 2)
	trustProfile(t, s, "default", "https://EXAMPLE.invalid:443", trustFile(t, append(append([]byte{}, b...), a...), 0644))
	path := trustProfile(t, s, "paused", "https://example.invalid", trustFile(t, a, 0600))
	before, _ := os.ReadFile(path)
	trustProfile(t, s, "unrelated", "https://else.invalid", filepath.Join(t.TempDir(), "absent"))
	result, e := trustRead(s, "https://example.invalid")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(result)
	var got struct {
		Origin string `json:"origin"`
		PEM    string `json:"ca_pem"`
		SHA    string `json:"ca_sha256"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	ders := [][]byte{}
	for _, p := range [][]byte{a, b} {
		block, _ := pem.Decode(p)
		ders = append(ders, block.Bytes)
	}
	sort.Slice(ders, func(i, j int) bool { return bytes.Compare(ders[i], ders[j]) < 0 })
	var want []byte
	for _, der := range ders {
		want = append(want, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	sum := sha256.Sum256(want)
	if got.Origin != "https://example.invalid:443" || got.PEM != string(want) || got.SHA != hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected result: %s", raw)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("profile changed")
	}
	if strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), s.registry.Root) {
		t.Fatal("private metadata leaked")
	}
}
func TestUpdateTrustSystemAndMissing(t *testing.T) {
	t.Parallel()
	s := trustService(t)
	if _, e := trustRead(s, "https://missing.invalid"); e == nil || e.Code != "trust_not_configured" {
		t.Fatalf("missing: %+v", e)
	}
	trustProfile(t, s, "default", "https://system.invalid", "")
	result, e := trustRead(s, "https://system.invalid:443")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(result)
	emptySum := sha256.Sum256(nil)
	if !bytes.Contains(raw, []byte(`"ca_pem":""`)) || !bytes.Contains(raw, []byte(hex.EncodeToString(emptySum[:]))) {
		t.Fatalf("system: %s", raw)
	}
}
func TestUpdateTrustRejectPayload(t *testing.T) {
	t.Parallel()
	s := trustService(t)
	for _, raw := range []string{`null`, `{}`, `{"origin":"https://updates.invalid","path":"x"}`, `{"origin":"https://updates.invalid","origin":"https://other.invalid"}`, `{"origin":null}`, `{"origin":"https://updates.invalid"} {}`} {
		if _, e := s.updateTrust(json.RawMessage(raw)); e == nil || e.Code != "invalid_payload" {
			t.Errorf("accepted %s: %+v", raw, e)
		}
	}
	for _, origin := range []string{"http://updates.invalid", "https://user:pass@updates.invalid", "https://updates.invalid/", "https://updates.invalid/x", "https://updates.invalid?", "https://updates.invalid#", "https://updates.invalid:0", "https://updates.invalid:65536", "https://updates.invalid:", "https://-bad", "https://bad..invalid", "https://[foo]", "https://updates.invalid%20b", "https://[fe80::1%25en0]", "https://updates.invalid\\b", "https://[foo]"} {
		if _, e := trustRead(s, origin); e == nil || e.Code != "invalid_payload" {
			t.Errorf("accepted %q: %+v", origin, e)
		}
	}
}
func TestUpdateTrustFailClosed(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing", "malformed", "empty", "oversized", "directory", "symlink", "key", "junk", "invalid-der", "skipped-block", "whitespace", "unreadable", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s := trustService(t)
			ca := trustFile(t, trustCert(t, 3), 0644)
			switch kind {
			case "unreadable":
				os.Chmod(ca, 0000)
			case "fifo":
				os.Remove(ca)
				if err := syscall.Mkfifo(ca, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				os.Remove(ca)
			case "malformed":
				os.WriteFile(ca, []byte("bad"), 0600)
			case "empty":
				os.WriteFile(ca, nil, 0600)
			case "oversized":
				os.WriteFile(ca, bytes.Repeat([]byte("x"), 256*1024+1), 0600)
			case "directory":
				ca = t.TempDir()
			case "symlink":
				target := ca
				ca = filepath.Join(t.TempDir(), "link")
				os.Symlink(target, ca)
			case "key":
				f, _ := os.OpenFile(ca, os.O_APPEND|os.O_WRONLY, 0600)
				f.Write(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret")}))
				f.Close()
			case "junk":
				f, _ := os.OpenFile(ca, os.O_APPEND|os.O_WRONLY, 0600)
				f.WriteString("junk")
				f.Close()
			case "whitespace":
				os.WriteFile(ca, []byte("  \n"), 0600)
			case "skipped-block":
				valid, _ := os.ReadFile(ca)
				os.WriteFile(ca, append([]byte("-----BEGIN CERTIFICATE-----\nbad\n-----END CERTIFICATE-----\n"), valid...), 0600)
			case "invalid-der":
				os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("bad")}), 0600)
			}
			trustProfile(t, s, "default", "https://fail.invalid", ca)
			trustProfile(t, s, "system", "https://fail.invalid", "")
			result, e := trustRead(s, "https://fail.invalid")
			if result != nil || e == nil || e.Code != "trust_unavailable" {
				t.Fatalf("%v %+v", result, e)
			}
			raw, _ := json.Marshal(e)
			if bytes.Contains(raw, []byte(ca)) || bytes.Contains(raw, []byte("secret")) {
				t.Fatal("error leaked private data")
			}
		})
	}
}

func TestUpdateTrustEndpointPrecedenceAndCanonical(t *testing.T) {
	t.Parallel()
	s := trustService(t)
	path := trustProfile(t, s, "default", "https://sync.invalid:8443", "")
	raw, _ := os.ReadFile(path)
	var p profile.Profile
	json.Unmarshal(raw, &p)
	p.Server.Endpoint = "https://fallback.invalid:9443/api"
	write := func() {
		data, _ := json.Marshal(p)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if _, e := trustRead(s, "https://fallback.invalid:9443"); e == nil || e.Code != "trust_not_configured" {
		t.Fatalf("fallback despite sync: %+v", e)
	}
	p.Server.SyncEndpoint = ""
	write()
	if _, e := trustRead(s, "https://fallback.invalid:9443"); e != nil {
		t.Fatal(e)
	}
	if _, e := trustRead(s, "https://fallback.invalid"); e == nil || e.Code != "trust_not_configured" {
		t.Fatalf("port inferred: %+v", e)
	}
	p.RegistrationOnly = true
	p.Server = profile.ServerInfo{}
	write()
	if _, e := trustRead(s, "https://fallback.invalid:9443"); e == nil || e.Code != "trust_not_configured" {
		t.Fatalf("registration mapping: %+v", e)
	}
	for input, want := range map[string]string{"https://[2001:0db8::1]": "https://[2001:db8::1]:443", "https://EXAMPLE.invalid:08443": "https://example.invalid:8443"} {
		got, err := updateTrustOrigin(input)
		if err != nil || got != want {
			t.Fatalf("canonical %q %q %v", input, got, err)
		}
	}
}

func TestUpdateTrustAggregateLimitAndUnsafeRegistry(t *testing.T) {
	t.Parallel()
	s := trustService(t)
	cert := trustCert(t, 8)
	// Each file is valid and under budget, but all matching input exceeds it.
	data := bytes.Repeat(cert, (130*1024)/len(cert)+1)
	ca := trustFile(t, data, 0644)
	trustProfile(t, s, "default", "https://large.invalid", ca)
	trustProfile(t, s, "second", "https://large.invalid", ca)
	if _, e := trustRead(s, "https://large.invalid"); e == nil || e.Code != "trust_unavailable" {
		t.Fatalf("aggregate: %+v", e)
	}
	if err := os.Chmod(s.registry.Root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, e := trustRead(s, "https://large.invalid"); e == nil || e.Code != "trust_unavailable" {
		t.Fatalf("registry: %+v", e)
	}
}

func TestUpdateTrustManagedCertificateAndResponseBound(t *testing.T) {
	t.Parallel()
	s := trustService(t)
	path := trustProfile(t, s, "managed", "https://managed.invalid", "")
	ca := filepath.Join(filepath.Dir(path), "server-ca.pem")
	cert := trustCert(t, 10)
	if err := os.WriteFile(ca, cert, 0644); err != nil {
		t.Fatal(err)
	}
	trustProfile(t, s, "managed", "https://managed.invalid", ca)
	result, e := trustRead(s, "https://managed.invalid")
	if e != nil {
		t.Fatal(e)
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) >= maxResponseBytes {
		t.Fatalf("response %d %v", len(data), err)
	}
	// Reading trust must not create migration metadata, locks, or desktop state.
	entries, err := os.ReadDir(s.registry.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "profiles" {
		t.Fatalf("unexpected writes: %v", entries)
	}
}

// Exercise the open itself, bypassing lstat, to cover objects substituted during
// the lstat/open window. In particular a FIFO open must not wait for a writer.
func TestUpdateTrustOpenReplacementSafety(t *testing.T) {
	t.Parallel()
	target := trustFile(t, trustCert(t, 11), 0600)
	link := filepath.Join(t.TempDir(), "replacement")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if f, err := openUpdateTrustCA(link); err == nil {
		f.Close()
		t.Fatal("open followed replacement symlink")
	}
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := openUpdateTrustCA(fifo)
	if err != nil {
		return
	} // An OS rejection is also safe and nonblocking.
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().IsRegular() {
		t.Fatal("replacement FIFO passed regular-file check")
	}
}

func TestUpdateTrustEndpointPathAssociation(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"valid", "missing", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s := trustService(t)
			cert := trustCert(t, 20)
			ca := trustFile(t, cert, 0600)
			switch kind {
			case "missing":
				if err := os.Remove(ca); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(ca, []byte("not a certificate"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			endpoint := "https://updates.invalid:8443/api"
			if kind == "valid" {
				endpoint = "https://UPDATES.invalid:08443/api/v1/"
			}
			trustProfile(t, s, "default", endpoint, ca)
			result, e := trustRead(s, "https://updates.invalid:8443")
			if kind != "valid" {
				if result != nil || e == nil || e.Code != "trust_unavailable" {
					t.Fatalf("%s prefixed endpoint: result=%v error=%+v; want trust_unavailable", kind, result, e)
				}
				return
			}
			if e != nil {
				t.Fatalf("valid prefixed endpoint: %+v", e)
			}
			got := result.(updateTrustResult)
			if got.Origin != "https://updates.invalid:8443" || got.CAPEM != string(cert) {
				t.Fatalf("unexpected prefixed endpoint trust: %+v", got)
			}
			sum := sha256.Sum256(cert)
			if got.CASHA256 != hex.EncodeToString(sum[:]) {
				t.Fatal("prefixed endpoint changed certificate digest")
			}
		})
	}
}

func TestUpdateTrustMatchingMalformedEndpointFailsClosed(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{
		"https://user:password@updates.invalid:8443/api",
		"https://user%zz@updates.invalid:8443/api",
		"https://updates.invalid:8443/api%zz",
		"https://updates.invalid:8443/api?query=value",
		"https://updates.invalid:8443/api?",
		"https://updates.invalid:8443/api#fragment",
		"https://updates.invalid:8443/api#",
		"https://updates.invalid:8443/api#%zz",
	} {
		t.Run(endpoint, func(t *testing.T) {
			t.Parallel()
			s := trustService(t)
			trustProfile(t, s, "default", endpoint, "")
			result, e := trustRead(s, "https://updates.invalid:8443")
			if result != nil || e == nil || e.Code != "trust_unavailable" {
				t.Fatalf("matching malformed endpoint: result=%v error=%+v; want trust_unavailable", result, e)
			}
		})
	}
}

func TestUpdateTrustUnrelatedEndpointPathsIgnored(t *testing.T) {
	t.Parallel()
	s := trustService(t)
	trustProfile(t, s, "default", "https://updates.invalid:8443/api", "")
	missing := filepath.Join(t.TempDir(), "missing-ca.pem")
	for name, endpoint := range map[string]string{
		"other-host":      "https://else.invalid:8443/api",
		"other-port":      "https://updates.invalid:9443/api",
		"other-malformed": "https://user:password@else.invalid:8443/api?query=value#fragment",
	} {
		trustProfile(t, s, name, endpoint, missing)
	}
	result, e := trustRead(s, "https://updates.invalid:8443")
	if e != nil {
		t.Fatalf("unrelated endpoint changed matching system trust: %+v", e)
	}
	if got := result.(updateTrustResult); got.CAPEM != "" {
		t.Fatalf("unexpected unrelated CA: %+v", got)
	}
}
