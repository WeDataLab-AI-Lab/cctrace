package envgen

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"cctrace/internal/codexconfig"
	"cctrace/internal/profile"
)

// GenerateSh generates a POSIX shell environment file with LF line endings.
// Uses standard 'export' statements. OTEL env vars are designed to be set globally;
// Claude Code reads them at startup via the OTEL SDK.
//
// OTEL_METRICS_INCLUDE_ACCOUNT_UUID makes Claude Code emit user.account_uuid,
// which it otherwise withholds. That is the key the quota history is stored
// under, so turning it on is what lets a usage line and a token/cost line on
// the same chart mean the same account. org_id already arrives; account_uuid
// did not, and profile_email is an organisation profile rather than a billing
// account -- they diverge as soon as someone signs in personally and works on
// a team profile. It only attaches to events emitted after the env file is
// regenerated, so there is nothing to gain by delaying it.
func GenerateSh(p *profile.Profile) string {
	var b strings.Builder

	b.WriteString("# Claude Code Trace - Auto-generated (do not edit manually)\n")
	b.WriteString("# Re-generate with: cctrace init\n")
	b.WriteString("export CLAUDE_CODE_ENABLE_TELEMETRY=1\n")
	b.WriteString("export OTEL_METRICS_EXPORTER=otlp\n")
	b.WriteString("export OTEL_LOGS_EXPORTER=otlp\n")
	b.WriteString("export OTEL_METRICS_INCLUDE_ACCOUNT_UUID=1\n")

	protocol := p.Server.Protocol
	if protocol == "" {
		protocol = "grpc"
	}
	fmt.Fprintf(&b, "export OTEL_EXPORTER_OTLP_PROTOCOL=%s\n", shValue(protocol))

	if p.Server.Endpoint != "" {
		fmt.Fprintf(&b, "export OTEL_EXPORTER_OTLP_ENDPOINT=%s\n", shValue(p.Server.Endpoint))
	}

	if p.Server.AuthToken != "" {
		fmt.Fprintf(&b, "export OTEL_EXPORTER_OTLP_HEADERS=%s\n", shValue("Authorization=Bearer "+p.Server.AuthToken))
	}

	fmt.Fprintf(&b, "export OTEL_METRIC_EXPORT_INTERVAL=%d\n", p.Options.MetricsExportInterval)
	fmt.Fprintf(&b, "export OTEL_LOGS_EXPORT_INTERVAL=%d\n", p.Options.LogsExportInterval)
	b.WriteString("export OTEL_BSP_MAX_QUEUE_SIZE=4096\n")
	b.WriteString("export OTEL_BSP_SCHEDULE_DELAY=5000\n")
	b.WriteString("export OTEL_BSP_MAX_EXPORT_BATCH_SIZE=512\n")
	b.WriteString("export OTEL_BSP_EXPORT_TIMEOUT=30000\n")

	attrs := buildResourceAttributes(p)
	fmt.Fprintf(&b, "export OTEL_RESOURCE_ATTRIBUTES=%s\n", shValue(attrs))

	return b.String()
}

// GeneratePs1 generates a PowerShell environment file with CRLF line endings.
func GeneratePs1(p *profile.Profile) string {
	var lines []string

	lines = append(lines, "# Claude Code Trace - Auto-generated (do not edit manually)")
	lines = append(lines, "# Re-generate with: cctrace init")
	lines = append(lines, `$env:CLAUDE_CODE_ENABLE_TELEMETRY = "1"`)
	lines = append(lines, `$env:OTEL_METRICS_EXPORTER = "otlp"`)
	lines = append(lines, `$env:OTEL_LOGS_EXPORTER = "otlp"`)
	lines = append(lines, `$env:OTEL_METRICS_INCLUDE_ACCOUNT_UUID = "1"`)

	protocol := p.Server.Protocol
	if protocol == "" {
		protocol = "grpc"
	}
	lines = append(lines, fmt.Sprintf("$env:OTEL_EXPORTER_OTLP_PROTOCOL = %s", psValue(protocol)))

	if p.Server.Endpoint != "" {
		lines = append(lines, fmt.Sprintf("$env:OTEL_EXPORTER_OTLP_ENDPOINT = %s", psValue(p.Server.Endpoint)))
	}

	if p.Server.AuthToken != "" {
		lines = append(lines, fmt.Sprintf("$env:OTEL_EXPORTER_OTLP_HEADERS = %s", psValue("Authorization=Bearer "+p.Server.AuthToken)))
	}

	lines = append(lines, fmt.Sprintf(`$env:OTEL_METRIC_EXPORT_INTERVAL = "%d"`, p.Options.MetricsExportInterval))
	lines = append(lines, fmt.Sprintf(`$env:OTEL_LOGS_EXPORT_INTERVAL = "%d"`, p.Options.LogsExportInterval))
	lines = append(lines, `$env:OTEL_BSP_MAX_QUEUE_SIZE = "4096"`)
	lines = append(lines, `$env:OTEL_BSP_SCHEDULE_DELAY = "5000"`)
	lines = append(lines, `$env:OTEL_BSP_MAX_EXPORT_BATCH_SIZE = "512"`)
	lines = append(lines, `$env:OTEL_BSP_EXPORT_TIMEOUT = "30000"`)

	attrs := buildResourceAttributes(p)
	lines = append(lines, fmt.Sprintf("$env:OTEL_RESOURCE_ATTRIBUTES = %s", psValue(attrs)))

	// Join with CRLF and add trailing CRLF
	return strings.Join(lines, "\r\n") + "\r\n"
}

