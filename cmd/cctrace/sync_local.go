package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cctrace/internal/codexconfig"
	"cctrace/internal/codexlog"
	"cctrace/internal/profile"
)

// applyLocalDevEndpoints rewrites the runtime OTEL endpoint and sync endpoint
// override to point at a local cctraced (env/.env ports or localhost defaults).
// Returns the resolved OTEL endpoint and mutates endpointOverride in place.
func applyLocalDevEndpoints(p *profile.Profile, endpointOverride *string) string {
	runtimeEndpoint := p.Server.Endpoint
	if otelEndpoint, ok := localDevEndpoint("CCTRACE_LOCAL_OTEL_ENDPOINT", []string{"CCTRACE_DEV_GRPC_PORT", "GRPC_PORT"}, "4317"); ok {
		runtimeEndpoint = otelEndpoint
	} else if !isLocalEndpoint(runtimeEndpoint) {
		runtimeEndpoint = "http://localhost:4317"
	}

	if *endpointOverride != "" {
		return runtimeEndpoint
	}
	if syncEndpoint, ok := localDevEndpoint("CCTRACE_LOCAL_SYNC_ENDPOINT", []string{"CCTRACE_DEV_HTTP_PORT", "HTTP_PORT"}, "8080"); ok {
		*endpointOverride = syncEndpoint
		return runtimeEndpoint
	}
	if isLocalEndpoint(p.Server.SyncEndpoint) {
		return runtimeEndpoint
	}
	*endpointOverride = "http://localhost:8080"
	return runtimeEndpoint
}

func localDevEndpoint(endpointKey string, portKeys []string, defaultPort string) (string, bool) {
	if v := strings.TrimSpace(configValue(endpointKey)); v != "" {
		return normalizeLocalEndpoint(v), true
	}
	for _, key := range portKeys {
		if v := strings.TrimSpace(configValue(key)); v != "" {
			return "http://localhost:" + v, true
		}
	}
	if defaultPort == "" {
		return "", false
	}
	return "http://localhost:" + defaultPort, false
}

func normalizeLocalEndpoint(value string) string {
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	return "http://" + value
}

func configValue(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return localDotEnv()[key]
}

func localDotEnv() map[string]string {
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	for {
		path := filepath.Join(wd, ".env")
		if values := readDotEnv(path); len(values) > 0 {
			return values
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return nil
		}
		wd = parent
	}
}

func readDotEnv(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close() //nolint:errcheck

	values := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" {
			values[key] = value
		}
	}
	return values
}

func isLocalEndpoint(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "localhost") || strings.Contains(value, "127.0.0.1") || strings.Contains(value, "[::1]")
}

