package desktop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/codexappserver"
	"cctrace/internal/usage"
)

type quotaSelectionTestView struct {
	Provider     string `json:"provider"`
	Profile      string `json:"profile"`
	Enabled      bool   `json:"enabled"`
	SelectionRef string `json:"selection_ref"`
	Generation   uint64 `json:"generation"`
	Source       string `json:"source"`
}

type quotaSelectionsView struct {
	Selections          []quotaSelectionTestView `json:"selections"`
	PollIntervalSeconds int                      `json:"poll_interval_seconds"`
}

type attributedQuotaView struct {
	Provider      string        `json:"provider"`
	State         string        `json:"state"`
	LastSuccessAt *string       `json:"last_success_at"`
	Windows       []quotaWindow `json:"windows"`
	Error         *ErrorObject  `json:"error"`
	SelectionRef  string        `json:"selection_ref"`
	Generation    uint64        `json:"generation"`
	Profile       string        `json:"profile"`
	Enabled       bool          `json:"enabled"`
	Source        string        `json:"source"`
}

func TestQuotaSelectionPersistsPreferencesAndRenewsRuntimeIdentity(t *testing.T) {
	root := t.TempDir()
	appSupport := filepath.Join(root, "app support")
	service := newSelectionTestService(t, appSupport, nil, "", "")
	initial := readQuotaSelections(t, service)
	if initial.PollIntervalSeconds != 300 || len(initial.Selections) != 2 {
		t.Fatalf("initial selections = %+v", initial)
	}
	claude := selectionFor(t, initial, "claude")
	if claude.Profile != "default" || !claude.Enabled || claude.Generation != 1 || claude.SelectionRef == "" || claude.Source != "claude_usage" {
		t.Fatalf("initial Claude selection = %+v", claude)
	}

	setResponse := call(t, service, "disable-codex", "quota.selection.set", map[string]any{
		"provider": "codex", "profile": "default", "enabled": false,
	})
	if setResponse.Error != nil {
		t.Fatalf("quota.selection.set error = %+v", setResponse.Error)
	}
	var setResult quotaSelectionsView
	if err := json.Unmarshal(setResponse.Result, &setResult); err != nil {
		t.Fatal(err)
	}
	if setResult.PollIntervalSeconds != 300 || selectionFor(t, setResult, "codex").Enabled {
		t.Fatalf("quota.selection.set result = %+v", setResult)
	}
	changed := selectionFor(t, setResult, "codex")
	if changed.Generation != 2 || changed.SelectionRef == selectionFor(t, initial, "codex").SelectionRef {
		t.Fatalf("changed Codex identity = %+v", changed)
	}

	statePath := filepath.Join(appSupport, "quota-selections.json")
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat selection preferences: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("selection preference permissions = %04o, want 0600", info.Mode().Perm())
	}
	stored, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"selection_ref", "generation", "home", "fingerprint", "token"} {
		if strings.Contains(strings.ToLower(string(stored)), forbidden) {
			t.Fatalf("persisted preference contains runtime identity or secret marker %q: %s", forbidden, stored)
		}
	}

	restarted := newSelectionTestService(t, appSupport, nil, "", "")
	afterRestart := readQuotaSelections(t, restarted)
	restartedCodex := selectionFor(t, afterRestart, "codex")
	if restartedCodex.Profile != "default" || restartedCodex.Enabled || restartedCodex.Generation != 1 {
		t.Fatalf("restarted Codex selection = %+v", restartedCodex)
	}
	if restartedCodex.SelectionRef == changed.SelectionRef {
		t.Fatal("helper restart reused the prior selection_ref")
	}
}

func TestQuotaSelectionRemovalResetsMappedSelectionAndCacheIdentity(t *testing.T) {
	root := t.TempDir()
	claudeHome := makeQuotaHome(t, "Claude default")
	mappedHome := makeQuotaHome(t, "Claude mapped")
	sentinel := filepath.Join(mappedHome, "synthetic source marker")
	if err := os.WriteFile(sentinel, []byte("fixture-only source bytes"), 0o400); err != nil {
		t.Fatal(err)
	}
	sentinelBefore := fileDigest(t, sentinel)
	service := newSelectionTestService(t, filepath.Join(root, "app support"), nil, claudeHome, "")
	added := call(t, service, "profile-add", "profiles.add", map[string]string{
		"name": "mapped", "claude_home": mappedHome, "codex_home": "",
	})
	if added.Error != nil {
		t.Fatalf("profiles.add error = %+v", added.Error)
	}
	setMapped := call(t, service, "select-mapped", "quota.selection.set", map[string]any{
		"provider": "claude", "profile": "mapped", "enabled": true,
	})
	if setMapped.Error != nil {
		t.Fatalf("select mapped error = %+v", setMapped.Error)
	}
	mapped := selectionFor(t, decodeSelections(t, setMapped), "claude")
	if mapped.Profile != "mapped" || mapped.Generation != 2 || mapped.Source != "claude_usage" {
		t.Fatalf("mapped selection = %+v", mapped)
	}
	status := call(t, service, "status", "status", map[string]any{})
	if status.Error != nil || !bytes.Contains(status.Result, []byte(`"state":"disabled"`)) {
		t.Fatalf("selection operation changed sync status: %+v", status)
	}
	if fileDigest(t, sentinel) != sentinelBefore {
		t.Fatal("selection operations changed a mapped-home source sentinel")
	}

	removed := call(t, service, "profile-remove", "profiles.remove", map[string]string{"name": "mapped"})
	if removed.Error != nil {
		t.Fatalf("profiles.remove error = %+v", removed.Error)
	}
	reset := selectionFor(t, readQuotaSelections(t, service), "claude")
	if reset.Profile != "default" || reset.Generation != 3 || reset.SelectionRef == mapped.SelectionRef {
		t.Fatalf("selection after mapping removal = %+v", reset)
	}
}

