package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func lifecycleWire(t *testing.T, service *Service, op string, payload any) (map[string]any, *ErrorObject) {
	t.Helper()
	record, err := json.Marshal(map[string]any{"version": 1, "kind": "request", "id": "lifecycle-wire", "op": op, "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Serve(context.Background(), service, bytes.NewReader(append(record, '\n')), &output); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Version int            `json:"version"`
		Kind    string         `json:"kind"`
		ID      string         `json:"id"`
		Result  map[string]any `json:"result"`
		Error   *ErrorObject   `json:"error"`
	}
	if bytes.Count(output.Bytes(), []byte("\n")) != 1 || json.Unmarshal(output.Bytes(), &response) != nil || response.Version != 1 || response.Kind != "response" || response.ID != "lifecycle-wire" {
		t.Fatal("invalid correlated JSONL response")
	}
	if (response.Result == nil) == (response.Error == nil) {
		t.Fatal("response must have exactly result or error")
	}
	return response.Result, response.Error
}

func lifecycleWireFixture(t *testing.T) (*Service, string, map[string][32]byte) {
	t.Helper()
	base := t.TempDir()
	app, claude, codex := filepath.Join(base, "App Support 東京"), filepath.Join(base, "Claude home"), filepath.Join(base, "Codex home")
	sentinels := map[string][32]byte{}
	for _, home := range []string{claude, codex} {
		if err := os.MkdirAll(home, 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"credentials.json", "settings.json", "history.jsonl"} {
			path := filepath.Join(home, name)
			data := []byte("synthetic source sentinel: " + name)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			sentinels[path] = sha256.Sum256(data)
		}
	}
	service, err := New(Config{AppSupportRoot: app})
	if err != nil {
		t.Fatal(err)
	}
	if _, e := lifecycleWire(t, service, "profiles.add", map[string]any{"name": "wire", "claude_home": claude, "codex_home": codex}); e != nil {
		t.Fatal(e)
	}
	return service, filepath.Join(app, "profiles", "wire", "profile.json"), sentinels
}

func assertWireSentinels(t *testing.T, sentinels map[string][32]byte) {
	t.Helper()
	for path, before := range sentinels {
		data, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(data) != before {
			t.Fatalf("source sentinel changed or removed: %s", path)
		}
		t.Logf("source_sha256 before=%x after=%x", before, sha256.Sum256(data))
	}
}

func TestLifecycleIPCDefaultUninstallPreservesAndExplicitEraseRemoves(t *testing.T) {
	service, record, sentinels := lifecycleWireFixture(t)
	plan, e := lifecycleWire(t, service, "uninstall.preview", map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	if digest, ok := plan["plan_digest"].(string); !ok || len(digest) != 64 {
		t.Fatal("missing versioned plan digest")
	}
	before, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	result, e := lifecycleWire(t, service, "uninstall.apply", map[string]any{"plan": plan, "confirmed": true})
	if e != nil || len(result["removed"].([]any)) != 0 {
		t.Fatalf("default uninstall: %+v", e)
	}
	after, err := os.ReadFile(record)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("default uninstall erased metadata")
	}
	assertWireSentinels(t, sentinels)
	plan, e = lifecycleWire(t, service, "uninstall.preview", map[string]any{"erase_app_data": true})
	if e != nil {
		t.Fatal(e)
	}
	result, e = lifecycleWire(t, service, "uninstall.apply", map[string]any{"plan": plan, "confirmed": true})
	if e != nil || len(result["removed"].([]any)) == 0 {
		t.Fatalf("explicit erase was a no-op: %+v", e)
	}
	if _, err := os.Lstat(record); !os.IsNotExist(err) {
		t.Fatal("owned metadata still exists after explicit erase")
	}
	assertWireSentinels(t, sentinels)
}

func TestLifecycleIPCResetRequiresCurrentConfirmedPlan(t *testing.T) {
	service, record, sentinels := lifecycleWireFixture(t)
	plan, e := lifecycleWire(t, service, "reset.preview", map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := lifecycleWire(t, service, "reset.apply", map[string]any{"plan": plan, "confirmed": false}); e == nil || e.Code != "confirmation_required" {
		t.Fatal("unconfirmed reset accepted")
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, e := lifecycleWire(t, service, "reset.apply", map[string]any{"plan": plan, "confirmed": true}); e == nil || e.Code != "plan_changed" {
		t.Fatalf("changed plan accepted: %+v", e)
	}
	plan, e = lifecycleWire(t, service, "reset.preview", map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	forged := map[string]any{}
	for k, v := range plan {
		forged[k] = v
	}
	forged["plan_digest"] = "forged"
	if _, e := lifecycleWire(t, service, "reset.apply", map[string]any{"plan": forged, "confirmed": true}); e == nil || e.Code != "invalid_plan" {
		t.Fatal("forged reset accepted")
	}
	if _, e := lifecycleWire(t, service, "reset.apply", map[string]any{"plan": plan, "confirmed": true}); e != nil {
		t.Fatal(e)
	}
	if _, err := os.Lstat(record); !os.IsNotExist(err) {
		t.Fatal("reset succeeded without removing owned metadata")
	}
	assertWireSentinels(t, sentinels)
}

func TestLifecycleIPCRejectsUnknownAndMissingApplyFields(t *testing.T) {
	service, record, sentinels := lifecycleWireFixture(t)
	for _, op := range []string{"reset.preview", "uninstall.preview"} {
		if _, e := lifecycleWire(t, service, op, map[string]any{"force": true}); e == nil || e.Code != "invalid_payload" {
			t.Fatalf("unknown preview field accepted: %s", op)
		}
	}
	for _, op := range []string{"reset.apply", "uninstall.apply"} {
		for _, payload := range []any{map[string]any{}, map[string]any{"plan": nil, "confirmed": true}, map[string]any{"plan": map[string]any{}, "confirmed": nil}} {
			if _, e := lifecycleWire(t, service, op, payload); e == nil || e.Code != "invalid_payload" {
				t.Fatalf("missing/null apply field accepted: %s", op)
			}
		}
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatal("invalid request changed storage")
	}
	assertWireSentinels(t, sentinels)
}