// removePIDFileIfCurrent removes the pid file only when it still points at the
// given pid, avoiding a race where a quick stop/start lets an old process delete
// the new daemon's pid file.
func removePIDFileIfCurrent(path string, pid int) error {
	current, err := readPID(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || current != pid {
		return nil
	}
	return os.Remove(path)
}

// autoMigrateCodex enables Codex tracking on first sync after upgrade if
// ~/.codex exists and the profile hasn't opted in yet, then self-heals the
// extra Codex homes of an enabled profile. Idempotent and
// non-fatal: any failure is logged but does not block sync.
//
// A profile whose owner turned Codex sync off is left alone entirely: neither
// the default home nor an extra one is written (#765).
func autoMigrateCodex(p *profile.Profile, profileName string, otelEndpoint string) {
	if otelEndpoint == "" || p.Server.AuthToken == "" || p.Options.CodexSyncDeclined {
		return
	}
	wrote := migrateDefaultCodexHome(p, profileName, otelEndpoint)
	if p.Options.CodexSyncEnabled && healExtraCodexHomes(p, otelEndpoint, true) {
		wrote = true
	}
	// Once per run: every home written here got the same endpoint.
	if wrote {
		warnCodexHTTPSEndpoint(otelEndpoint)
	}
}

// setCodexSync records the owner's choice on Codex sync. Turning it off is
// remembered as a decline, which autoMigrateCodex honours; turning it on clears
// that.
func setCodexSync(p *profile.Profile, enabled bool) {
	p.Options.CodexSyncEnabled = enabled
	p.Options.CodexSyncDeclined = !enabled
}

// healExtraCodexHomes self-heals every other home the sync scans (CODEX_HOME,
// options.codex_dirs) so each one's metrics carry its own account header (#753).
// Only a home whose otel section already sends to this profile's server is
// touched; one sending elsewhere is named and left alone. Reports whether it
// wrote any.
//
// keepHomeToken is true on sync: a home keeps its own token, and one that
// differs from the profile's is named so a reissued token does not go stale in
// silence. `cctrace init` passes false to replace it.
func healExtraCodexHomes(p *profile.Profile, otelEndpoint string, keepHomeToken bool) (wrote bool) {
	defaultDir, _ := filepath.Abs(codexlog.ExpandHome(codexlog.DefaultCodexDir()))
	for _, dir := range resolveCodexScanDirs(io.Discard, p) {
		if dir == defaultDir || skipSymlinkedCodexConfig(dir) {
			continue
		}
		result, err := codexconfig.HealExistingOtelBlock(dir, otelEndpoint, p.Server.AuthToken, keepHomeToken)
		if err == nil && keepHomeToken && (result == codexconfig.HealWritten || result == codexconfig.HealUnchanged) {
			if token := codexconfig.OtelBearerToken(dir); token != "" && token != p.Server.AuthToken {
				fmt.Printf("  [codex] %s/config.toml 의 토큰이 현재 프로필과 다릅니다. 토큰을 재발급했다면 'cctrace init' 으로 갱신하세요.\n", dir)
			}
		}
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "  [codex] config self-heal %s: %v\n", dir, err)
		case result == codexconfig.HealWritten:
			fmt.Printf("  [codex] %s/config.toml otel 블록을 현재 형식으로 정리했습니다.\n", dir)
			wrote = true
		case result == codexconfig.HealForeign:
			fmt.Printf("  [codex] %s/config.toml otel 블록이 다른 수집기를 가리켜 그대로 둡니다.\n", dir)
		}
	}
	return wrote
}

// skipSymlinkedCodexConfig reports whether dir's config.toml is a symlink, saying
// so in one line. Such a file (a dotfile manager's) cannot be replaced
// atomically, and retrying it only repeats the same error on every sync.
func skipSymlinkedCodexConfig(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, "config.toml"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	fmt.Printf("  [codex] %s/config.toml 이 심볼릭 링크라 otel 블록을 고치지 않습니다. 링크 대상을 직접 수정하세요.\n", dir)
	return true
}

// migrateDefaultCodexHome is autoMigrateCodex for the default home: it heals an
// enabled profile's config and opts a disabled profile in. Reports whether it
// wrote the config.
func migrateDefaultCodexHome(p *profile.Profile, profileName string, otelEndpoint string) bool {
	codexDir := codexlog.DefaultCodexDir()
	if _, err := os.Stat(codexDir); err != nil {
		return false
	}
	if skipSymlinkedCodexConfig(codexDir) {
		return false
	}

	if p.Options.CodexSyncEnabled {
		if changed, err := codexconfig.EnsureOtelBlock(codexDir, otelEndpoint, p.Server.AuthToken); err != nil {
			fmt.Fprintf(os.Stderr, "  [codex] config self-heal: %v\n", err)
		} else if changed {
			fmt.Println("  [codex] config.toml otel 블록을 현재 형식으로 정리했습니다.")
			return true
		}
		return false
	}
	if err := codexconfig.WriteOtelBlock(codexDir, otelEndpoint, p.Server.AuthToken); err != nil {
		fmt.Fprintf(os.Stderr, "  [codex-migrate] write config.toml: %v\n", err)
		return false
	}
	p.Options.CodexSyncEnabled = true
	p.UpdatedAt = time.Now().UTC()
	var saveErr error
	if profileName != "" {
		saveErr = profile.SaveNamed(p, profileName)
	} else {
		saveErr = profile.Save(p)
	}
	if saveErr != nil {
		fmt.Fprintf(os.Stderr, "  [codex-migrate] save profile: %v\n", saveErr)
		return true
	}
	fmt.Println("  [codex-migrate] Codex integration enabled automatically.")
	return true
}