func TestQuotaSelectionSurvivesUnavailableMappedHome(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	claudeDefaultHome := makeQuotaHome(t, "Claude default")
	claudeMappedHome := makeQuotaHome(t, "Claude mapped")
	codexDefaultHome := makeQuotaHome(t, "Codex default")
	codexMappedHome := makeQuotaHome(t, "Codex mapped")
	writeCodexAuth(t, codexMappedHome, `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-only"}}`)
	var claudeReads atomic.Int32
	var codexReads atomic.Int32
	reader := &quotaSelectionReader{
		claude: func(_ context.Context, home string) (*usage.Response, error) {
			claudeReads.Add(1)
			switch home {
			case claudeDefaultHome:
				return syntheticClaudeUsage(now, "claude-default-auth", 17), nil
			case claudeMappedHome:
				return syntheticClaudeUsage(now, "claude-mapped-auth", 61), nil
			default:
				t.Fatalf("Claude reader home = %q", home)
				return nil, nil
			}
		},
		codex: func(_ context.Context, home string) (*codexappserver.Snapshot, error) {
			codexReads.Add(1)
			if home != codexMappedHome {
				t.Fatalf("Codex reader home = %q, want mapped home", home)
			}
			return codexSnapshot(now, "synthetic@example.invalid", 31), nil
		},
	}
	appSupport := filepath.Join(t.TempDir(), "app support")
	service := newSelectionTestServiceWithClock(t, appSupport, reader, claudeDefaultHome, codexDefaultHome, func() time.Time { return now })

	added := call(t, service, "add-mapped", "profiles.add", map[string]string{
		"name": "mapped", "claude_home": claudeMappedHome, "codex_home": codexMappedHome,
	})
	if added.Error != nil {
		t.Fatalf("profiles.add error = %+v", added.Error)
	}
	selected := call(t, service, "select-claude-mapped", "quota.selection.set", map[string]any{
		"provider": "claude", "profile": "mapped", "enabled": true,
	})
	if selected.Error != nil {
		t.Fatalf("select Claude mapped profile error = %+v", selected.Error)
	}
	claudeSelection := selectionFor(t, decodeSelections(t, selected), "claude")
	warmed := attributedRead(t, service, "warm-mapped-cache", claudeSelection)
	if warmed.State != "fresh" || len(warmed.Windows) != 1 || warmed.Windows[0].Utilization != 61 {
		t.Fatalf("mapped Claude reading = %+v", warmed)
	}

	profilePath := filepath.Join(appSupport, "profiles", "mapped", "profile.json")
	if err := os.RemoveAll(claudeMappedHome); err != nil {
		t.Fatalf("remove mapped Claude home: %v", err)
	}
	if _, err := os.Stat(profilePath); err != nil {
		t.Fatalf("mapped profile metadata was not left in place: %v", err)
	}

	metadata := readQuotaSelections(t, service)
	claudeMetadata := selectionFor(t, metadata, "claude")
	codexMetadata := selectionFor(t, metadata, "codex")
	if claudeMetadata.Profile != "mapped" || claudeMetadata.SelectionRef != claudeSelection.SelectionRef || claudeMetadata.Generation != claudeSelection.Generation {
		t.Fatalf("Claude metadata lost unavailable mapped selection: %+v", claudeMetadata)
	}
	if codexMetadata.Profile != "default" || !codexMetadata.Enabled || claudeReads.Load() != 1 || codexReads.Load() != 0 {
		t.Fatalf("metadata read called a provider or lost Codex selection: Claude=%d Codex=%d selection=%+v", claudeReads.Load(), codexReads.Load(), codexMetadata)
	}

	unavailable := call(t, service, "read-missing-claude-home", "quota.read", map[string]any{
		"provider": "claude", "selection_ref": claudeMetadata.SelectionRef, "generation": claudeMetadata.Generation,
	})
	if unavailable.Error == nil || unavailable.Error.Code != "selection_unavailable" || len(unavailable.Result) != 0 || claudeReads.Load() != 1 {
		t.Fatalf("missing-home Claude read = %+v, provider reads=%d", unavailable, claudeReads.Load())
	}

	changedCodex := call(t, service, "select-codex-mapped", "quota.selection.set", map[string]any{
		"provider": "codex", "profile": "mapped", "enabled": true,
	})
	if changedCodex.Error != nil {
		t.Fatalf("change Codex profile while Claude home is unavailable: %+v", changedCodex.Error)
	}
	codexSelection := selectionFor(t, decodeSelections(t, changedCodex), "codex")
	if codexSelection.Profile != "mapped" {
		t.Fatalf("Codex selection = %+v", codexSelection)
	}
	codexResult := attributedRead(t, service, "read-codex-mapped", codexSelection)
	if codexResult.State != "fresh" || len(codexResult.Windows) != 1 || codexResult.Windows[0].Utilization != 31 || codexReads.Load() != 1 {
		t.Fatalf("Codex read after Claude home loss = %+v, provider reads=%d", codexResult, codexReads.Load())
	}

	disabled := call(t, service, "disable-unavailable-claude", "quota.selection.set", map[string]any{
		"provider": "claude", "profile": "mapped", "enabled": false,
	})
	if disabled.Error != nil {
		t.Fatalf("disable unavailable Claude selection: %+v", disabled.Error)
	}
	disabledSelection := selectionFor(t, decodeSelections(t, disabled), "claude")
	if disabledSelection.Profile != "mapped" || disabledSelection.Enabled || disabledSelection.Generation != claudeSelection.Generation+1 {
		t.Fatalf("disabled unavailable selection = %+v", disabledSelection)
	}

	recovered := call(t, service, "recover-claude-default", "quota.selection.set", map[string]any{
		"provider": "claude", "profile": "default", "enabled": true,
	})
	if recovered.Error != nil {
		t.Fatalf("recover Claude default profile: %+v", recovered.Error)
	}
	recoveredSelection := selectionFor(t, decodeSelections(t, recovered), "claude")
	if recoveredSelection.Profile != "default" || !recoveredSelection.Enabled || recoveredSelection.Generation != disabledSelection.Generation+1 {
		t.Fatalf("recovered Claude selection = %+v", recoveredSelection)
	}
	recoveredRead := attributedRead(t, service, "read-recovered-claude", recoveredSelection)
	if recoveredRead.State != "fresh" || len(recoveredRead.Windows) != 1 || recoveredRead.Windows[0].Utilization != 17 {
		t.Fatalf("recovered Claude reading = %+v", recoveredRead)
	}
}

