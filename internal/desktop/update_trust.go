package desktop

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Bound both input material and the normalized PEM well below the 1 MiB IPC
// envelope, including JSON escaping. No transport or OS trust changes occur here.
const updateTrustMaxBytes = 256 << 10

type updateTrustResult struct {
	Origin   string `json:"origin"`
	CAPEM    string `json:"ca_pem"`
	CASHA256 string `json:"ca_sha256"`
}

// updateTrust reads existing registry definitions only, including paused profiles.
// A missing association is distinct from a configured system-roots-only server.
// It deliberately does not migrate profiles, probe servers, or consult keychains.
func (s *Service) updateTrust(raw json.RawMessage) (any, *ErrorObject) {
	var payload struct {
		Origin string `json:"origin"`
	}
	if len(raw) > 4096 || decodePayload(raw, &payload) != nil {
		return nil, &ErrorObject{Code: "invalid_payload", Message: "Expected an HTTPS origin."}
	}
	origin, err := updateTrustOrigin(payload.Origin)
	if err != nil {
		return nil, &ErrorObject{Code: "invalid_payload", Message: "Expected an HTTPS origin."}
	}
	unavailable := func() (any, *ErrorObject) {
		return nil, &ErrorObject{Code: "trust_unavailable", Message: "Configured update trust is unavailable."}
	}
	if s.registry == nil {
		return unavailable()
	}
	entries, err := s.registry.List()
	if err != nil {
		return unavailable()
	}
	matched := false
	certificates := map[string][]byte{}
	remaining := updateTrustMaxBytes
	for _, entry := range entries {
		p := entry.Profile
		endpoint := p.Server.SyncEndpoint
		if endpoint == "" {
			endpoint = p.Server.Endpoint
		}
		// CLI API endpoint precedence: an explicit SyncEndpoint always wins.
		configured, err := updateTrustEndpointOrigin(endpoint)
		if configured != origin {
			continue
		}
		if err != nil {
			return unavailable()
		}
		matched = true
		if p.Server.CACertFile == "" {
			continue
		}
		data, err := readUpdateTrustCA(p.Server.CACertFile, remaining)
		if err != nil {
			return unavailable()
		}
		remaining -= len(data)
		for len(bytes.TrimSpace(data)) > 0 {
			data = bytes.TrimSpace(data)
			// pem.Decode otherwise skips arbitrary leading junk and malformed blocks.
			if !bytes.HasPrefix(data, []byte("-----BEGIN CERTIFICATE-----")) {
				return unavailable()
			}
			endMarker := []byte("-----END CERTIFICATE-----")
			end := bytes.Index(data, endMarker)
			if end < 0 {
				return unavailable()
			}
			end += len(endMarker)
			block, trailing := pem.Decode(data[:end])
			rest := data[end:]
			if len(trailing) != 0 {
				return unavailable()
			}
			if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
				return unavailable()
			}
			if _, err := x509.ParseCertificate(block.Bytes); err != nil {
				return unavailable()
			}
			certificates[string(block.Bytes)] = block.Bytes
			data = rest
		}
	}
	if !matched {
		return nil, &ErrorObject{Code: "trust_not_configured", Message: "No profile configures this update origin."}
	}
	ders := make([][]byte, 0, len(certificates))
	for _, der := range certificates {
		ders = append(ders, der)
	}
	sort.Slice(ders, func(i, j int) bool { return bytes.Compare(ders[i], ders[j]) < 0 })
	var bundle []byte
	for _, der := range ders {
		bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
		if len(bundle) > updateTrustMaxBytes {
			return unavailable()
		}
	}
	sum := sha256.Sum256(bundle)
	return updateTrustResult{Origin: origin, CAPEM: string(bundle), CASHA256: hex.EncodeToString(sum[:])}, nil
}

// Endpoint API paths do not change TLS identity. Recover the authority before
// parsing the full URL so malformed matching configuration cannot appear absent.
func updateTrustEndpointOrigin(raw string) (string, error) {
	scheme, authority, ok := strings.Cut(raw, "://")
	if !ok || !strings.EqualFold(scheme, "https") {
		return "", errors.New("invalid endpoint")
	}
	if end := strings.IndexAny(authority, "/?#\\"); end >= 0 {
		authority = authority[:end]
	}
	if end := strings.LastIndexByte(authority, '@'); end >= 0 {
		authority = authority[end+1:]
	}
	origin, err := updateTrustOrigin("https://" + authority)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "#\\") {
		return origin, errors.New("invalid endpoint")
	}
	return origin, nil
}

func updateTrustOrigin(raw string) (string, error) {
	invalid := errors.New("invalid origin")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Host == "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "#\\") {
		return "", invalid
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.Contains(host, "%") {
		return "", invalid
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		if strings.ContainsAny(u.Host, "[]") || len(host) > 253 {
			return "", invalid
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", invalid
			}
			for _, c := range label {
				if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
					return "", invalid
				}
			}
		}
	}
	port := u.Port()
	if port == "" {
		if strings.HasSuffix(u.Host, ":") {
			return "", invalid
		}
		port = "443"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", invalid
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(n)), nil
}

// Final symlinks and nonregular files are rejected before reading. O_NOFOLLOW
// and O_NONBLOCK also cover replacement with a symlink/FIFO between lstat/open.
// External absolute files and ordinary 0600/0644 policy-managed files are valid.
func readUpdateTrustCA(path string, limit int) ([]byte, error) {
	invalid := errors.New("invalid CA material")
	if !filepath.IsAbs(path) || limit <= 0 {
		return nil, invalid
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > int64(limit) {
		return nil, invalid
	}
	f, err := openUpdateTrustCA(path)
	if err != nil {
		return nil, invalid
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return nil, invalid
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(data) > limit || len(bytes.TrimSpace(data)) == 0 {
		return nil, invalid
	}
	return data, nil
}