// WriteSh writes the shell environment file with 0600 permissions.
func WriteSh(p *profile.Profile, path string) error {
	content := GenerateSh(p)
	return os.WriteFile(path, []byte(content), 0600)
}

// WritePs1 writes the PowerShell environment file with 0600 permissions.
func WritePs1(p *profile.Profile, path string) error {
	content := GeneratePs1(p)
	return os.WriteFile(path, []byte(content), 0600)
}

// claudeCAFile is server.ca_cert_file when it applies to Claude Code's exporter:
// a CA is set and the OTEL endpoint is https. Otherwise "".
func claudeCAFile(p *profile.Profile) string {
	if p.Server.CACertFile == "" {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(p.Server.Endpoint))
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return ""
	}
	return p.Server.CACertFile
}

// BuildEnvMap returns the OTEL environment variables as a map for a given profile.
//
// With a private CA on an https endpoint, Claude Code is sent over http/protobuf
// to the OTLP/HTTP port beside the gRPC one, whatever server.protocol says.
// Measured on Claude Code 2.1.291 against a private CA: the grpc exporter
// aborted the TLS handshake with the CA in OTEL_EXPORTER_OTLP_CERTIFICATE and
// in NODE_EXTRA_CA_CERTS alike, and http/protobuf with NODE_EXTRA_CA_CERTS
// (written by applyNodeExtraCACerts) delivered metrics and logs. Whether the
// grpc exporter trusts a CA installed in the OS keychain was not measured.
// Without a CA, or on plain http, nothing here changes.
func BuildEnvMap(p *profile.Profile) map[string]string {
	protocol := p.Server.Protocol
	if protocol == "" {
		protocol = "grpc"
	}
	endpoint := p.Server.Endpoint
	if claudeCAFile(p) != "" {
		protocol = "http/protobuf"
		endpoint = codexconfig.OTLPHTTPEndpoint(endpoint)
	}
	// Keys come from the shared constants so this map and otelKeySet cannot drift.
	env := map[string]string{
		envEnableTelemetry:    "1",
		envMetricsExporter:    "otlp",
		envLogsExporter:       "otlp",
		envIncludeAccountUUID: "1",
		envOTLPProtocol:       protocol,
		envMetricInterval:     fmt.Sprintf("%d", p.Options.MetricsExportInterval),
		envLogsInterval:       fmt.Sprintf("%d", p.Options.LogsExportInterval),
		envBSPMaxQueueSize:    "4096",
		envBSPScheduleDelay:   "5000",
		envBSPMaxExportBatch:  "512",
		envBSPExportTimeout:   "30000",
		envResourceAttributes: buildResourceAttributes(p),
	}
	if endpoint != "" {
		env[envOTLPEndpoint] = endpoint
	}
	if p.Server.AuthToken != "" {
		env[envOTLPHeaders] = "Authorization=Bearer " + p.Server.AuthToken
	}
	return env
}

// buildResourceAttributes builds the OTEL_RESOURCE_ATTRIBUTES value.
// Values are URL-path-escaped per OTEL spec (spaces -> %20).
func buildResourceAttributes(p *profile.Profile) string {
	var parts []string
	if p.User.ID != "" {
		parts = append(parts, "user.id="+url.PathEscape(p.User.ID))
	}
	if p.User.Name != "" {
		parts = append(parts, "user.name="+url.PathEscape(p.User.Name))
	}
	if p.User.Email != "" {
		parts = append(parts, "user.profile.email="+url.PathEscape(p.User.Email))
	}
	if p.User.Team != "" {
		parts = append(parts, "user.team="+url.PathEscape(p.User.Team))
	}
	return strings.Join(parts, ",")
}
