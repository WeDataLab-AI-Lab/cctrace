package codexappserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cctrace/internal/airuntime"
	"cctrace/internal/atomicfile"
)

// configValue limits what goes into config.toml to plain identifiers, so a
// value can never close its string and open a table of its own.
var configValue = regexp.MustCompile(`^[A-Za-z0-9._:/-]+$`)

// toolLimitsConfig holds the tool limits no command-line flag reaches. The model
// catalog, not the features, turns on sub-agents and code mode's nested skills
// and clock tools, so they are switched off here; a captured model request then
// carries only the report's tools (experiment §6.3).
const toolLimitsConfig = "\n[features.code_mode]\nexcluded_tool_namespaces = [\"skills\", \"clock\"]\n\n[agents]\nenabled = false\n"

// apiKeyAuthMode is auth.json's auth_mode as `codex login --with-api-key`
// 0.154.0 writes it, next to OPENAI_API_KEY.
const apiKeyAuthMode = "apikey"

// PrepareHome writes the dedicated CODEX_HOME's config.toml with model,
// reasoning effort and the tool limits only. A default ~/.codex carries an otel
// exporter, hooks and MCP servers (experiment §2.1); none of that may reach a
// report run, so the file is replaced whole rather than edited. It also puts
// the API key login in place (prepareAuth).
func PrepareHome(cfg RuntimeConfig) error {
	if cfg.Home == "" {
		return errors.New("codex home is empty")
	}
	var b strings.Builder
	for _, kv := range []struct{ key, value string }{
		{"model", cfg.Model},
		{"model_reasoning_effort", cfg.ReasoningEffort},
	} {
		if kv.value == "" {
			continue
		}
		if !configValue.MatchString(kv.value) {
			return fmt.Errorf("%s %q is not a plain identifier", kv.key, kv.value)
		}
		fmt.Fprintf(&b, "%s = %q\n", kv.key, kv.value)
	}
	b.WriteString(toolLimitsConfig)
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		return err
	}
	if err := atomicfile.Write(filepath.Join(cfg.Home, "config.toml"), []byte(b.String()), 0o600); err != nil {
		return err
	}
	return prepareAuth(cfg.Home, cfg.APIKey)
}

// envKeyMarker, next to auth.json, says the key file there was written from
// CODEX_API_KEY. A key an admin registered from the screen has no marker, so a
// boot without the variable leaves it alone.
const envKeyMarker = "cctrace-env-api-key"

// authFileIsLinked reports a login file that is also someone else's: auth.json
// as a symlink or a hard link, or a home that is itself a symlink. A file bind
// mounted into the home looks like a plain file and is not detected. A file
// that cannot be inspected counts as linked, so a change fails closed.
func authFileIsLinked(home string) bool {
	if info, err := os.Lstat(filepath.Clean(home)); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	info, err := os.Lstat(filepath.Join(home, "auth.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return err != nil || info.Mode()&os.ModeSymlink != 0 || hardLinked(info)
}

// prepareAuth keeps the home's auth.json in line with the key. app-server loads
// auth with CODEX_API_KEY disabled, so a key reaches a run only as the login
// file. A key is written over no file or an earlier key; a file holding any
// other login is someone's sign-in and is refused, not replaced. Without a key
// an earlier key file the environment wrote is removed so runs stop using it.
// A linked auth.json is never written or removed: with a key it is refused
// before the marker is written, without one it is left as it is.
func prepareAuth(home, apiKey string) error {
	if authFileIsLinked(home) {
		if apiKey != "" {
			return errors.New("auth.json in the codex home is a link; replace it with a plain file or unset CODEX_API_KEY")
		}
		return nil
	}
	path := filepath.Join(home, "auth.json")
	marker := filepath.Join(home, envKeyMarker)
	raw, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var current struct {
		AuthMode string `json:"auth_mode"`
	}
	if exists {
		// An unparsable file counts as another login.
		_ = json.Unmarshal(raw, &current)
	}
	isKeyFile := exists && current.AuthMode == apiKeyAuthMode
	if apiKey == "" {
		_, markerErr := os.Stat(marker)
		if markerErr != nil {
			return nil
		}
		if isKeyFile {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		return os.Remove(marker)
	}
	if exists && !isKeyFile {
		return errors.New("auth.json in the codex home holds another login; remove it or unset CODEX_API_KEY")
	}
	body, err := json.Marshal(map[string]string{"auth_mode": apiKeyAuthMode, "OPENAI_API_KEY": apiKey})
	if err != nil {
		return err
	}
	// The marker goes first: a crash between the two writes leaves a marker
	// without a key file, which the next keyless boot clears, rather than an
	// environment key that no boot would ever remove.
	if err := atomicfile.Write(marker, nil, 0o600); err != nil {
		return err
	}
	return atomicfile.Write(path, body, 0o600)
}

// AuthMode says how a run will authenticate: an API key when one is given or
// auth.json holds one, otherwise the ChatGPT login stored there.
func AuthMode(home, apiKey string) string {
	if apiKey != "" {
		return airuntime.AuthModeAPIKey
	}
	if home == "" {
		return airuntime.AuthModeNone
	}
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return airuntime.AuthModeNone
	}
	var current struct {
		AuthMode string `json:"auth_mode"`
	}
	if json.Unmarshal(raw, &current) == nil && current.AuthMode == apiKeyAuthMode {
		return airuntime.AuthModeAPIKey
	}
	return airuntime.AuthModeChatGPT
}
