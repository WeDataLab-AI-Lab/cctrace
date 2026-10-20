package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/profile"
)

// Standard input/output and HOME are process-wide, so this actual CLI journey
// runs serially. Pipe notification replaces sleeps and the server uses port 0.
func TestInitConflictNeverRestoresSingleProfileBackupOverConcurrentState(t *testing.T) {
	home := setupTestHome(t)
	provider := filepath.Join(home, ".claude")
	if err := os.Mkdir(provider, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(provider, ".claude.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"name":"Fixture","email":"testuser@example.com","team":"Fixture","api_token":"synthetic"}`)
	}))
	defer server.Close()
	p, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.Server.Endpoint = server.URL
	p.Server.SyncEndpoint = server.URL
	if err = profile.Save(p); err != nil {
		t.Fatal(err)
	}
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	priorIn, priorOut := os.Stdin, os.Stdout
	os.Stdin = inputRead
	os.Stdout = outputWrite
	defer func() {
		os.Stdin = priorIn
		os.Stdout = priorOut
		_ = inputRead.Close()
		_ = inputWrite.Close()
		_ = outputRead.Close()
		_ = outputWrite.Close()
	}()
	ready := make(chan struct{}, 1)
	captured := make(chan string, 1)
	go func() {
		var output bytes.Buffer
		chunk := make([]byte, 1024)
		for {
			n, e := outputRead.Read(chunk)
			if n > 0 {
				output.Write(chunk[:n])
				if strings.Contains(output.String(), "Commit this summary and server configuration?") {
					select {
					case ready <- struct{}{}:
					default:
					}
				}
			}
			if e != nil {
				captured <- output.String()
				return
			}
		}
	}()
	choice := 0
	for i, option := range initReRunOptions(codexInstalled()) {
		if option.action == reRunFullSetup {
			choice = i + 1
		}
	}
	script := fmt.Sprintf("%d\nall\nallow\nno\nyes\n\n\ntestuser\nsynthetic\ny\n", choice)
	if _, err = inputWrite.WriteString(script); err != nil {
		t.Fatal(err)
	}
	type mutationResult struct {
		data []byte
		err  error
	}
	mutated := make(chan mutationResult, 1)
	go func() {
		defer inputWrite.Close()
		select {
		case <-ready:
		case <-time.After(time.Second):
			mutated <- mutationResult{err: fmt.Errorf("final summary not reached")}
			return
		}
		current, e := profile.Load()
		if e == nil {
			current.User.Team = "concurrent-owner"
			e = profile.Save(current)
		}
		data, _ := os.ReadFile(profile.DefaultPath())
		mutated <- mutationResult{data: data, err: e}
		_, _ = inputWrite.WriteString("yes\n")
	}()
	command := initCmd()
	err = runInit(command, "")
	_ = outputWrite.Close()
	output := <-captured
	change := <-mutated
	if change.err != nil {
		t.Fatal(change.err, output)
	}
	if err == nil || !strings.Contains(err.Error(), "profile_conflict") {
		t.Fatalf("expected confirmed stale-summary conflict, got %v; output=%s", err, output)
	}
	after, readErr := os.ReadFile(profile.DefaultPath())
	if readErr != nil || !bytes.Equal(after, change.data) {
		t.Fatal("old single-file backup overwrote concurrent registry state")
	}
	if _, statErr := os.Stat(filepath.Join(provider, "settings.json")); !os.IsNotExist(statErr) {
		t.Fatal("rejected onboarding configured a provider")
	}
}
