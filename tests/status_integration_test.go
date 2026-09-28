package tests

import (
	"net"
	"strings"
	"testing"
)

func TestStatusDisplaysCorrectly(t *testing.T) {
	home, bin := setupHome(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start OTEL listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	ts := startAuthServer(t, "Alice Doe", "alice@example.com", "platform")
	input := ts.URL + "\nhttp://" + ln.Addr().String() + "\nalice\ntestpass\nn\nn\n"
	_, err = runCctrace(t, bin, home, input, "init")
	if err != nil {
		t.Fatal("init failed:", err)
	}

	out, err := runCctrace(t, bin, home, "", "status")
	if err != nil {
		t.Fatalf("status failed: %v\noutput: %s", err, out)
	}

	for _, expected := range []string{"Alice Doe", "alice@example.com", "platform"} {
		if !strings.Contains(out, expected) {
			t.Errorf("status output missing %q:\n%s", expected, out)
		}
	}
}

func TestStatusWithoutProfile(t *testing.T) {
	home, bin := setupHome(t)

	out, err := runCctrace(t, bin, home, "", "status")
	if err == nil {
		t.Fatal("expected non-zero exit code when no profile")
	}

	lower := strings.ToLower(out)
	if !strings.Contains(lower, "no profile") {
		t.Errorf("expected 'no profile' message, got:\n%s", out)
	}
}
