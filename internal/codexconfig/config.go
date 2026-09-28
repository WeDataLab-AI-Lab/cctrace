// Package codexconfig manages ~/.codex/config.toml for cctrace integration.
package codexconfig

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cctrace/internal/codexauth"
)

// Config holds the cctrace-relevant fields from ~/.codex/config.toml.
type Config struct {
	OtelEndpoint string
	HasOtel      bool
}

// ReadConfig parses the relevant fields from ~/.codex/config.toml.
// Returns an empty Config if the file does not exist.
func ReadConfig(codexDir string) (*Config, error) {
	path := filepath.Join(codexDir, "config.toml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	inOtel := false
	var stringState tomlStringState
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if stringState.inMultiline() {
			stringState.update(line)
			continue
		}
		if name, ok := tomlHeaderName(trimmed); ok {
			inOtel = isOtelTableName(name)
			if inOtel {
				cfg.HasOtel = true
			}
			continue
		}
		if inOtel {
			// Supports both the legacy `endpoint = "..."`
			// and the current inline `metrics_exporter = { ... endpoint = "..." }`.
			re := regexp.MustCompile(`endpoint\s*=\s*"([^"]+)"`)
			if m := re.FindStringSubmatch(trimmed); len(m) == 2 {
				cfg.OtelEndpoint = m[1]
			}
		}
		stringState.update(line)
	}
	return cfg, nil
}

// WriteOtelBlock writes or updates the [otel] section in ~/.codex/config.toml.
// Idempotent: existing [otel] section (inline or legacy table form, including
// duplicates) is replaced, other config preserved.
func WriteOtelBlock(codexDir, endpoint, authToken string) error {
	_, err := EnsureOtelBlock(codexDir, endpoint, authToken)
	return err
}

// ErrMultilineRootOtelKey means the config has a root-level otel.* key whose
// value spans several lines. Removing it line by line would leave broken TOML,
// so the config is left untouched for a person to fix.
var ErrMultilineRootOtelKey = errors.New("config.toml has a multi-line root otel.* key; left untouched, move it under [otel] by hand")

// EnsureOtelBlock writes the [otel] block only when the config's current otel
// section differs from what cctrace would generate now (legacy table form,
// duplicates, or a changed endpoint/token). Returns changed=true if it wrote.
// Self-heal entry point for already-enabled Codex users — calling it every sync
// is cheap because an already-correct config is left untouched.
func EnsureOtelBlock(codexDir, endpoint, authToken string) (bool, error) {
	path := filepath.Join(codexDir, "config.toml")

	var existing string
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err == nil {
		existing = string(data)
	}

	if hasMultilineRootOtelKey(existing) {
		return false, ErrMultilineRootOtelKey
	}

	// The billing account the metrics are sent under, from the same auth.json the
	// JSONL syncer stamps session records with (#715). An unreadable file leaves
	// the header out: it must not stop the block from being written.
	accountID, _ := codexauth.ReadAccountID(codexDir)
	updated := replaceOrAppendOtelSection(existing, buildOtelBlock(endpoint, authToken, accountID))
	if updated == existing {
		return false, nil
	}
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		return false, err
	}
	return true, writeConfigFileAtomic(path, []byte(updated), 0600)
}

// HealResult says what HealExistingOtelBlock did with a home.
type HealResult int

const (
	HealUnchanged HealResult = iota // already current
	HealWritten                     // rewritten to the current form
	HealNoOtel                      // no otel section; left alone
	HealForeign                     // otel section sends elsewhere; left alone
)

// HealExistingOtelBlock is EnsureOtelBlock for a home cctrace was never asked to
// configure (#753). It rewrites only an otel section cctrace owns -- one whose
// endpoint, legacy form included, already sends to this cctrace's metrics
// host:port. A config without one is left alone: adding a section would start
// sending that home's metrics on cctrace's own initiative, and rewriting another
// collector's would take them from it.
//
// keepHomeToken keeps a Bearer token already in the block (routine sync: named
// profiles on the same server share the process CODEX_HOME and would otherwise
// overwrite each other). An explicit `cctrace init` passes false so a reissued
// token replaces a stale one.
func HealExistingOtelBlock(codexDir, endpoint, authToken string, keepHomeToken bool) (HealResult, error) {
	data, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if os.IsNotExist(err) {
		return HealNoOtel, nil
	}
	if err != nil {
		return HealUnchanged, err
	}
	section := otelSectionLines(string(data))
	if len(section) == 0 {
		return HealNoOtel, nil
	}
	if !sendsTo(section, codexMetricsEndpoint(endpoint)) {
		return HealForeign, nil
	}
	if token := existingBearerToken(section); keepHomeToken && token != "" {
		authToken = token
	}
	changed, err := EnsureOtelBlock(codexDir, endpoint, authToken)
	if changed {
		return HealWritten, err
	}
	return HealUnchanged, err
}

var (
	otelEndpointRe = regexp.MustCompile(`endpoint\s*=\s*"([^"]+)"`)
	bearerTokenRe  = regexp.MustCompile(`Authorization\s*=\s*"Bearer ([^"]+)"`)
)

// OtelBearerToken returns the Bearer token codexDir's otel block sends, or "" when
// there is none or the config cannot be read.
func OtelBearerToken(codexDir string) string {
	data, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		return ""
	}
	return existingBearerToken(otelSectionLines(string(data)))
}

