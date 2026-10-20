// Package serverpolicy verifies independently signed, narrowly targeted server migrations.
package serverpolicy

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const MaxBundleBytes = 256 << 10

// A client lists the policy features it applies in the policy request's
// CapabilityParam query parameter. The server answers a policy for all targets
// only to a client listing CapabilityAllTargets: clients that predate it would
// reject the field and report a failure every run.
const (
	CapabilityParam      = "accept"
	CapabilityAllTargets = "all_targets"
)
const signatureDomain = "cctrace-server-policy-v1\n"

type Endpoints struct {
	Sync     string `json:"sync"`
	OTEL     string `json:"otel"`
	Protocol string `json:"protocol"`
}
type Target struct {
	InstallationID string `json:"installation_id"`
	Profile        string `json:"profile"`
}
type Policy struct {
	Schema     int       `json:"schema"`
	Sequence   uint64    `json:"sequence"`
	ServerHost string    `json:"server_host"`
	NotBefore  time.Time `json:"not_before"`
	ExpiresAt  time.Time `json:"expires_at"`
	Targets    []Target  `json:"targets"`
	// AllTargets names every installation and profile the server serves. Empty
	// when false, so explicitly targeted policies keep the bytes clients that
	// predate it decode strictly.
	AllTargets bool        `json:"all_targets,omitempty"`
	From       []Endpoints `json:"from"`
	To         Endpoints   `json:"to"`
	CAPEM      string      `json:"ca_pem"`
	CASHA256   string      `json:"ca_sha256"`
}
type envelope struct {
	Policy    json.RawMessage `json:"policy"`
	Signature string          `json:"signature"`
}

func strictJSON(raw []byte, dst any) error {
	if len(raw) > MaxBundleBytes {
		return errors.New("policy exceeds size limit")
	}
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("policy contains trailing JSON")
	}
	return nil
}

// uniqueKeys rejects ambiguous objects before decoding signed data.
func uniqueKeys(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate or invalid JSON key")
			}
			seen[name] = true
			if err := uniqueKeys(dec); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := uniqueKeys(dec); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = dec.Token()
	return err
}

// Validate checks the complete policy before signing or applying it. Validity at
// a particular instant and trust in a signing key are checked by Verify.
func Validate(p Policy) error {
	if p.Schema != 1 || p.Sequence == 0 || p.NotBefore.IsZero() || !p.ExpiresAt.After(p.NotBefore) {
		return errors.New("invalid policy version, sequence or validity interval")
	}
	if p.ServerHost == "" || canonicalHost(p.ServerHost) != p.ServerHost || strings.ContainsAny(p.ServerHost, "/@?#[]") {
		return errors.New("invalid canonical server host")
	}
	if (len(p.Targets) == 0 && !p.AllTargets) || len(p.From) == 0 {
		return errors.New("policy requires targets and previous endpoints")
	}
	if p.AllTargets && len(p.Targets) > 0 {
		return errors.New("policy for all targets lists targets")
	}
	seen := map[Target]bool{}
	for _, target := range p.Targets {
		if strings.TrimSpace(target.InstallationID) == "" || seen[target] {
			return errors.New("empty or duplicate policy target")
		}
		seen[target] = true
	}
	for _, endpoints := range append(append([]Endpoints(nil), p.From...), p.To) {
		if endpoints.Protocol != "grpc" && endpoints.Protocol != "http/protobuf" {
			return errors.New("unsupported OTLP protocol")
		}
		for _, endpoint := range []string{endpoints.Sync, endpoints.OTEL} {
			if err := validateEndpoint(endpoint, p.ServerHost); err != nil {
				return err
			}
		}
	}
	if p.To.Protocol != "http/protobuf" || !strings.HasPrefix(p.To.Sync, "https://") || !strings.HasPrefix(p.To.OTEL, "https://") {
		return errors.New("destination requires HTTPS and http/protobuf")
	}
	sum := sha256.Sum256([]byte(p.CAPEM))
	if p.CASHA256 != hex.EncodeToString(sum[:]) {
		return errors.New("CA digest mismatch")
	}
	remaining := []byte(p.CAPEM)
	count := 0
	for len(bytes.TrimSpace(remaining)) > 0 {
		remaining = bytes.TrimSpace(remaining)
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return errors.New("unexpected CA PEM content")
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) > 0 {
			return errors.New("invalid CA PEM")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid {
			return errors.New("bundle contains non-CA certificate")
		}
		count++
		remaining = rest
	}
	if count == 0 {
		return errors.New("empty CA bundle")
	}
	return nil
}
func canonicalHost(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}
func validateEndpoint(endpoint, host string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(endpoint, "#") || (u.Path != "" && u.Path != "/") || u.RawPath != "" || canonicalHost(u.Hostname()) != host {
		return errors.New("endpoint is outside allowed server origin")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("endpoint requires an explicit valid port")
	}
	return nil
}
func Sign(p Policy, key ed25519.PrivateKey) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid signing key")
	}
	if err := Validate(p); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	result, err := json.Marshal(envelope{Policy: raw, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(signatureDomain), raw...)))})
	if len(result) > MaxBundleBytes {
		return nil, errors.New("policy exceeds size limit")
	}
	return result, err
}
func decode(raw []byte) (envelope, Policy, []byte, error) {
	var e envelope
	var p Policy
	if err := strictJSON(raw, &e); err != nil {
		return e, p, nil, err
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(e.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return e, p, nil, errors.New("invalid policy signature encoding")
	}
	if err := strictJSON(e.Policy, &p); err != nil {
		return e, p, nil, err
	}
	if err := Validate(p); err != nil {
		return e, p, nil, err
	}
	return e, p, signature, nil
}
func Verify(raw []byte, keys []ed25519.PublicKey, now time.Time) (Policy, error) {
	e, p, signature, err := decode(raw)
	if err != nil {
		return Policy{}, err
	}
	valid := false
	for _, key := range keys {
		if len(key) == ed25519.PublicKeySize && ed25519.Verify(key, append([]byte(signatureDomain), e.Policy...), signature) {
			valid = true
			break
		}
	}
	if !valid {
		return Policy{}, errors.New("untrusted policy signature")
	}
	if now.Before(p.NotBefore) || !now.Before(p.ExpiresAt) {
		return Policy{}, errors.New("policy outside validity interval")
	}
	return p, nil
}

// Covers reports whether the policy is meant for target.
func (p Policy) Covers(target Target) bool {
	return p.AllTargets || slices.Contains(p.Targets, target)
}

func Match(p Policy, target Target, current Endpoints) error {
	if !p.Covers(target) {
		return errors.New("policy does not target this installation and profile")
	}
	for _, old := range p.From {
		if old == current {
			return nil
		}
	}
	return errors.New("current endpoints are not authorized by policy")
}

// Handler reloads the signed file on every request; an empty configured path
// disables distribution. Clients independently verify the signature.
func Handler(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if path == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, "policy unavailable", http.StatusServiceUnavailable)
			return
		}
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, MaxBundleBytes+1))
		if err != nil {
			http.Error(w, "policy unavailable", http.StatusServiceUnavailable)
			return
		}
		_, p, _, err := decode(raw)
		if err != nil {
			http.Error(w, "policy unavailable", http.StatusServiceUnavailable)
			return
		}
		if p.AllTargets && !slices.Contains(r.URL.Query()[CapabilityParam], CapabilityAllTargets) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		target := Target{InstallationID: r.URL.Query().Get("installation_id"), Profile: r.URL.Query().Get("profile")}
		if p.Covers(target) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(raw)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
