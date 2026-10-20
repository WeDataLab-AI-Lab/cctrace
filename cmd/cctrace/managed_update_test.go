package main

import (
	"context"
	"testing"
	"time"

	"cctrace/internal/profile"
)

func TestManagedUpdaterChecksProfilesWithOneOwner(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	for _, name := range []string{"one", "two"} {
		p := profile.NewDefault()
		p.User = profile.UserInfo{ID: name, Name: name, Email: name + "@fixture.invalid", Team: "fixture"}
		p.Server.Endpoint = "http://127.0.0.1:1"
		if err := profile.SaveNamed(p, name); err != nil {
			t.Fatal(err)
		}
	}
	var calls []string
	applyDaemonParentUpdateFn = func(ctx context.Context, name, endpoint string, local bool) daemonParentUpdateResult {
		calls = append(calls, name)
		if name == "two" {
			return daemonParentUpdateResult{Applied: true}
		}
		return daemonParentUpdateResult{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	applied := 0
	managedUpdateLoop(ctx, profile.Registry{Root: profile.DefaultDir()}, time.Millisecond, func() { applied++ })
	if applied != 1 || len(calls) != 2 || calls[0] != "one" || calls[1] != "two" {
		t.Fatalf("profile update isolation: applied=%d calls=%v", applied, calls)
	}
}