func TestQuotaSelectionSwitchAndRemovalDoNotReuseOtherHomeSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	defaultHome := makeQuotaHome(t, "Claude default")
	mappedHome := makeQuotaHome(t, "Claude mapped")
	var defaultReads atomic.Int32
	reader := &quotaSelectionReader{claude: func(_ context.Context, home string) (*usage.Response, error) {
		if home == mappedHome {
			return syntheticClaudeUsage(now, "same-auth", 44), nil
		}
		if home != defaultHome {
			t.Fatalf("reader home = %q, want one selected fixture home", home)
		}
		if defaultReads.Add(1) == 1 {
			return syntheticClaudeUsage(now, "same-auth", 11), nil
		}
		return &usage.Response{TokenFingerprint: "same-auth"}, &usage.RateLimitError{RetryAfter: time.Minute}
	}}
	service := newSelectionTestServiceWithClock(t, filepath.Join(t.TempDir(), "app support"), reader, defaultHome, "", func() time.Time { return now })
	initial := selectionFor(t, readQuotaSelections(t, service), "claude")
	defaultResult := attributedRead(t, service, "default-snapshot", initial)
	if defaultResult.Windows[0].Utilization != 11 {
		t.Fatalf("default snapshot = %+v", defaultResult)
	}
	added := call(t, service, "profile-add", "profiles.add", map[string]string{"name": "mapped", "claude_home": mappedHome, "codex_home": ""})
	if added.Error != nil {
		t.Fatalf("profiles.add error = %+v", added.Error)
	}
	changed := call(t, service, "select-mapped", "quota.selection.set", map[string]any{"provider": "claude", "profile": "mapped", "enabled": true})
	if changed.Error != nil {
		t.Fatalf("selection switch error = %+v", changed.Error)
	}
	mapped := selectionFor(t, decodeSelections(t, changed), "claude")
	mappedResult := attributedRead(t, service, "mapped-snapshot", mapped)
	if mappedResult.State != "fresh" || mappedResult.Windows[0].Utilization != 44 {
		t.Fatalf("mapped snapshot = %+v", mappedResult)
	}
	removed := call(t, service, "profile-remove", "profiles.remove", map[string]string{"name": "mapped"})
	if removed.Error != nil {
		t.Fatalf("profiles.remove error = %+v", removed.Error)
	}
	reset := selectionFor(t, readQuotaSelections(t, service), "claude")
	if reset.Profile != "default" || reset.Generation != mapped.Generation+1 {
		t.Fatalf("selection after removal = %+v", reset)
	}
	afterRemoval := attributedRead(t, service, "after-removal", reset)
	if afterRemoval.State != "unavailable" || len(afterRemoval.Windows) != 0 || afterRemoval.LastSuccessAt != nil || afterRemoval.Error == nil || afterRemoval.Error.Code != "rate_limited" {
		t.Fatalf("removed-home snapshot leaked into default result: %+v", afterRemoval)
	}
}

