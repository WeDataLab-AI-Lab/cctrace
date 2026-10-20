package desktop

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransferDiagnosticsOptInKeepsLegacyStatusShape(t *testing.T) {
	s := newDaemonStatusService(t, t.TempDir())
	legacy := call(t, s, "legacy", "hello", map[string]any{"daemon_status": true})
	if legacy.Error != nil {
		t.Fatal(legacy.Error)
	}
	hello := call(t, s, "new", "hello", map[string]any{"daemon_diagnostics": true})
	if hello.Error != nil {
		t.Fatalf("diagnostics negotiation failed: %+v", hello.Error)
	}
	read := call(t, s, "read", "daemon.status.read", map[string]any{"diagnostics_version": 1})
	if read.Error != nil {
		t.Fatalf("diagnostics read failed: %+v", read.Error)
	}
	bad := call(t, s, "bad", "daemon.status.read", map[string]any{"diagnostics_version": 2})
	if bad.Error == nil {
		t.Fatal("unknown schema accepted")
	}
}

func TestTransferDiagnosticsRequestAcceptsJSONWhitespace(t *testing.T) {
	if request, ok := parseDaemonStatusRequest(json.RawMessage(` { "diagnostics_version" : 1 } `)); !ok || !request.diagnostics {
		t.Fatal("valid JSON whitespace rejected")
	}
	for _, value := range []string{`{"diagnostics_version":1,"extra":true}`, `{"diagnostics_version":1,"diagnostics_version":1}`, `{"diagnostics_version":1.0}`, `{"diagnostics_version":2}`} {
		if _, ok := parseDaemonStatusRequest(json.RawMessage(value)); ok {
			t.Fatalf("unrecognized schema accepted: %s", value)
		}
	}
}

func TestTransferDiagnosticsReadsActualPrivateFileAndRejectsUntrustedFields(t *testing.T) {
	root := privateDaemonFixture(t)
	writeDaemonProfile(t, root, true, "")
	s := newDaemonStatusService(t, root)
	path := filepath.Join(root, "transfer-diagnostics.json")
	event := `{"schema_version":1,"events":[{"sequence":1,"at":"2026-10-12T11:59:00Z","provider":"codex","client_version":"dev","request_bytes":12345,"record_count":12,"http_status":413,"duration_ms":45,"deadline_ms":30000,"split_depth":0,"outcome":"body_too_large","server_body_limit_known":false}]}`
	writeRawDaemonState(t, path, event)
	legacy := call(t, s, "legacy", "daemon.status.read", map[string]any{})
	if bytes.Contains(legacy.Result, []byte("transfer_diagnostics")) {
		t.Fatal("legacy status schema changed")
	}
	read := call(t, s, "read", "daemon.status.read", map[string]any{"diagnostics_version": 1})
	if read.Error != nil || !bytes.Contains(read.Result, []byte(`"request_bytes":12345`)) {
		t.Fatalf("actual event lost: %s %+v", read.Result, read.Error)
	}

	var pinned map[string]any
	if err := json.Unmarshal([]byte(event), &pinned); err != nil {
		t.Fatal(err)
	}
	failure := (pinned["events"].([]any))[0].(map[string]any)
	failure["sequence"] = 99
	pinned["last_failure"] = failure
	// Keep the retained event at sequence 1 while the forged pinned event claims 99.
	pinned["events"] = []any{map[string]any{"sequence": 1, "at": "2026-10-12T11:59:00Z", "provider": "codex", "client_version": "dev", "request_bytes": 1, "record_count": 1, "duration_ms": 0, "deadline_ms": 30000, "split_depth": 0, "outcome": "accepted", "server_body_limit_known": false}}
	malformed, _ := json.Marshal(pinned)
	writeRawDaemonState(t, path, string(malformed))
	read = call(t, s, "pinned", "daemon.status.read", map[string]any{"diagnostics_version": 1})
	if read.Error != nil || bytes.Contains(read.Result, []byte("transfer_diagnostics")) {
		t.Fatal("forged pinned sequence exported or ordinary status poisoned")
	}
	for _, bad := range []string{
		strings.Replace(event, `"provider":"codex"`, `"provider":"SECRET"`, 1),
		strings.Replace(event, `"duration_ms":45`, `"duration_ms":-1`, 1),
		strings.Replace(event, `"sequence":1`, `"sequence":1,"sequence":2`, 1),
		strings.Replace(event, `"record_count":12`, `"record_count":12,"raw":"SECRET"`, 1),
		strings.Replace(event, `"at":"2026-10-12T11:59:00Z"`, `"at":"2027-10-12T11:59:00Z"`, 1),
	} {
		writeRawDaemonState(t, path, bad)
		read = call(t, s, "bad", "daemon.status.read", map[string]any{"diagnostics_version": 1})
		if read.Error != nil || bytes.Contains(read.Result, []byte("transfer_diagnostics")) || bytes.Contains(read.Result, []byte("SECRET")) {
			t.Fatalf("untrusted diagnostic exported: %s", read.Result)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "history.json")
	writeRawDaemonState(t, outside, event)
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	read = call(t, s, "unsafe", "daemon.status.read", map[string]any{"diagnostics_version": 1})
	if bytes.Contains(read.Result, []byte("transfer_diagnostics")) {
		t.Fatal("symlink diagnostic accepted")
	}
}
