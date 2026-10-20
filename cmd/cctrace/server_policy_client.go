package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"

	"cctrace/internal/atomicfile"
	"cctrace/internal/profile"
	"cctrace/internal/serverpolicy"
)

func serverPolicyTarget(name string) (serverpolicy.Target, error) {
	target := serverpolicy.Target{Profile: name}
	if name != "" {
		if err := profile.ValidateName(name); err != nil {
			return target, err
		}
	}
	if err := os.MkdirAll(profile.DefaultDir(), 0700); err != nil {
		return target, err
	}
	path := filepath.Join(profile.DefaultDir(), "installation-id")
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return target, err
	}
	defer lock.Unlock() //nolint:errcheck
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data = []byte(uuid.NewString())
		err = atomicfile.Write(path, data, 0600)
	}
	if err != nil {
		return target, err
	}
	id := string(data)
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return target, fmt.Errorf("invalid installation-id; restore its original value")
	}
	target.InstallationID = id
	return target, nil
}

var errPolicyRedirect = errors.New("server policy redirects are forbidden")

type policyTransportError struct{ err error }

func (e *policyTransportError) Error() string { return e.err.Error() }
func (e *policyTransportError) Unwrap() error { return e.err }

func policyBootstrapAllowed(bootstrap, current string) bool {
	b, err := url.Parse(bootstrap)
	if err != nil {
		return false
	}
	c, err := url.Parse(current)
	if err != nil {
		return false
	}
	sameHost := strings.EqualFold(strings.TrimSuffix(b.Hostname(), "."), strings.TrimSuffix(c.Hostname(), "."))
	if a, z := net.ParseIP(b.Hostname()), net.ParseIP(c.Hostname()); a != nil && z != nil {
		sameHost = a.Equal(z)
	}
	port, err := strconv.Atoi(b.Port())
	return err == nil && port > 0 && port <= 65535 && b.Scheme == "http" && b.User == nil && b.RawQuery == "" && !b.ForceQuery && b.Fragment == "" && !strings.Contains(bootstrap, "#") && (b.Path == "" || b.Path == "/") && b.RawPath == "" && b.Hostname() != "" && sameHost
}

// A previously signed HTTP origin is retained only for recovering trust after
// an offline device misses a CA overlap window. It never carries credentials.
func fetchServerPolicyWithBootstrap(ctx context.Context, p *profile.Profile, target serverpolicy.Target, keys []ed25519.PublicKey, now time.Time, state serverPolicyState) (*serverpolicy.Policy, string, error) {
	pol, digest, err := fetchServerPolicy(ctx, p, target, keys, now)
	var transportErr *policyTransportError
	if err == nil || !errors.As(err, &transportErr) || ctx.Err() != nil || !policyBootstrapAllowed(state.BootstrapEndpoint, profileHTTPAPIEndpoint(p)) {
		return pol, digest, err
	}
	fallback := *p
	fallback.Server.SyncEndpoint = state.BootstrapEndpoint
	fallback.Server.CACertFile = ""
	return fetchServerPolicy(ctx, &fallback, target, keys, now)
}

// Policy discovery is public and never sends upload/read credentials. Trust is
// established by the embedded signing key, including on the old HTTP endpoint.
func fetchServerPolicy(ctx context.Context, p *profile.Profile, target serverpolicy.Target, keys []ed25519.PublicKey, now time.Time) (*serverpolicy.Policy, string, error) {
	base, err := url.Parse(profileHTTPAPIEndpoint(p))
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "http" && base.Scheme != "https") || base.RawQuery != "" || base.Fragment != "" {
		return nil, "", fmt.Errorf("invalid current policy endpoint")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/client-config-policy"
	q := base.Query()
	q.Set("installation_id", target.InstallationID)
	q.Set("profile", target.Profile)
	// Policies for every profile on the server reach only clients that say they
	// apply them.
	q.Set(serverpolicy.CapabilityParam, serverpolicy.CapabilityAllTargets)
	base.RawQuery = q.Encode()
	client, err := serverClient(p.Server.CACertFile, 5*time.Second)
	if err != nil {
		return nil, "", err
	}
	client.CheckRedirect = rejectPolicyRedirect
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		if resp == nil && !errors.Is(err, errPolicyRedirect) {
			return nil, "", &policyTransportError{err: err}
		}
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent {
		return nil, "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("policy endpoint returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, serverpolicy.MaxBundleBytes+1))
	if err != nil {
		return nil, "", err
	}
	policy, err := serverpolicy.Verify(raw, keys, now)
	if err != nil {
		return nil, "", err
	}
	canonical, err := json.Marshal(policy)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return &policy, hex.EncodeToString(sum[:]), nil
}

func rejectPolicyRedirect(_ *http.Request, _ []*http.Request) error {
	return errPolicyRedirect
}

// Probe each new transport before changing any configuration. Empty protobuf
// exports exercise auth and the actual OTLP receiver without uploading sessions.
func probeServerPolicy(ctx context.Context, p *profile.Profile, policy serverpolicy.Policy) error {
	pool, err := x509.SystemCertPool()
	if err != nil {
		return err
	}
	if !pool.AppendCertsFromPEM([]byte(policy.CAPEM)) {
		return fmt.Errorf("policy has no CA certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: rejectPolicyRedirect}
	for _, endpoint := range []struct{ base, path, method string }{
		{policy.To.Sync, "/api/health", http.MethodGet},
		{policy.To.Sync, "/api/version", http.MethodGet},
		{policy.To.OTEL, "/v1/logs", http.MethodPost},
		{policy.To.OTEL, "/v1/metrics", http.MethodPost},
	} {
		req, err := http.NewRequestWithContext(ctx, endpoint.method, strings.TrimRight(endpoint.base, "/")+endpoint.path, nil)
		if err != nil {
			return err
		}
		if endpoint.method == http.MethodPost {
			req.Header.Set("Content-Type", "application/x-protobuf")
			if p.Server.AuthToken != "" {
				req.Header.Set("Authorization", "Bearer "+p.Server.AuthToken)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("probe %s: %w", endpoint.path, err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("probe %s returned HTTP %d", endpoint.path, resp.StatusCode)
		}
	}
	return nil
}
