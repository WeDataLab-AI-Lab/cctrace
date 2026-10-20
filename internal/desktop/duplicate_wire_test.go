package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDuplicateWireCannotOverwriteMutationSelection(t *testing.T) {
	for _, record := range []string{
		`{"version":1,"kind":"request","id":"dup","op":"profiles.remove","payload":{"name":"other","name":"wire"}}`,
		`{"version":1,"kind":"request","id":"dup","op":"status","op":"profiles.remove","payload":{"name":"wire"}}`,
		`{"version":1,"kind":"request","id":"one","id":"two","op":"profiles.remove","payload":{"name":"wire"}}`,
	} {
		t.Run(record, func(t *testing.T) {
			service, path, sentinels := lifecycleWireFixture(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := Serve(context.Background(), service, bytes.NewBufferString(record+"\n"), &output); err != nil {
				t.Fatal(err)
			}
			var response struct {
				Result any          `json:"result"`
				Error  *ErrorObject `json:"error"`
			}
			if err := json.Unmarshal(output.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Result != nil || response.Error == nil || (response.Error.Code != "invalid_request" && response.Error.Code != "invalid_payload") {
				t.Errorf("ambiguous destructive record accepted: %s", output.Bytes())
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Error("ambiguous record changed or removed metadata")
			}
			assertWireSentinels(t, sentinels)
		})
	}
}

func TestDuplicateWireCannotOverwriteProfileHome(t *testing.T) {
	service, record, sentinels := lifecycleWireFixture(t)
	var output bytes.Buffer
	request := `{"version":1,"kind":"request","id":"dup-home","op":"profiles.add","payload":{"name":"new","claude_home":"/unselected/synthetic/missing","claude_home":"","codex_home":""}}`
	if err := Serve(context.Background(), service, bytes.NewBufferString(request+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result any          `json:"result"`
		Error  *ErrorObject `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result != nil || response.Error == nil || (response.Error.Code != "invalid_request" && response.Error.Code != "invalid_payload") {
		t.Errorf("ambiguous home accepted: %s", output.Bytes())
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(filepath.Dir(record)), "new")); !os.IsNotExist(err) {
		t.Error("ambiguous home request created metadata")
	}
	assertWireSentinels(t, sentinels)
}
