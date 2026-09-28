package main

import (
	"fmt"
	"strings"
)

// envDoc is one environment variable the daemon reads. The help text is
// generated from this list rather than written out, so a key cannot be added to
// the code and forgotten here -- TestUsageTextCoversEveryEnvKeyTheDaemonReads
// compares the list against the lookups in this package's source.
type envDoc struct {
	key      string
	def      string
	desc     string
	required bool
}

// Required first: an operator scanning the list should reach the variables that
// decide whether the server starts at all before the tuning knobs.
var envDocs = []envDoc{
	{key: "JWT_SECRET", def: "", desc: "dashboard JWT signing key, at least 32 bytes", required: true},
	{key: "CCTRACE_SETUP_TOKEN", def: "", desc: "initial admin token; generated and logged if unset, invalid after setup"},
	{key: "DATABASE_URL", def: "postgres://cctrace:cctrace@localhost:5432/cctrace?sslmode=disable", desc: "TimescaleDB/PostgreSQL connection string"},
	{key: "COOKIE_SECURE", def: "0", desc: "set to 1 to force Secure auth cookies behind HTTPS proxies"},
	{key: "API_KEY", def: "", desc: "shared API token; empty disables the global key (per-user tokens still work)"},
	{key: "CCTRACE_ALLOWED_ORIGINS", def: "", desc: "comma-separated exact browser origins; empty disables CORS; no wildcard"},
	{key: "CCTRACE_MAX_SYNC_BODY_BYTES", def: "8388608", desc: "/api/sync body ceiling in bytes; unusable values keep the default"},
	{key: "HTTP_PORT", def: "8080", desc: "REST API + dashboard port"},
	{key: "GRPC_PORT", def: "4317", desc: "OTLP/gRPC ingest port"},
	{key: "HTTP_OTEL_PORT", def: "4318", desc: "OTLP/HTTP ingest port (inside the container)"},
	{key: "OTEL_RETENTION_DAYS", def: "", desc: "retention for otel_events/otel_metrics; unset keeps the 90d migration default"},
	{key: "SESSION_RETENTION_DAYS", def: "", desc: "retention for session_records (conversation content); unset = kept indefinitely, 0 = keep forever"},
	{key: "CCTRACE_USERID_ACCESS_CONTROL", def: "true", desc: "per-user data isolation; only the literal \"false\" disables it"},
	{key: "WAL_DIR", def: "", desc: "spill directory for the ingest queue"},
	{key: "STORAGE_VOLUME_PATH", def: "", desc: "volume probed for the admin storage panel; defaults to WAL_DIR"},
	{key: "ACCESS_LOG_FILE", def: "", desc: "HTTP access log path; empty logs to stdout"},
	{key: "EMAIL_ALIASES", def: "", desc: "path to an email alias mapping file"},
	{key: "CCTRACE_ALLOW_EMPTY_DASHBOARD", def: "", desc: "set to 1 to boot without embedded web assets (backend-only)"},
	{key: "CCTRACE_AI_ENABLED_DEFAULT", def: "", desc: "the default switch for weekly AI reports (true or false): whether they may send data to a provider. Admin > AI's switch is stored in the database and wins from the moment an admin flips it. Empty is off unless CCTRACE_AI_RUNTIME_DEFAULT is set; any other value is off"},
	{key: "CCTRACE_AI_RUNTIME_DEFAULT", def: "", desc: "the default AI report runtime (codex-app-server, openai-api, claude-api, nvidia-api, litellm-api) until an admin picks one in Admin > AI. Empty chooses none: reports cannot run until someone chooses. Setting it also turns reports on when CCTRACE_AI_ENABLED_DEFAULT is unset"},
	{key: "CCTRACE_AI_CODEX_HOME", def: "", desc: "dedicated CODEX_HOME for AI reports; defaults to STORAGE_VOLUME_PATH/ai-codex-home"},
	{key: "CCTRACE_AI_MODEL_DEFAULT", def: "gpt-5.6-terra", desc: "default model for AI reports on codex-app-server, until an admin saves one in Admin > AI"},
	{key: "CCTRACE_AI_REASONING_EFFORT_DEFAULT", def: "", desc: "default reasoning effort on codex-app-server, until an admin saves one in Admin > AI; empty uses the model's own default"},
	{key: "CCTRACE_AI_OPENAI_MODEL_DEFAULT", def: "gpt-5.6-terra", desc: "default model for AI reports on openai-api, until an admin saves one in Admin > AI"},
	{key: "CCTRACE_AI_CLAUDE_MODEL_DEFAULT", def: "claude-sonnet-5", desc: "default model for AI reports on claude-api, until an admin saves one in Admin > AI"},
	{key: "CCTRACE_AI_OPENAI_API_KEY", def: "", desc: "OpenAI API key for the openai-api runtime; while set, Admin > AI cannot register or delete one. OPENAI_API_KEY is not read"},
	{key: "CCTRACE_AI_ANTHROPIC_API_KEY", def: "", desc: "Anthropic API key for the claude-api runtime; while set, Admin > AI cannot register or delete one. ANTHROPIC_API_KEY is not read"},
	{key: "CCTRACE_AI_NVIDIA_MODEL_DEFAULT", def: "z-ai/glm-5.3", desc: "default model for AI reports on nvidia-api, until an admin saves one in Admin > AI"},
	{key: "CCTRACE_AI_NVIDIA_API_KEY", def: "", desc: "NVIDIA API key for the nvidia-api runtime; while set, Admin > AI cannot register or delete one. NVIDIA_API_KEY is not read"},
	{key: "CCTRACE_AI_LITELLM_BASE_URL_DEFAULT", def: "", desc: "default LiteLLM proxy address, e.g. https://litellm.example.com; self-hosted, so there is no built-in default. An admin's address in Admin > AI wins and takes effect on the next run. With neither, litellm-api is listed but reports no address. A trailing /v1 is accepted"},
	{key: "CCTRACE_AI_LITELLM_MODEL_DEFAULT", def: "", desc: "default model for AI reports on litellm-api; empty leaves the choice to Admin > AI, since the models a proxy serves are its own"},
	{key: "CCTRACE_AI_LITELLM_API_KEY", def: "", desc: "LiteLLM virtual key for the litellm-api runtime; while set, Admin > AI cannot register or delete one"},
	{key: "CCTRACE_SECRETS_KEY", def: "", desc: "secret, at least 32 bytes, that encrypts API keys registered in Admin > AI (AES-256-GCM, HKDF-SHA256); empty uses JWT_SECRET, so rotating JWT_SECRET then makes stored keys unreadable. Set it to keep keys across JWT_SECRET rotation. Changing the one in use means registering keys again"},
	{key: "CODEX_API_KEY", def: "", desc: "API key for the Codex AI runtime, written to CCTRACE_AI_CODEX_HOME/auth.json at boot (refused over a ChatGPT login); while set, Admin > AI cannot change the account. Empty leaves the login to Admin > AI (ChatGPT device code or API key), and removes only a key file this variable wrote"},
	{key: "CCTRACE_AI_MAX_CONCURRENT", def: "2", desc: "AI report runs allowed at once across all users"},
	{key: "CCTRACE_AI_WALL_CLOCK", def: "8m", desc: "wall-clock limit for one AI report run (Go duration)"},
	{key: "CCTRACE_AI_DEFAULT_TZ", def: "", desc: "IANA time zone (e.g. Asia/Seoul) that weekly automatic AI report times are read in for a user who saved no zone and has no report yet; a saved zone and the last report's zone come first. Empty is UTC; a zone that does not load logs a warning at boot and is UTC. deploy/docker-compose passes Asia/Seoul"},
	{key: "CCTRACE_AI_ENABLED", def: "", desc: "deprecated spelling of CCTRACE_AI_ENABLED_DEFAULT; still read, with a warning at boot"},
	{key: "CCTRACE_AI_RUNTIME", def: "", desc: "deprecated spelling of CCTRACE_AI_RUNTIME_DEFAULT; still read, with a warning at boot"},
	{key: "CCTRACE_AI_MODEL", def: "", desc: "deprecated spelling of CCTRACE_AI_MODEL_DEFAULT; still read, with a warning at boot"},
	{key: "CCTRACE_AI_REASONING_EFFORT", def: "", desc: "deprecated spelling of CCTRACE_AI_REASONING_EFFORT_DEFAULT; still read, with a warning at boot"},
	{key: "CCTRACE_AI_OPENAI_MODEL", def: "", desc: "deprecated spelling of CCTRACE_AI_OPENAI_MODEL_DEFAULT; still read, with a warning at boot"},
	{key: "CCTRACE_AI_CLAUDE_MODEL", def: "", desc: "deprecated spelling of CCTRACE_AI_CLAUDE_MODEL_DEFAULT; still read, with a warning at boot"},
	{key: "CCTRACE_AI_NVIDIA_MODEL", def: "", desc: "deprecated spelling of CCTRACE_AI_NVIDIA_MODEL_DEFAULT; still read, with a warning at boot"},
	{key: "CCTRACE_AI_LITELLM_MODEL", def: "", desc: "deprecated spelling of CCTRACE_AI_LITELLM_MODEL_DEFAULT; still read, with a warning at boot"},
	{key: "CCTRACE_AI_LITELLM_BASE_URL", def: "", desc: "deprecated spelling of CCTRACE_AI_LITELLM_BASE_URL_DEFAULT; still read, with a warning at boot"},
}

