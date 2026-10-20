package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLifecycleIPCReportsActualPartialRemovalInErrorEnvelope(t *testing.T) {
	service, record, sentinels := lifecycleWireFixture(t)
	plan, err := service.ResetPreview()
	if err != nil {
		t.Fatal(err)
	}
	profiles := filepath.Dir(filepath.Dir(record))
	if err := os.Chmod(profiles, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(profiles, 0700)
	request, marshalErr := json.Marshal(map[string]any{"version": 1, "kind": "request", "id": "partial", "op": "reset.apply", "payload": map[string]any{"plan": plan, "confirmed": true}})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var output bytes.Buffer
	if err := Serve(context.Background(), service, bytes.NewReader(append(request, '\n')), &output); err != nil {
		t.Fatal(err)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if _, present := response["result"]; present {
		t.Fatal("failure must have error only")
	}
	var failure struct {
		Code    string               `json:"code"`
		Partial LifecycleApplyResult `json:"partial_result"`
	}
	if err := json.Unmarshal(response["error"], &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Code != "storage_error" || failure.Partial.Operation != "reset" || failure.Partial.Digest != plan.Digest || len(failure.Partial.Removed) != 1 || failure.Partial.Removed[0] != filepath.Join("profiles", "wire", "profile.json") {
		t.Errorf("JSONL lost actual partial removal: %s", output.Bytes())
	}
	if _, err := os.Lstat(record); !os.IsNotExist(err) {
		t.Error("partial removal response is not grounded in actual file removal")
	}
	if _, err := os.Stat(filepath.Dir(record)); err != nil {
		t.Error("failed directory removal must leave directory")
	}
	assertWireSentinels(t, sentinels)
}

func TestLifecycleIPCRejectsDuplicateAndNullPreviewPayload(t *testing.T) {
	service, record, sentinels := lifecycleWireFixture(t)
	for _, raw := range []string{`{"erase_app_data":null}`, `{"erase_app_data":true,"erase_app_data":false}`} {
		response := service.Handle(context.Background(), Request{Version: 1, Kind: "request", ID: "strict-preview", Op: "uninstall.preview", Payload: json.RawMessage(raw)})
		if response.Error == nil || response.Error.Code != "invalid_payload" {
			t.Errorf("ambiguous preview accepted: %s %+v", raw, response.Error)
		}
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatal("invalid preview mutated storage")
	}
	assertWireSentinels(t, sentinels)
}