func TestAttributedCache_IdentityAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	currentTime := now
	var reads atomic.Int32
	reader := &quotaSelectionReader{claude: func(context.Context, string) (*usage.Response, error) {
		switch reads.Add(1) {
		case 1:
			return syntheticClaudeUsage(now, "same-auth", 22), nil
		case 2:
			return &usage.Response{TokenFingerprint: "same-auth"}, &usage.RateLimitError{RetryAfter: time.Minute}
		case 3:
			return &usage.Response{}, &usage.RateLimitError{RetryAfter: time.Minute}
		case 4:
			return syntheticClaudeUsage(now, "same-auth", 33), nil
		default:
			return &usage.Response{TokenFingerprint: "same-auth"}, &usage.RateLimitError{RetryAfter: time.Minute}
		}
	}}
	service := newSelectionTestServiceWithClock(t, filepath.Join(t.TempDir(), "app support"), reader, makeQuotaHome(t, "Claude"), "", func() time.Time { return currentTime })
	selection := selectionFor(t, readQuotaSelections(t, service), "claude")
	_ = attributedRead(t, service, "success", selection)
	sameIdentityFailure := attributedRead(t, service, "same-identity-failure", selection)
	if sameIdentityFailure.State != "stale" || sameIdentityFailure.Error == nil || sameIdentityFailure.Error.Code != "rate_limited" || len(sameIdentityFailure.Windows) != 1 || sameIdentityFailure.Windows[0].Utilization != 22 {
		t.Fatalf("same-identity failure did not retain bounded cache: %+v", sameIdentityFailure)
	}
	unknownIdentity := attributedRead(t, service, "unknown-identity", selection)
	if unknownIdentity.State != "unavailable" || len(unknownIdentity.Windows) != 0 || unknownIdentity.LastSuccessAt != nil {
		t.Fatalf("unknown identity reused cache: %+v", unknownIdentity)
	}
	_ = attributedRead(t, service, "refreshed", selection)
	currentTime = currentTime.Add(16 * time.Minute)
	expired := attributedRead(t, service, "expired", selection)
	if expired.State != "unavailable" || len(expired.Windows) != 0 || expired.LastSuccessAt != nil {
		t.Fatalf("expired cache remained available: %+v", expired)
	}
}

func TestAttributedQuotaReadRejectsMismatchedAndDisabledSelections(t *testing.T) {
	var reads atomic.Int32
	reader := &quotaSelectionReader{claude: func(context.Context, string) (*usage.Response, error) {
		reads.Add(1)
		return nil, errors.New("reader must not run")
	}}
	service := newSelectionTestService(t, filepath.Join(t.TempDir(), "app support"), reader, makeQuotaHome(t, "Claude"), "")
	selection := selectionFor(t, readQuotaSelections(t, service), "claude")
	wrong := call(t, service, "wrong-selection", "quota.read", map[string]any{
		"provider": "claude", "selection_ref": "stale-reference", "generation": selection.Generation,
	})
	if wrong.Error == nil || wrong.Error.Code != "selection_mismatch" || len(wrong.Result) != 0 {
		t.Fatalf("mismatched selection response = %+v", wrong)
	}

	setDisabled := call(t, service, "disable-claude", "quota.selection.set", map[string]any{
		"provider": "claude", "profile": "default", "enabled": false,
	})
	if setDisabled.Error != nil {
		t.Fatalf("disable selection error = %+v", setDisabled.Error)
	}
	disabled := selectionFor(t, decodeSelections(t, setDisabled), "claude")
	old := call(t, service, "old-generation", "quota.read", map[string]any{
		"provider": "claude", "selection_ref": selection.SelectionRef, "generation": selection.Generation,
	})
	if old.Error == nil || old.Error.Code != "selection_mismatch" {
		t.Fatalf("old generation response = %+v", old)
	}
	got := call(t, service, "disabled-read", "quota.read", map[string]any{
		"provider": "claude", "selection_ref": disabled.SelectionRef, "generation": disabled.Generation,
	})
	if got.Error != nil {
		t.Fatalf("disabled attributed quota protocol error = %+v", got.Error)
	}
	var result attributedQuotaView
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "unavailable" || result.Error == nil || result.Error.Code != "provider_disabled" || result.Enabled || result.SelectionRef != disabled.SelectionRef {
		t.Fatalf("disabled quota = %+v", result)
	}
	if reads.Load() != 0 {
		t.Fatalf("provider reader calls = %d, want 0", reads.Load())
	}
}

