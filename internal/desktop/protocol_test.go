package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServe_InvalidUTF8(t *testing.T) {
	svc := newTestService(t, t.TempDir(), "")
	validIDInvalidName := []byte("{\"version\":1,\"kind\":\"request\",\"id\":\"utf8-name\",\"op\":\"profiles.add\",\"payload\":{\"name\":\"bad")
	validIDInvalidName = append(validIDInvalidName, 0xff)
	validIDInvalidName = append(validIDInvalidName, []byte("\",\"claude_home\":\"\",\"codex_home\":\"\"}}\n")...)
	invalidID := []byte("{\"version\":1,\"kind\":\"request\",\"id\":\"")
	invalidID = append(invalidID, 0xff)
	invalidID = append(invalidID, []byte("\",\"op\":\"hello\",\"payload\":{}}\n")...)
	input := append(validIDInvalidName, invalidID...)
	var output bytes.Buffer
	if err := Serve(context.Background(), svc, bytes.NewReader(input), &output); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("response lines=%d: %q", len(lines), output.String())
	}
	for i, line := range lines {
		var got responseView
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("response %d is invalid JSON: %v", i, err)
		}
		if got.Error == nil || got.Error.Code != "invalid_utf8" {
			t.Errorf("response %d error=%+v, want invalid_utf8", i, got.Error)
		}
	}
	if strings.ContainsRune(output.String(), '\uFFFD') {
		t.Fatalf("invalid UTF-8 was replacement-decoded in output: %q", output.String())
	}
}

func TestWriteResponseTooLargeReplacesWholeResult(t *testing.T) {
	var output bytes.Buffer
	response := Response{
		Version: ProtocolVersion,
		Kind:    "response",
		ID:      "large-result",
		Result:  map[string]string{"value": strings.Repeat("x", maxResponseBytes)},
	}
	if err := writeResponse(&output, response); err != nil {
		t.Fatalf("writeResponse: %v", err)
	}
	if output.Len() > maxResponseBytes+1 || bytes.Count(output.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("oversize result was not replaced atomically: bytes=%d output tail=%q", output.Len(), output.Bytes()[output.Len()-min(output.Len(), 200):])
	}
	var got responseView
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "large-result" || got.Error == nil || got.Error.Code != "response_too_large" || len(got.Result) != 0 {
		t.Fatalf("oversize response = %+v", got)
	}
}

func TestServeStopsAfterStdoutFramingFailure(t *testing.T) {
	svc := newTestService(t, t.TempDir(), "")
	input := strings.NewReader("{\"version\":1,\"kind\":\"request\",\"id\":\"first\",\"op\":\"hello\",\"payload\":{}}\n" +
		"{\"version\":1,\"kind\":\"request\",\"id\":\"second\",\"op\":\"hello\",\"payload\":{}}\n")
	writer := &shortWriter{}
	err := Serve(context.Background(), svc, input, writer)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Serve error = %v, want io.ErrShortWrite", err)
	}
	if writer.calls != 1 {
		t.Fatalf("stdout writes=%d, want one failed frame and immediate stop", writer.calls)
	}
}

func TestQuotaFixtureErrorMessageIsRedacted(t *testing.T) {
	base := t.TempDir()
	fixtureRoot := filepath.Join(base, "fixtures")
	if err := os.MkdirAll(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"claude":{"state":"stale","last_success_at":"2026-10-04T12:20:00Z","windows":[{"name":"five_hour","utilization":50,"resets_at":"2026-10-04T15:00:00Z"}],"error":{"code":"rate_limited","message":"fixture-secret-token"}},"codex":{"state":"unavailable","last_success_at":null,"windows":[],"error":{"code":"authentication_required","message":"fixture-secret-token"}}}`
	if err := os.WriteFile(filepath.Join(fixtureRoot, "quota.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, filepath.Join(base, "app support"), fixtureRoot)
	response := call(t, svc, "redacted", "quota.read", map[string]string{"provider": "claude"})
	if response.Error != nil {
		t.Fatalf("quota.read protocol error: %+v", response.Error)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("fixture-secret-token")) {
		t.Fatalf("fixture diagnostic leaked to response: %s", encoded)
	}
}

func FuzzDecodeRequest(f *testing.F) {
	f.Add([]byte(`{"version":1,"kind":"request","id":"seed","op":"hello","payload":{}}`))
	f.Add([]byte("not-json"))
	f.Add([]byte{0xff, '{', '}'})
	f.Fuzz(func(t *testing.T, line []byte) {
		if len(line) > maxRequestBytes {
			t.Skip()
		}
		request, response, ok := decodeRequest(line)
		if ok && (!validRequestID(request.ID) || response.ID != "") {
			t.Fatalf("accepted request has invalid correlation: request=%+v response=%+v", request, response)
		}
		if !ok && response.Kind != "response" {
			t.Fatalf("rejected request has invalid response envelope: %+v", response)
		}
	})
}

type shortWriter struct {
	calls int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	w.calls++
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}
