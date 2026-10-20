package desktop

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestProfileRemovalIPCPreservesAmbiguousAndSharedEntries(t *testing.T) {
	for _, kind := range []string{"null", "empty", "shared-credential", "unknown-file"} {
		t.Run(kind, func(t *testing.T) {
			service, record, sentinels := lifecycleWireFixture(t)
			data, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			preserve := map[string][]byte{}
			switch kind {
			case "null":
				data = []byte("null\n")
			case "empty":
				data = []byte("{}\n")
			case "shared-credential":
				data = bytes.Replace(data, []byte(`"protocol": "grpc"`), []byte(`"protocol": "grpc", "auth_token": "synthetic-shared-token"`), 1)
				if !bytes.Contains(data, []byte("synthetic-shared-token")) {
					t.Fatal("shared profile mutation was not applied")
				}
			case "unknown-file":
				path := filepath.Join(filepath.Dir(record), "foreign-history.jsonl")
				preserve[path] = []byte("synthetic unrelated history\n")
			}
			preserve[record] = data
			for path, content := range preserve {
				if err := os.WriteFile(path, content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, e := lifecycleWire(t, service, "profiles.remove", map[string]any{"name": "wire"})
			if e == nil || (e.Code != "unsafe_storage" && e.Code != "manual_repair_required") {
				t.Errorf("ambiguous/shared profile removal accepted: %+v", e)
			}
			for path, before := range preserve {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Errorf("ambiguous/shared entry changed or removed: %s", path)
				}
			}
			assertWireSentinels(t, sentinels)
		})
	}
}

func TestProfileRemovalIPCRemovesOnlyOwnedMapping(t *testing.T) {
	service, record, sentinels := lifecycleWireFixture(t)
	result, e := lifecycleWire(t, service, "profiles.remove", map[string]any{"name": "wire"})
	if e != nil || result["name"] != "wire" || result["removed"] != true {
		t.Fatalf("owned mapping removal failed: %+v %+v", result, e)
	}
	if _, err := os.Lstat(record); !os.IsNotExist(err) {
		t.Error("reported removal was a no-op")
	}
	assertWireSentinels(t, sentinels)
}
