//go:build codexlive

package codexappserver

import (
	"context"
	"os"
	"testing"
)

// TestLiveModels asks the real codex for its model catalog. model/list spends
// no model turn, but it needs a login, so it is manual only:
//
//	mkdir -p /tmp/cctrace-live-home && ln -s ~/.codex/auth.json /tmp/cctrace-live-home/auth.json
//	CCTRACE_CODEX_LIVE_HOME=/tmp/cctrace-live-home go test -tags codexlive ./internal/codexappserver -run TestLiveModels -v
//
// Never point it at ~/.codex.
func TestLiveModels(t *testing.T) {
	home := os.Getenv("CCTRACE_CODEX_LIVE_HOME")
	if home == "" {
		t.Skip("CCTRACE_CODEX_LIVE_HOME not set")
	}
	models, err := NewRuntime(RuntimeConfig{Home: home, APIKey: os.Getenv("CODEX_API_KEY")}).(*CodexRuntime).Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("empty catalog")
	}
	for _, m := range models {
		if len(m.SupportedReasoningEfforts) == 0 || m.DefaultReasoningEffort == "" {
			t.Errorf("model %s has no reasoning efforts: %+v", m.ID, m)
		}
		t.Logf("%s (%s) default=%v effort=%s supported=%v", m.ID, m.DisplayName, m.IsDefault, m.DefaultReasoningEffort, m.SupportedReasoningEfforts)
	}
}
