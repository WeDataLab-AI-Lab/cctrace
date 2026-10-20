package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryExclusionPreservesRawFieldsAndRevision(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.Chmod(root, 0700)
	path := filepath.Join(root, "profile.json")
	raw := []byte(`{"version":1,"user":{"email":"fixture@example.invalid"},"server":{"future":true},"options":{"sync_enabled":true,"future":{"x":1}},"future_root":[1,2]}`)
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	r := Registry{Root: root}
	entry, e := r.Load("default")
	if e != nil {
		t.Fatal(e)
	}
	updated, e := r.UpdateExcludedAccounts("default", entry.Revision, []string{"anthropic:acct"})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(path)
	var got, before map[string]any
	json.Unmarshal(b, &got)
	json.Unmarshal(raw, &before)
	if len(got) != len(before) || len(got["user"].(map[string]any)) != 1 || got["future_root"] == nil {
		t.Fatalf("unrelated schema changed: %s", b)
	}
	if got["options"].(map[string]any)["future"] == nil {
		t.Fatal("unknown option dropped")
	}
	if _, e = r.UpdateExcludedAccounts("default", entry.Revision, nil); !errors.Is(e, ErrConflict) {
		t.Fatal("stale revision accepted")
	}
	if updated.Revision == entry.Revision {
		t.Fatal("revision unchanged")
	}
}