// versionText is what --version prints.
func versionText() string {
	return fmt.Sprintf("cctraced %s", version)
}

// usageText is what --help prints. It doubles as the only reference an operator
// holding just the binary has: the daemon takes no configuration flags, so the
// environment table IS the interface.
func usageText() string {
	var b strings.Builder
	b.WriteString(versionText() + "\n\n")
	b.WriteString("cctrace telemetry server: OTLP ingest, REST API, and the embedded dashboard.\n")
	b.WriteString("Server configuration is environment-only.\n\n")
	b.WriteString("Usage:\n")
	b.WriteString("  cctraced            start the server (foreground)\n")
	b.WriteString("  cctraced project-identity-repair --dry-run\n")
	b.WriteString("                       classify the complete projects identity snapshot without writes\n")
	b.WriteString("  cctraced project-identity-repair --apply\n")
	b.WriteString("                       write the recoverable repository ids in one transaction\n")
	b.WriteString("  cctraced --stop     stop the running server (no-op if none is running)\n")
	b.WriteString("  cctraced --version  print the build stamp\n")
	b.WriteString("  cctraced --help     print this text\n\n")

	width := 0
	for _, e := range envDocs {
		if len(e.key) > width {
			width = len(e.key)
		}
	}
	b.WriteString("Environment (required):\n")
	section := true
	for _, e := range envDocs {
		if !e.required && section {
			b.WriteString("\nEnvironment (optional):\n")
			section = false
		}
		def := e.def
		if def == "" {
			def = "-"
		}
		fmt.Fprintf(&b, "  %-*s  %s\n", width, e.key, e.desc)
		fmt.Fprintf(&b, "  %-*s  default: %s\n", width, "", def)
	}
	return b.String()
}