func TestServeRequiresQuotaSelectionNegotiation(t *testing.T) {
	service := newSelectionTestService(t, filepath.Join(t.TempDir(), "app support"), nil, "", "")
	input := strings.NewReader(
		`{"version":1,"kind":"request","id":"legacy-hello","op":"hello","payload":{}}` + "\n" +
			`{"version":1,"kind":"request","id":"selection-read","op":"quota.selection.read","payload":{}}` + "\n" +
			`{"version":1,"kind":"request","id":"attributed-read","op":"quota.read","payload":{"provider":"claude","selection_ref":"unnegotiated","generation":1}}` + "\n",
	)
	var output bytes.Buffer
	if err := Serve(context.Background(), service, input, &output); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	responses := make([]responseView, 0, len(lines))
	for _, line := range lines {
		var response responseView
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		responses = append(responses, response)
	}
	if len(responses) != 3 || responses[0].Error != nil || responses[1].Error == nil || responses[1].Error.Code != "capability_not_negotiated" || responses[2].Error == nil || responses[2].Error.Code != "capability_not_negotiated" {
		t.Fatalf("pre-negotiation responses = %+v", responses)
	}
	var legacyHello struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(responses[0].Result, &legacyHello); err != nil {
		t.Fatal(err)
	}
	if _, exists := legacyHello.Capabilities["quota_selection"]; exists {
		t.Fatalf("legacy hello advertised selection capability: %s", responses[0].Result)
	}
}

func TestAttributedQuotaDiscardsLateSuccessAndErrorAfterSelectionSwitch(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "late success"},
		{name: "late error", err: errors.New("private token /synthetic/home")},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			defaultHome := makeQuotaHome(t, "Claude default")
			mappedHome := makeQuotaHome(t, "Claude mapped")
			started := make(chan struct{})
			release := make(chan struct{})
			now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
			reader := &quotaSelectionReader{claude: func(context.Context, string) (*usage.Response, error) {
				close(started)
				<-release // Deliberately ignore cancellation to prove the publication guard.
				if test.err != nil {
					return nil, test.err
				}
				return syntheticClaudeUsage(now, "auth-a", 12), nil
			}}
			service := newSelectionTestServiceWithClock(t, filepath.Join(root, "app support"), reader, defaultHome, "", func() time.Time { return now })
			added := call(t, service, "profile-add", "profiles.add", map[string]string{"name": "mapped", "claude_home": mappedHome, "codex_home": ""})
			if added.Error != nil {
				t.Fatalf("profiles.add error = %+v", added.Error)
			}
			initial := selectionFor(t, readQuotaSelections(t, service), "claude")
			responseCh := make(chan responseView, 1)
			go func() {
				responseCh <- call(t, service, "late-read", "quota.read", map[string]any{
					"provider": "claude", "selection_ref": initial.SelectionRef, "generation": initial.Generation,
				})
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("provider read did not start")
			}
			changed := call(t, service, "switch-selection", "quota.selection.set", map[string]any{
				"provider": "claude", "profile": "mapped", "enabled": true,
			})
			if changed.Error != nil {
				t.Fatalf("selection switch error = %+v", changed.Error)
			}
			changedSelection := selectionFor(t, decodeSelections(t, changed), "claude")
			cancelOld := call(t, service, "cancel-old-generation", "quota.cancel", map[string]any{
				"provider": "claude", "selection_ref": initial.SelectionRef, "generation": initial.Generation,
			})
			if cancelOld.Error != nil {
				t.Fatalf("cancel old generation error = %+v", cancelOld.Error)
			}
			var cancelResult struct {
				Cancelled bool `json:"cancelled"`
			}
			if err := json.Unmarshal(cancelOld.Result, &cancelResult); err != nil || !cancelResult.Cancelled {
				t.Fatalf("cancel old generation result=%s err=%v", cancelOld.Result, err)
			}
			stillCurrent := selectionFor(t, readQuotaSelections(t, service), "claude")
			if stillCurrent.Generation != changedSelection.Generation || stillCurrent.SelectionRef != changedSelection.SelectionRef {
				t.Fatalf("cancel changed current selection: before=%+v after=%+v", changedSelection, stillCurrent)
			}
			close(release)
			select {
			case response := <-responseCh:
				if response.Error == nil || response.Error.Code != "selection_changed" || len(response.Result) != 0 {
					t.Fatalf("late response = %+v", response)
				}
			case <-time.After(time.Second):
				t.Fatal("late provider read did not finish")
			}
		})
	}
}

