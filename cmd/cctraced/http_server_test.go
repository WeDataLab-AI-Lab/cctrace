package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadline = deadline
	return nil
}

func TestDownloadHandlerClearsWriteDeadline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/cctrace-linux-amd64", []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), deadline: time.Now().Add(time.Minute)}

	downloadHandler(dir).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/downloads/cctrace-linux-amd64", nil))

	if !recorder.deadline.IsZero() {
		t.Fatalf("download write deadline = %s, want cleared", recorder.deadline)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

// A client binary is ~11MB and a self-update runs over whatever link the user
// happens to be on, so a /downloads/ response can legitimately outlive a write
// deadline that suits a REST call. If the deadline still applies the body is cut
// mid-stream, and a truncated body is indistinguishable from a short file to the
// client — it just fails the update's checksum compare.
//
// Driven end to end rather than by inspecting server configuration: the fix may
// live on the server (no deadline at all) or on the handler (clearing it per
// response), and the contract is only that a slow transfer arrives whole.
func TestDownloadHandler_SlowTransferIsNotTruncated(t *testing.T) {
	const payloadSize = 4 << 20 // larger than any socket buffer, so the server blocks on write
	const serverWriteTimeout = 100 * time.Millisecond
	const clientChunk = 64 << 10
	const clientPause = 15 * time.Millisecond

	dir := t.TempDir()
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(dir, "cctrace-linux-amd64"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer("", downloadHandler(dir))
	// Stand in for a production write deadline, compressed so the test costs a
	// second instead of a minute. Reading 4MB at the rate below takes ~1s, well
	// past this, so the deadline decides whether the body survives.
	srv.WriteTimeout = serverWriteTimeout
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	resp, err := http.Get(fmt.Sprintf("http://%s/downloads/cctrace-linux-amd64", ln.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Read slowly, the way a client on a poor link would.
	received := 0
	buf := make([]byte, clientChunk)
	for {
		n, readErr := resp.Body.Read(buf)
		received += n
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read failed after %d of %d bytes with a %s server write deadline: %v. "+
				"Serve /downloads/ without a write deadline, or clear it per response with "+
				"http.NewResponseController(w).SetWriteDeadline(time.Time{}).",
				received, payloadSize, serverWriteTimeout, readErr)
		}
		time.Sleep(clientPause)
	}

	if received != payloadSize {
		t.Fatalf("received %d of %d bytes: the %s write deadline truncated the download",
			received, payloadSize, serverWriteTimeout)
	}
}
