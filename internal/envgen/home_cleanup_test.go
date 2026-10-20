package envgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/profile"
)

func TestHomeCleanupRemovesOwnedTelemetryAndPreservesUserSettings(t *testing.T) {
	t.Parallel()
	home, _ := profile.NormalizeCodexHome(t.TempDir())
	p := profile.NewDefault()
	p.ClaudeConfigDir = home
	p.Server.Endpoint = "https://fixture.invalid"
	p.Server.AuthToken = "synthetic-token"
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"theme":"user","env":{"USER_KEY":"preserved"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveManagedHomeSettings(home, p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == "" {
		t.Fatal("settings removed")
	}
	var check struct {
		Theme string         `json:"theme"`
		Env   map[string]any `json:"env"`
	}
	if json.Unmarshal(got, &check) != nil {
		t.Fatal("invalid settings")
	}
	if check.Theme != "user" || check.Env["USER_KEY"] != "preserved" || check.Env["CLAUDE_CODE_ENABLE_TELEMETRY"] != nil {
		t.Fatalf("unsafe cleanup: %s", got)
	}
}

func TestHomeCleanupPreservesStampedForeignExporterAndFencesConcurrentEdit(t *testing.T) {
	t.Parallel()
	home, _ := profile.NormalizeCodexHome(t.TempDir())
	p := profile.NewDefault()
	p.ClaudeConfigDir = home
	p.Server.Endpoint = "https://owned.invalid"
	p.Server.AuthToken = "synthetic-owned"
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "settings.json")
	data, _ := os.ReadFile(path)
	var settings map[string]any
	_ = json.Unmarshal(data, &settings)
	env := settings["env"].(map[string]any)
	env["OTEL_EXPORTER_OTLP_ENDPOINT"] = "https://foreign.invalid"
	env["OTEL_EXPORTER_OTLP_HEADERS"] = "Authorization=Bearer synthetic-foreign"
	env["USER_KEY"] = "keep"
	before, _ := json.Marshal(settings)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	after, err := PlanOwnedHomeRemoval(before, p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(after, &got)
	preserved := got["env"].(map[string]any)
	if preserved["OTEL_EXPORTER_OTLP_ENDPOINT"] != "https://foreign.invalid" || preserved["CLAUDE_CODE_ENABLE_TELEMETRY"] != "1" || preserved["USER_KEY"] != "keep" {
		t.Fatalf("foreign exporter changed: %s", after)
	}
	concurrent := append(append([]byte{}, before...), byte(' '))
	if err = os.WriteFile(path, concurrent, 0600); err != nil {
		t.Fatal(err)
	}
	if err = ApplyHomeSettingsCAS(home, before, after); !errors.Is(err, profile.ErrConflict) {
		t.Fatalf("concurrent update not fenced: %v", err)
	}
	actual, _ := os.ReadFile(path)
	if !bytes.Equal(actual, concurrent) {
		t.Fatal("concurrent settings overwritten")
	}
}

func TestCollectionStopCleanupPreservesSharedCATrust(t *testing.T) {
	t.Parallel()
	p := profile.NewDefault()
	p.Server.Endpoint = "https://owned.invalid"
	p.Server.AuthToken = "synthetic"
	env := map[string]any{}
	for k, v := range BuildEnvMap(p) {
		env[k] = v
	}
	env[envNodeExtraCACerts] = "/synthetic/shared-ca.pem"
	settings := map[string]any{"env": env, "cctrace": map[string]any{managedHashField: "old-stamp", managedNodeCAField: "/synthetic/shared-ca.pem"}}
	before, _ := json.Marshal(settings)
	after, err := PlanOwnedHomeRemoval(before, p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(after, &got)
	if got["env"].(map[string]any)[envNodeExtraCACerts] != "/synthetic/shared-ca.pem" || got["cctrace"].(map[string]any)[managedNodeCAField] != "/synthetic/shared-ca.pem" {
		t.Fatal("collection stop changed independent CA trust")
	}
	if got["env"].(map[string]any)[envEnableTelemetry] != nil {
		t.Fatal("collection stop retained managed telemetry enable")
	}
}
