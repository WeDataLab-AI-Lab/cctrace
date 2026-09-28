package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"

	"cctrace/internal/aireport"
)

// CCTRACE_AI_DEFAULT_TZ reaches the report service, whose schedule the admin
// screen and the ticker both read.
func TestAIDefaultTZReachesTheService(t *testing.T) {
	t.Setenv("CCTRACE_AI_DEFAULT_TZ", "Asia/Seoul")

	env := aiEnvFromEnv(t.TempDir())
	if env.DefaultTZ != "Asia/Seoul" {
		t.Fatalf("DefaultTZ = %q", env.DefaultTZ)
	}
	svc := newAIReportService(aireport.NewMemStore(), env)
	t.Cleanup(func() { svc.Shutdown(context.Background()) })
	got, err := svc.AutoSchedule(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.TZ != "Asia/Seoul" {
		t.Fatalf("service schedule TZ = %q, want the variable's zone", got.TZ)
	}
}

// A zone that does not load must not stop the server or reach the scheduler,
// which would then skip every user without one of their own each minute. It
// falls back to UTC, and the log says so.
func TestInvalidAIDefaultTZFallsBackToUTC(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	t.Setenv("CCTRACE_AI_DEFAULT_TZ", "Mars/Olympus")

	env := aiEnvFromEnv(t.TempDir())
	if env.DefaultTZ != "" {
		t.Fatalf("DefaultTZ = %q, want empty (UTC)", env.DefaultTZ)
	}
	if out := logged.String(); !strings.Contains(out, "CCTRACE_AI_DEFAULT_TZ") || !strings.Contains(out, "UTC") {
		t.Fatalf("no warning naming the variable and UTC; log was %q", out)
	}
}
