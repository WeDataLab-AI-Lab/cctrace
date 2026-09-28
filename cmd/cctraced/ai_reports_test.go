package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The progress stream relies on http.ResponseController reaching the connection
// through the access-log wrapper: Flush to deliver each event, and
// SetWriteDeadline to outlive the server's WriteTimeout.
func TestStatusRecorderPassesFlushAndDeadlineThrough(t *testing.T) {
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		rc := http.NewResponseController(rec)
		if err := rc.SetWriteDeadline(time.Time{}); err != nil {
			t.Errorf("SetWriteDeadline through statusRecorder: %v", err)
		}
		_, _ = io.WriteString(rec, "first\n")
		if err := rc.Flush(); err != nil {
			t.Errorf("Flush through statusRecorder: %v", err)
		}
		<-release
		time.Sleep(150 * time.Millisecond) // past the server WriteTimeout below
		_, _ = io.WriteString(rec, "second\n")
	})
	ts := httptest.NewUnstartedServer(handler)
	ts.Config.WriteTimeout = 100 * time.Millisecond
	ts.Start()
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	first, err := br.ReadString('\n')
	close(release)
	if err != nil || first != "first\n" {
		t.Fatalf("first chunk before handler returned = %q, %v", first, err)
	}
	second, err := br.ReadString('\n')
	if err != nil || second != "second\n" {
		t.Fatalf("second chunk after write timeout = %q, %v", second, err)
	}
}