func TestAttributedQuotaAuthIdentityChangeAdvancesGenerationAndClearsData(t *testing.T) {
	for _, test := range []struct {
		name        string
		returnError bool
	}{
		{name: "late success"},
		{name: "late error", returnError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
			var reads atomic.Int32
			reader := &quotaSelectionReader{claude: func(context.Context, string) (*usage.Response, error) {
				call := reads.Add(1)
				if call == 1 {
					return syntheticClaudeUsage(now, "auth-a", 18), nil
				}
				response := syntheticClaudeUsage(now, "auth-b", 45)
				if test.returnError && call == 2 {
					return response, usage.ErrAccountSwitch
				}
				return response, nil
			}}
			home := makeQuotaHome(t, "Claude")
			service := newSelectionTestServiceWithClock(t, filepath.Join(t.TempDir(), "app support"), reader, home, "", func() time.Time { return now })
			initial := selectionFor(t, readQuotaSelections(t, service), "claude")
			first := attributedRead(t, service, "first", initial)
			if first.State != "fresh" || first.Windows[0].Utilization != 18 || first.Source != "claude_usage" {
				t.Fatalf("first attributed quota = %+v", first)
			}
			encoded, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"auth-a", home} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("attributed result leaked %q: %s", secret, encoded)
				}
			}
			changed := call(t, service, "auth-change", "quota.read", map[string]any{
				"provider": "claude", "selection_ref": initial.SelectionRef, "generation": initial.Generation,
			})
			if changed.Error == nil || changed.Error.Code != "selection_changed" || len(changed.Result) != 0 {
				t.Fatalf("auth changed quota response = %+v", changed)
			}
			current := selectionFor(t, readQuotaSelections(t, service), "claude")
			if current.Generation != initial.Generation+1 || current.SelectionRef == initial.SelectionRef {
				t.Fatalf("selection after auth change = %+v", current)
			}
			second := attributedRead(t, service, "second", current)
			if second.State != "fresh" || second.Windows[0].Utilization != 45 || second.SelectionRef != current.SelectionRef {
				t.Fatalf("new identity quota = %+v", second)
			}
		})
	}
}

