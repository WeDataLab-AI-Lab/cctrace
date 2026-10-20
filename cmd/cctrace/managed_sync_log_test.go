package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

// The supervisor package has no logger of its own; the daemon has to hand it one
// or a blocked profile leaves nothing in sync.log (the file a person is told to
// look at).
func TestSupervisorWritesBlockedProfilesToTheDaemonLog(t *testing.T) {
	// Not parallel: t.Setenv(HOME) and the process-wide log writer are replaced.
	home := setupTestHome(t)
	t.Setenv("CCTRACE_PROFILE", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_CONFIG_DIR", "")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	def, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	def.Options.SyncEnabled = false
	def.Options.CodexSyncEnabled = true
	if err := profile.Save(def); err != nil {
		t.Fatal(err)
	}
	work := profile.NewDefault()
	work.User, work.Server, work.Options = def.User, def.Server, def.Options
	if err := profile.SaveNamed(work, "work"); err != nil {
		t.Fatal(err)
	}

	sink := &lockedBuffer{}
	logged := func() string { return string(sink.Bytes()) }
	previousWriter, previousFlags, previousInstalled := log.Writer(), log.Flags(), daemonLogInstalled
	log.SetOutput(sink)
	log.SetFlags(0)
	daemonLogInstalled = true
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		daemonLogInstalled = previousInstalled
	})

	done := make(chan error, 1)
	go func() { done <- runSupervisor(syncRunOptions{interval: 50 * time.Millisecond}) }()
	root := profile.DefaultDir()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if running, err := localdaemon.Running(root); err == nil && running {
			_ = localdaemon.Command(ctx, root, "shutdown", "")
		}
		<-done
	})

	managedEventually(t, func() bool {
		return strings.Contains(logged(), "[routes] fully-shared profile=work sources=1 holders=default")
	})
	for _, line := range strings.Split(logged(), "\n") {
		if strings.HasPrefix(line, "[routes]") && strings.Contains(line, home) {
			t.Fatalf("the routes line leaked a path: %s", line)
		}
	}
}
