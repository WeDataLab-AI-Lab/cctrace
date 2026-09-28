package main

import (
	"bufio"
	"net"
	"strings"
	"testing"
)

func TestPromptInput_ReturnsDefault(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\n"))
	result := promptInput(reader, "Label", "default")
	if result != "default" {
		t.Errorf("expected %q, got %q", "default", result)
	}
}

func TestPromptInput_ReturnsUserInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("alice\n"))
	result := promptInput(reader, "Label", "")
	if result != "alice" {
		t.Errorf("expected %q, got %q", "alice", result)
	}
}

func TestPromptInput_TrimsWhitespace(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("  bob  \n"))
	result := promptInput(reader, "Label", "")
	if result != "bob" {
		t.Errorf("expected %q, got %q", "bob", result)
	}
}

func TestIsEndpointReachable_Unreachable(t *testing.T) {
	if isEndpointReachable("localhost:1") {
		t.Error("expected false for unreachable port 1")
	}
}

func TestIsEndpointReachable_Reachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer ln.Close()

	if !isEndpointReachable(ln.Addr().String()) {
		t.Errorf("expected true for reachable address %s", ln.Addr().String())
	}
}

func TestCheckEndpoint_Connected(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer ln.Close()

	result := checkEndpoint(ln.Addr().String())
	if !strings.Contains(result, "connected") && !strings.Contains(result, "OK") {
		t.Errorf("expected result containing 'connected' or 'OK', got %q", result)
	}
}

func TestCheckEndpoint_Unreachable(t *testing.T) {
	result := checkEndpoint("localhost:1")
	if !strings.Contains(result, "unreachable") && !strings.Contains(result, "WARN") {
		t.Errorf("expected result containing 'unreachable' or 'WARN', got %q", result)
	}
}

func TestIsEndpointReachable_URLFormat(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer ln.Close()

	addr := "grpc://" + ln.Addr().String()
	if !isEndpointReachable(addr) {
		t.Errorf("expected true for URL-format address %s", addr)
	}
}

func TestIsEndpointReachable_NoPort(t *testing.T) {
	// Must not panic — result may be true or false
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("isEndpointReachable panicked: %v", r)
		}
	}()
	isEndpointReachable("localhost")
}