func TestQuotaReadsCoalesceAndCancelWithoutChangingGeneration(t *testing.T) {
	started := make(chan struct{})
	readerDone := make(chan struct{})
	var reads atomic.Int32
	reader := &quotaSelectionReader{claude: func(ctx context.Context, _ string) (*usage.Response, error) {
		reads.Add(1)
		close(started)
		<-ctx.Done()
		close(readerDone)
		return nil, ctx.Err()
	}}
	service := newSelectionTestService(t, filepath.Join(t.TempDir(), "app support"), reader, makeQuotaHome(t, "Claude"), "")
	selection := selectionFor(t, readQuotaSelections(t, service), "claude")
	responses := make(chan responseView, 2)
	for i := 0; i < 2; i++ {
		id := "coalesced-" + string(rune('a'+i))
		go func(requestID string) {
			responses <- call(t, service, requestID, "quota.read", map[string]any{
				"provider": "claude", "selection_ref": selection.SelectionRef, "generation": selection.Generation,
			})
		}(id)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider reader did not start")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		service.quotaFlightMu.Lock()
		flight := service.quotaFlights["claude"]
		waiters := 0
		if flight != nil {
			waiters = flight.waiters
		}
		service.quotaFlightMu.Unlock()
		if waiters == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	service.quotaFlightMu.Lock()
	flight := service.quotaFlights["claude"]
	waiters := 0
	if flight != nil {
		waiters = flight.waiters
	}
	service.quotaFlightMu.Unlock()
	if waiters != 2 || reads.Load() != 1 {
		t.Fatalf("coalesced waiters=%d reader calls=%d, want two waiters and one reader", waiters, reads.Load())
	}
	cancel := call(t, service, "cancel", "quota.cancel", map[string]any{
		"provider": "claude", "selection_ref": selection.SelectionRef, "generation": selection.Generation,
	})
	if cancel.Error != nil {
		t.Fatalf("quota.cancel error = %+v", cancel.Error)
	}
	var cancelResult struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.Unmarshal(cancel.Result, &cancelResult); err != nil || !cancelResult.Cancelled {
		t.Fatalf("quota.cancel result=%s err=%v", cancel.Result, err)
	}
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		t.Fatal("quota.cancel did not cancel the provider context")
	}
	for i := 0; i < 2; i++ {
		select {
		case response := <-responses:
			if response.Error != nil {
				t.Fatalf("coalesced read protocol error = %+v", response.Error)
			}
			var result attributedQuotaView
			if err := json.Unmarshal(response.Result, &result); err != nil {
				t.Fatal(err)
			}
			if result.State != "unavailable" || result.Error == nil || result.Error.Code != "request_cancelled" {
				t.Fatalf("cancelled read result = %+v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("coalesced waiter did not return")
		}
	}
	current := selectionFor(t, readQuotaSelections(t, service), "claude")
	if current.Generation != selection.Generation || current.SelectionRef != selection.SelectionRef || reads.Load() != 1 {
		t.Fatalf("cancel changed identity or duplicated work: selection=%+v reads=%d", current, reads.Load())
	}
}

func TestServeConcurrentAttributedReadsKeepProvidersIndependentAndSerializeOutput(t *testing.T) {
	root := t.TempDir()
	claudeHome := makeQuotaHome(t, "Claude")
	codexHome := makeQuotaHome(t, "Codex")
	writeCodexAuth(t, codexHome, `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-only"}}`)
	started := make(chan struct{})
	release := make(chan struct{})
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	reader := &quotaSelectionReader{
		claude: func(context.Context, string) (*usage.Response, error) {
			close(started)
			<-release
			return syntheticClaudeUsage(now, "claude-auth-a", 20), nil
		},
		codex: func(context.Context, string) (*codexappserver.Snapshot, error) {
			return codexSnapshot(now, "private@example.invalid", 30), nil
		},
	}
	service := newSelectionTestServiceWithClock(t, filepath.Join(root, "app support"), reader, claudeHome, codexHome, func() time.Time { return now })
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	serveDone := make(chan error, 1)
	go func() { serveDone <- Serve(context.Background(), service, inputReader, outputWriter) }()
	scanner := bufio.NewScanner(outputReader)
	writeWireRequest(t, inputWriter, "hello", "hello", map[string]bool{"quota_selection": true})
	hello := readWireResponse(t, scanner)
	var helloResult struct {
		Capabilities struct {
			QuotaSelection bool `json:"quota_selection"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(hello.Result, &helloResult); err != nil || !helloResult.Capabilities.QuotaSelection {
		t.Fatalf("negotiated hello=%s err=%v", hello.Result, err)
	}
	writeWireRequest(t, inputWriter, "selections", "quota.selection.read", map[string]any{})
	selectionsResponse := readWireResponse(t, scanner)
	if selectionsResponse.Error != nil {
		t.Fatalf("selection read response = %+v", selectionsResponse.Error)
	}
	var selections quotaSelectionsView
	if err := json.Unmarshal(selectionsResponse.Result, &selections); err != nil {
		t.Fatal(err)
	}
	claude := selectionFor(t, selections, "claude")
	codex := selectionFor(t, selections, "codex")
	writeWireRequest(t, inputWriter, "claude-read", "quota.read", map[string]any{
		"provider": "claude", "selection_ref": claude.SelectionRef, "generation": claude.Generation,
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("slow Claude provider did not start")
	}
	writeWireRequest(t, inputWriter, "codex-read", "quota.read", map[string]any{
		"provider": "codex", "selection_ref": codex.SelectionRef, "generation": codex.Generation,
	})
	codexResponse := readWireResponse(t, scanner)
	if codexResponse.ID != "codex-read" || codexResponse.Error != nil {
		t.Fatalf("fast Codex response = %+v", codexResponse)
	}
	close(release)
	claudeResponse := readWireResponse(t, scanner)
	if claudeResponse.ID != "claude-read" || claudeResponse.Error != nil {
		t.Fatalf("slow Claude response = %+v", claudeResponse)
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve after EOF: %v", err)
	}
	_ = outputReader.Close()
}

func TestServeEOFCancelsAndJoinsOutstandingAttributedRead(t *testing.T) {
	started := make(chan struct{})
	reader := &quotaSelectionReader{claude: func(ctx context.Context, _ string) (*usage.Response, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	service := newSelectionTestService(t, filepath.Join(t.TempDir(), "app support"), reader, makeQuotaHome(t, "Claude"), "")
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	serveDone := make(chan error, 1)
	go func() { serveDone <- Serve(context.Background(), service, inputReader, outputWriter) }()
	scanner := bufio.NewScanner(outputReader)
	writeWireRequest(t, inputWriter, "hello", "hello", map[string]bool{"quota_selection": true})
	_ = readWireResponse(t, scanner)
	writeWireRequest(t, inputWriter, "selections", "quota.selection.read", map[string]any{})
	selectionResponse := readWireResponse(t, scanner)
	selections := quotaSelectionsView{}
	if err := json.Unmarshal(selectionResponse.Result, &selections); err != nil {
		t.Fatal(err)
	}
	selection := selectionFor(t, selections, "claude")
	writeWireRequest(t, inputWriter, "pending-read", "quota.read", map[string]any{
		"provider": "claude", "selection_ref": selection.SelectionRef, "generation": selection.Generation,
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider read did not start before EOF")
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	response := readWireResponse(t, scanner)
	if response.ID != "pending-read" || response.Error != nil {
		t.Fatalf("EOF cancellation response = %+v", response)
	}
	var result attributedQuotaView
	if err := json.Unmarshal(response.Result, &result); err != nil || result.State != "unavailable" || result.Error == nil || result.Error.Code != "request_cancelled" {
		t.Fatalf("EOF quota result=%+v err=%v", result, err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve after orderly EOF: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not join the cancelled quota read")
	}
	_ = outputReader.Close()
}

type quotaSelectionReader struct {
	claude func(context.Context, string) (*usage.Response, error)
	codex  func(context.Context, string) (*codexappserver.Snapshot, error)
}

func (r *quotaSelectionReader) ReadClaudeQuota(ctx context.Context, home string) (*usage.Response, error) {
	if r.claude == nil {
		return nil, nil
	}
	return r.claude(ctx, home)
}

func (r *quotaSelectionReader) ReadCodexQuota(ctx context.Context, home string) (*codexappserver.Snapshot, error) {
	if r.codex == nil {
		return nil, nil
	}
	return r.codex(ctx, home)
}

func newSelectionTestService(t *testing.T, appSupport string, reader ProviderQuotaReader, claudeHome, codexHome string) *Service {
	t.Helper()
	return newSelectionTestServiceWithClock(t, appSupport, reader, claudeHome, codexHome, func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) })
}

func newSelectionTestServiceWithClock(t *testing.T, appSupport string, reader ProviderQuotaReader, claudeHome, codexHome string, now func() time.Time) *Service {
	t.Helper()
	service, err := New(Config{
		AppSupportRoot: appSupport,
		ProviderMode:   true,
		ClaudeHome:     claudeHome,
		CodexHome:      codexHome,
		ProviderReader: reader,
		Now:            now,
	})
	if err != nil {
		t.Fatalf("New provider selection service: %v", err)
	}
	return service
}

func readQuotaSelections(t *testing.T, service *Service) quotaSelectionsView {
	t.Helper()
	return decodeSelections(t, call(t, service, "selection-read", "quota.selection.read", map[string]any{}))
}

func decodeSelections(t *testing.T, response responseView) quotaSelectionsView {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("quota.selection response error = %+v", response.Error)
	}
	var result quotaSelectionsView
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode selections %s: %v", response.Result, err)
	}
	return result
}

func selectionFor(t *testing.T, result quotaSelectionsView, provider string) quotaSelectionTestView {
	t.Helper()
	for _, selection := range result.Selections {
		if selection.Provider == provider {
			return selection
		}
	}
	t.Fatalf("missing %s selection in %+v", provider, result.Selections)
	return quotaSelectionTestView{}
}

func attributedRead(t *testing.T, service *Service, id string, selection quotaSelectionTestView) attributedQuotaView {
	t.Helper()
	response := call(t, service, id, "quota.read", map[string]any{
		"provider": selection.Provider, "selection_ref": selection.SelectionRef, "generation": selection.Generation,
	})
	if response.Error != nil {
		t.Fatalf("attributed quota read error = %+v", response.Error)
	}
	var result attributedQuotaView
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func syntheticClaudeUsage(fetchedAt time.Time, identity string, percent float64) *usage.Response {
	return &usage.Response{
		FetchedAt:        fetchedAt,
		TokenFingerprint: identity,
		FiveHour:         &usage.UsageWindow{Utilization: percent, ResetsAt: fetchedAt.Add(time.Hour).Format(time.RFC3339Nano)},
	}
}

func writeWireRequest(t *testing.T, writer io.Writer, id, op string, payload any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"version": 1, "kind": "request", "id": id, "op": op, "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if _, err := writer.Write(data); err != nil {
		t.Fatalf("write wire request %s: %v", id, err)
	}
}

func readWireResponse(t *testing.T, scanner *bufio.Scanner) responseView {
	t.Helper()
	if !scanner.Scan() {
		t.Fatalf("read protocol response: %v", scanner.Err())
	}
	var response responseView
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		t.Fatalf("decode wire response %q: %v", scanner.Text(), err)
	}
	return response
}