// existingBearerToken returns the Bearer token the otel section lines send, or "".
func existingBearerToken(section []string) string {
	for _, line := range section {
		if m := bearerTokenRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// sendsTo reports whether any endpoint in the otel section lines, converted the
// way cctrace converts its own (4317 -> 4318; a legacy "host:port" without a
// scheme read as http), names the same host:port as metricsEndpoint. A cctrace
// header alone does not count: another profile's server writes the same header.
func sendsTo(section []string, metricsEndpoint string) bool {
	want, err := url.Parse(metricsEndpoint)
	if err != nil || want.Host == "" {
		return false
	}
	for _, line := range section {
		for _, m := range otelEndpointRe.FindAllStringSubmatch(line, -1) {
			raw := m[1]
			if !strings.Contains(raw, "://") {
				raw = "http://" + raw
			}
			got, err := url.Parse(codexMetricsEndpoint(raw))
			if err == nil && strings.EqualFold(got.Host, want.Host) {
				return true
			}
		}
	}
	return false
}

// buildOtelBlock constructs the [otel] TOML section string.
// accountID is sent only when known -- an empty header would name no account.
func buildOtelBlock(endpoint, authToken, accountID string) string {
	metricsEndpoint := codexMetricsEndpoint(endpoint)
	var headers []string
	if authToken != "" {
		headers = append(headers, fmt.Sprintf("Authorization = %q", "Bearer "+authToken))
	}
	if accountID != "" {
		headers = append(headers, fmt.Sprintf("%s = %q", CodexAccountHeader, accountID))
	}
	if len(headers) > 0 {
		return fmt.Sprintf("[otel]\nmetrics_exporter = { otlp-http = { endpoint = %q, protocol = \"binary\", headers = { %s } } }\n", metricsEndpoint, strings.Join(headers, ", "))
	}
	return fmt.Sprintf("[otel]\nmetrics_exporter = { otlp-http = { endpoint = %q, protocol = \"binary\" } }\n", metricsEndpoint)
}

// CodexAccountHeader carries the Codex billing account id with each metrics
// export, so the server can apply billing-account exclusions to metrics (#715).
const CodexAccountHeader = "X-Cctrace-Codex-Account"

// codexMetricsEndpoint converts the cctrace OTEL endpoint to Codex's
// signal-specific OTLP/HTTP metrics endpoint.
func codexMetricsEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return endpoint
	}

	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(endpoint, "/") + "/v1/metrics"
	}

	host := u.Hostname()
	port := u.Port()
	switch port {
	case "4317":
		port = "4318"
	case "14317":
		port = "14318"
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	}

	path := strings.TrimRight(u.Path, "/")
	switch path {
	case "", "/":
		u.Path = "/v1/metrics"
	case "/v1/logs", "/v1/traces":
		u.Path = "/v1/metrics"
	default:
		if !strings.HasSuffix(path, "/v1/metrics") {
			u.Path = path + "/v1/metrics"
		} else {
			u.Path = path
		}
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// replaceOrAppendOtelSection removes every existing otel section (inline OR
// legacy table form, including duplicates) and writes a single otelBlock in
// their place. If no otel section exists, otelBlock is appended.
//
// The old implementation only matched the exact `[otel]` header, so a config
// using the table form (`[otel.metrics_exporter.otlp-http]`) was not detected
// and a second `[otel]` got appended — producing a duplicate metrics_exporter
// key that breaks Codex's TOML parser.
func replaceOrAppendOtelSection(existing, otelBlock string) string {
	lines := strings.Split(existing, "\n")
	out := make([]string, 0, len(lines)+2)
	inOtel := false
	inserted := false
	inRoot := true
	var stringState tomlStringState
	// Blank and comment lines at the end of an otel body. They are dropped with
	// the body when more otel content follows, but kept when a non-otel header
	// follows: a comment there introduces that next section.
	var pending []string
	keepPending := func() {
		for _, p := range pending {
			if strings.HasPrefix(strings.TrimSpace(p), "#") {
				out = append(out, pending...)
				break
			}
		}
		pending = nil
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if stringState.inMultiline() {
			if !inOtel {
				out = append(out, line)
			}
			stringState.update(line)
			continue
		}
		if name, ok := tomlHeaderName(trimmed); ok {
			if isOtelTableName(name) {
				inOtel = true
				inRoot = false
				pending = nil
				if !inserted {
					out = append(out, strings.TrimRight(otelBlock, "\n"))
					inserted = true
				}
				continue
			}
			if inOtel {
				keepPending()
			}
			inOtel = false
			inRoot = false
		}
		if inOtel {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				pending = append(pending, line)
			} else {
				pending = nil
			}
			continue // drop the body of an otel section
		}
		if inRoot && isRootOtelKeyLine(trimmed) {
			// Dropped, not replaced in place: an [otel] table here would pull
			// every root key after it into the table. The block goes to the
			// first otel table, or is appended.
			continue
		}
		out = append(out, line)
		stringState.update(line)
	}

	result := strings.Join(out, "\n")
	if !inserted {
		// No otel section existed: append with a blank-line separator.
		if result != "" {
			result = strings.TrimRight(result, "\n") + "\n\n"
		}
		result += otelBlock
	}
	// Normalize to exactly one trailing newline so repeated runs are stable
	// (idempotent) — EnsureOtelBlock compares this result against the file.
	return strings.TrimRight(result, "\n") + "\n"
}
