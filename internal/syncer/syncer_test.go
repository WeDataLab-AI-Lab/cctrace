package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cctrace/internal/sessionlog"
	"cctrace/internal/store"
)

// ---- state.go tests ----

func TestLoadState_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent", "sync-state.json")
	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil State")
	}
	if len(s.Files) != 0 {
		t.Fatalf("expected empty Files map, got %v", s.Files)
	}
}

func TestSetOffsetGetOffset_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-state.json")
	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	const filePath = "/some/session.jsonl"
	if got := s.GetOffset(filePath); got != 0 {
		t.Fatalf("expected 0 for unknown path, got %d", got)
	}

	s.SetOffset(filePath, 1234)
	if got := s.GetOffset(filePath); got != 1234 {
		t.Fatalf("expected 1234, got %d", got)
	}
}

func TestSave_LoadState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync-state.json")

	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	const filePath = "/my/log.jsonl"
	s.SetOffset(filePath, 9999)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s2, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState after save: %v", err)
	}
	if got := s2.GetOffset(filePath); got != 9999 {
		t.Fatalf("expected 9999, got %d", got)
	}
	if got := s2.Files[filePath].LastAttributionSkill; got != "" {
		t.Fatalf("LastAttributionSkill = %q, want empty", got)
	}
}

func TestSave_LoadState_PreservesLastAttributionSkill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync-state.json")

	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	const filePath = "/my/log.jsonl"
	s.SetOffset(filePath, 9999)
	s.Files[filePath].LastAttributionSkill = "playwright-cli"
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s2, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState after save: %v", err)
	}
	if got := s2.Files[filePath].LastAttributionSkill; got != "playwright-cli" {
		t.Fatalf("LastAttributionSkill = %q, want playwright-cli", got)
	}
}

func TestSave_IsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync-state.json")

	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	s.SetOffset("/a.jsonl", 42)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Final file must exist.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected state file to exist: %v", err)
	}
	// Temp file must not be left behind.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Fatal("expected .tmp file to be gone after Save")
	}
}

// ---- client.go tests ----

func TestClient_Send_Success(t *testing.T) {
	// Given
	const (
		token   = "mytoken"
		version = "v1.2.3"
	)
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 3})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, token, version)

	// When
	inserted, err := c.Send(context.Background(), "claude", "user@example.com", "", "hash123", "", ProjectIdentity{}, nil)

	// Then
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if inserted != 3 {
		t.Fatalf("expected 3 inserted, got %d", inserted)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer "+token {
		t.Errorf("Authorization = %q, want Bearer %s", got, token)
	}
	if got := gotHeaders.Get("X-Cctrace-Version"); got != version {
		t.Errorf("X-Cctrace-Version = %q, want %q", got, version)
	}
	if got := gotHeaders.Get("X-Cctrace-Os"); got != runtime.GOOS {
		t.Errorf("X-Cctrace-Os = %q, want runtime.GOOS %q", got, runtime.GOOS)
	}
	if got := gotHeaders.Get("X-Cctrace-Arch"); got != runtime.GOARCH {
		t.Errorf("X-Cctrace-Arch = %q, want runtime.GOARCH %q", got, runtime.GOARCH)
	}
}

func TestClient_SendsRuntimePlatformHeaders_whenReenriching(t *testing.T) {
	// Given
	const version = "v1.2.3"
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/capabilities" {
			_ = json.NewEncoder(w).Encode(syncCapabilities{Reenrich: true})
			return
		}
		if r.URL.Path != "/api/sync" {
			t.Fatalf("path = %q, want /api/sync", r.URL.Path)
		}
		gotHeaders = r.Header.Clone()
		_ = json.NewEncoder(w).Encode(syncResponse{Updated: 1})
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "", version)

	// When
	if _, err := client.SendReenrich(context.Background(), SyncPayload{Agent: "claude", ProfileEmail: "user@example.com", UserID: "u1"}); err != nil {
		t.Fatalf("SendReenrich: %v", err)
	}

	// Then
	if got := gotHeaders.Get("X-Cctrace-Version"); got != version {
		t.Errorf("X-Cctrace-Version = %q, want %q", got, version)
	}
	if got := gotHeaders.Get("X-Cctrace-Os"); got != runtime.GOOS {
		t.Errorf("X-Cctrace-Os = %q, want runtime.GOOS %q", got, runtime.GOOS)
	}
	if got := gotHeaders.Get("X-Cctrace-Arch"); got != runtime.GOARCH {
		t.Errorf("X-Cctrace-Arch = %q, want runtime.GOARCH %q", got, runtime.GOARCH)
	}
}

func TestClient_Send_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "")
	_, err := c.Send(context.Background(), "claude", "user@example.com", "", "hash123", "", ProjectIdentity{}, nil)
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

func TestClient_Send_InvalidJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{bad json"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "")
	_, err := c.Send(context.Background(), "claude", "user@example.com", "", "hash123", "", ProjectIdentity{}, nil)
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("error = %v, want JSON syntax error", err)
	}
}

func TestClient_SendReenrich_setsRequestMode(t *testing.T) {
	var got SyncPayload
	capabilityRequests := 0
	syncRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/sync/capabilities" {
			capabilityRequests++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(syncCapabilities{Reenrich: true})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/sync" {
			t.Fatalf("request = %s %s, want POST /api/sync", r.Method, r.URL.Path)
		}
		syncRequests++
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 0, Updated: 1})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "")
	updated, err := c.SendReenrich(context.Background(), SyncPayload{
		Agent:        "claude",
		ProfileEmail: "user@example.com",
		UserID:       "u1",
		Records: []*store.SessionRecord{{
			SessionID:  "legacy-s1",
			RecordType: "assistant",
			UUID:       "turn-1",
			SourceFile: "session.jsonl",
		}},
	})
	if err != nil {
		t.Fatalf("SendReenrich: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}
	if !got.Reenrich {
		t.Fatal("reenrich flag was not sent")
	}
	if len(got.Records) != 1 || got.Records[0].UUID != "turn-1" {
		t.Fatalf("records = %+v, want one enriched record", got.Records)
	}
	if _, err := c.SendReenrich(context.Background(), SyncPayload{
		Agent:        "claude",
		ProfileEmail: "user@example.com",
		UserID:       "u1",
		Records: []*store.SessionRecord{{
			SessionID:  "legacy-s1",
			RecordType: "assistant",
			UUID:       "turn-2",
		}},
	}); err != nil {
		t.Fatalf("second SendReenrich: %v", err)
	}
	if capabilityRequests != 1 || syncRequests != 2 {
		t.Fatalf("capabilityRequests/syncRequests = %d/%d, want 1/2", capabilityRequests, syncRequests)
	}
}

func TestClient_SendReenrich_rejectsUnsupportedServerBeforePost(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/sync/capabilities" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/sync" {
			posted = true
			http.Error(w, "unexpected post", http.StatusInternalServerError)
			return
		}
		t.Fatalf("request = %s %s, want capabilities probe", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "")
	_, err := c.SendReenrich(context.Background(), SyncPayload{
		Agent:        "claude",
		ProfileEmail: "user@example.com",
		UserID:       "u1",
		Records: []*store.SessionRecord{{
			SessionID:  "legacy-s1",
			RecordType: "assistant",
			UUID:       "turn-1",
		}},
	})
	if !errors.Is(err, ErrReenrichUnsupported) {
		t.Fatalf("SendReenrich error = %v, want ErrReenrichUnsupported", err)
	}
	if posted {
		t.Fatal("SendReenrich posted reenrich payload to unsupported server")
	}
}

func TestClient_SendReenrich_capabilityRateLimitIsRetryable(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/sync/capabilities" {
			w.Header().Set("Retry-After", "7")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/sync" {
			posted = true
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "")
	_, err := c.SendReenrich(context.Background(), SyncPayload{
		Agent:        "claude",
		ProfileEmail: "user@example.com",
		UserID:       "u1",
		Records: []*store.SessionRecord{{
			SessionID:  "legacy-s1",
			RecordType: "assistant",
			UUID:       "turn-1",
		}},
	})
	var retryErr *RetryableError
	if !errors.As(err, &retryErr) {
		t.Fatalf("SendReenrich error = %v, want RetryableError", err)
	}
	if retryErr.RetryAfter != 7*time.Second {
		t.Fatalf("RetryAfter = %v, want 7s", retryErr.RetryAfter)
	}
	if posted {
		t.Fatal("SendReenrich posted reenrich payload after rate-limited capability probe")
	}
}

func TestClient_Send_BadURL(t *testing.T) {
	c := NewClient("http://127.0.0.1:0", "tok", "")
	_, err := c.Send(context.Background(), "claude", "user@example.com", "", "hash123", "", ProjectIdentity{}, nil)
	if err == nil {
		t.Fatal("expected error for bad URL, got nil")
	}
}

func TestClient_SendProjectRules(t *testing.T) {
	const token = "mytoken"
	var gotAuth string
	var gotPath string
	var got store.ProjectRuleIngestRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(store.ProjectRuleIngestResponse{
			InsertedRules:    1,
			InsertedVersions: 1,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, token, "")
	resp, err := c.SendProjectRules(context.Background(), &store.ProjectRuleIngestRequest{
		ProfileEmail:  "user@example.com",
		UserID:        "u1",
		Agent:         "codex",
		ProjectHash:   "proj",
		RepositoryID:  "github.com/org/repo",
		RepositoryKey: "github.com/org/repo",
		Rules: []*store.ProjectRuleSnapshot{{
			RulePath: "AGENTS.md",
			RuleKind: "agents",
			Status:   "active",
			Content:  "# Rules\n",
		}},
	})
	if err != nil {
		t.Fatalf("SendProjectRules returned error: %v", err)
	}
	if gotPath != "/api/project-rules" {
		t.Fatalf("path = %q, want /api/project-rules", gotPath)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want Bearer token", gotAuth)
	}
	if got.Agent != "codex" || got.Rules[0].RulePath != "AGENTS.md" {
		t.Fatalf("unexpected request: %+v", got)
	}
	if resp.InsertedRules != 1 || resp.InsertedVersions != 1 {
		t.Fatalf("response = %+v, want inserted rule/version", resp)
	}
}

func TestClient_SendProjectRules_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	_, err := c.SendProjectRules(context.Background(), &store.ProjectRuleIngestRequest{
		RepositoryKey: "github.com/org/repo",
		Rules:         []*store.ProjectRuleSnapshot{{RulePath: "CLAUDE.md", Status: "active"}},
	})
	if !errors.Is(err, ErrProjectRulesForbidden) {
		t.Fatalf("err = %v, want ErrProjectRulesForbidden", err)
	}
}

func TestClient_SendProjectRules_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	_, err := c.SendProjectRules(context.Background(), &store.ProjectRuleIngestRequest{
		RepositoryKey: "github.com/org/repo",
		Rules:         []*store.ProjectRuleSnapshot{{RulePath: "CLAUDE.md", Status: "active"}},
	})
	if !errors.Is(err, ErrProjectRulesUnsupported) {
		t.Fatalf("err = %v, want ErrProjectRulesUnsupported", err)
	}
}

// ---- syncer.go tests ----

func makeAssistantRecord(model string, input, output, cacheRead, cacheCreate int) *sessionlog.Record {
	msg := sessionlog.AssistantMessage{
		Model: model,
		Usage: sessionlog.TokenUsage{
			InputTokens:              input,
			OutputTokens:             output,
			CacheReadInputTokens:     cacheRead,
			CacheCreationInputTokens: cacheCreate,
		},
	}
	raw, _ := json.Marshal(msg)
	return &sessionlog.Record{
		Type:      "assistant",
		Timestamp: time.Now(),
		SessionID: "sess-1",
		Message:   json.RawMessage(raw),
	}
}

func TestToStoreRecord_AssistantWithTokens(t *testing.T) {
	r := makeAssistantRecord("claude-3", 100, 200, 50, 25)
	sr := toStoreRecord(r, "user@test.com", "", "proj-hash")
	if sr == nil {
		t.Fatal("expected non-nil SessionRecord")
	}
	if sr.RecordType != "assistant" {
		t.Errorf("RecordType: want 'assistant', got %q", sr.RecordType)
	}
	if sr.Model != "claude-3" {
		t.Errorf("Model: want 'claude-3', got %q", sr.Model)
	}
	if sr.ProfileEmail != "user@test.com" {
		t.Errorf("ProfileEmail: want 'user@test.com', got %q", sr.ProfileEmail)
	}
	if sr.ProjectHash != "proj-hash" {
		t.Errorf("ProjectHash: want 'proj-hash', got %q", sr.ProjectHash)
	}

	assertIntPtr := func(name string, ptr *int, want int) {
		t.Helper()
		if ptr == nil {
			t.Errorf("%s: expected non-nil pointer", name)
			return
		}
		if *ptr != want {
			t.Errorf("%s: want %d, got %d", name, want, *ptr)
		}
	}
	assertIntPtr("InputTokens", sr.InputTokens, 100)
	assertIntPtr("OutputTokens", sr.OutputTokens, 200)
	assertIntPtr("CacheReadTokens", sr.CacheReadTokens, 50)
	assertIntPtr("CacheCreateTokens", sr.CacheCreateTokens, 25)
}

func TestToStoreRecord_AssistantZeroTokens(t *testing.T) {
	// All token counts zero: token pointer fields should remain nil.
	r := makeAssistantRecord("claude-3", 0, 0, 0, 0)
	sr := toStoreRecord(r, "u@x.com", "", "h")
	if sr == nil {
		t.Fatal("expected non-nil SessionRecord")
	}
	if sr.InputTokens != nil {
		t.Errorf("InputTokens should be nil for zero value, got %d", *sr.InputTokens)
	}
	if sr.OutputTokens != nil {
		t.Errorf("OutputTokens should be nil for zero value, got %d", *sr.OutputTokens)
	}
}

func TestToStoreRecord_NonAssistantType(t *testing.T) {
	r := &sessionlog.Record{
		Type:      "user",
		Timestamp: time.Now(),
		SessionID: "sess-2",
		Message:   json.RawMessage(`{"text":"hello"}`),
	}
	sr := toStoreRecord(r, "user@test.com", "", "proj-hash")
	if sr == nil {
		t.Fatal("expected non-nil SessionRecord for user record")
	}
	if sr.RecordType != "user" {
		t.Errorf("RecordType: want 'user', got %q", sr.RecordType)
	}
	// Token fields must not be set for non-assistant records.
	if sr.Model != "" {
		t.Errorf("Model should be empty for non-assistant record, got %q", sr.Model)
	}
	if sr.InputTokens != nil || sr.OutputTokens != nil {
		t.Error("token pointers should be nil for non-assistant record")
	}
}

func TestAttributionSkillInvocationName(t *testing.T) {
	active := ""
	realUser := &sessionlog.Record{
		Type:    "user",
		Message: json.RawMessage(`{"role":"user","content":"use playwright"}`),
	}
	firstAssistant := &sessionlog.Record{Type: "assistant", AttributionSkill: "playwright-cli"}
	sameAssistant := &sessionlog.Record{Type: "assistant", AttributionSkill: "playwright-cli"}
	toolResult := &sessionlog.Record{
		Type:    "user",
		Message: json.RawMessage(`{"role":"user","content":[{"type":"tool_result","content":"ok"}]}`),
	}
	afterTool := &sessionlog.Record{Type: "assistant", AttributionSkill: "playwright-cli"}

	if got := attributionSkillInvocationName(realUser, &active); got != "" {
		t.Fatalf("real user invocation = %q, want empty", got)
	}
	if got := attributionSkillInvocationName(firstAssistant, &active); got != "playwright-cli" {
		t.Fatalf("first assistant invocation = %q, want playwright-cli", got)
	}
	if got := attributionSkillInvocationName(sameAssistant, &active); got != "" {
		t.Fatalf("same assistant invocation = %q, want empty", got)
	}
	if got := attributionSkillInvocationName(toolResult, &active); got != "" {
		t.Fatalf("tool result invocation = %q, want empty", got)
	}
	if got := attributionSkillInvocationName(afterTool, &active); got != "" {
		t.Fatalf("after tool invocation = %q, want empty", got)
	}
	if got := attributionSkillInvocationName(realUser, &active); got != "" {
		t.Fatalf("next real user invocation = %q, want empty", got)
	}
	if got := attributionSkillInvocationName(firstAssistant, &active); got != "playwright-cli" {
		t.Fatalf("new turn invocation = %q, want playwright-cli", got)
	}
}

func TestToStoreRecord_CommandOverride(t *testing.T) {
	r := makeAssistantRecord("claude-3", 100, 200, 0, 0)
	sr := toStoreRecord(r, "u@x.com", "uid", "h", "playwright-cli")
	if sr == nil {
		t.Fatal("expected non-nil SessionRecord")
	}
	if sr.CommandName != "playwright-cli" {
		t.Fatalf("CommandName = %q, want playwright-cli", sr.CommandName)
	}
}

// A record with no timestamp used to be stamped with time.Now(). That put the clock
// into its storage key, so reading the same line twice produced two different keys
// and ON CONFLICT never fired -- one duplicate row per metadata line per rescan.
// The timestamp now comes from the line, which for these lines means a fixed
// instant; the identity that tells them apart lives in the uuid instead.
func TestToStoreRecord_ZeroTimestampIsStableNotNow(t *testing.T) {
	mk := func() *sessionlog.Record {
		return &sessionlog.Record{
			Type:      "system",
			Timestamp: time.Time{}, // zero
			SessionID: "sess-3",
			RawLine:   []byte(`{"type":"system","sessionId":"sess-3"}`),
		}
	}
	first := toStoreRecord(mk(), "u@x.com", "", "h")
	time.Sleep(2 * time.Millisecond)
	second := toStoreRecord(mk(), "u@x.com", "", "h")

	if first == nil || second == nil {
		t.Fatal("expected non-nil SessionRecord")
	}
	if !first.Ts.Equal(second.Ts) {
		t.Errorf("two reads produced different timestamps: %v vs %v", first.Ts, second.Ts)
	}
	if first.UUID != second.UUID || first.UUID == "" {
		t.Errorf("two reads produced different identities: %q vs %q", first.UUID, second.UUID)
	}
}

func TestSyncer_ReenrichOnce_scansWholeFileWithoutAdvancingState(t *testing.T) {
	var got SyncPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/sync/capabilities" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(syncCapabilities{Reenrich: true})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/sync" {
			t.Fatalf("request = %s %s, want POST /api/sync", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 0, Updated: 2})
	}))
	defer srv.Close()

	cwd := t.TempDir()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	first := `{"type":"user","timestamp":"2026-07-06T10:00:00Z","sessionId":"legacy-s1","uuid":"turn-1","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hi"}}` + "\n"
	second := `{"type":"assistant","timestamp":"2026-07-06T10:00:01Z","sessionId":"legacy-s1","uuid":"turn-2","parentUuid":"turn-1","cwd":` + strconvQuote(cwd) + `,"message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input_tokens":7,"output_tokens":3},"content":[{"type":"text","text":"hello"}]}}` + "\n"
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	if err := os.WriteFile(sessionPath, []byte(first+second), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.SetOffset(sessionPath, int64(len(first)))

	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	n, err := s.ReenrichOnce(context.Background())
	if err != nil {
		t.Fatalf("ReenrichOnce: %v", err)
	}
	if n != 2 {
		t.Fatalf("reenriched records = %d, want 2", n)
	}
	if !got.Reenrich {
		t.Fatal("reenrich flag was not sent")
	}
	if len(got.Records) != 2 {
		t.Fatalf("request records = %d, want 2", len(got.Records))
	}
	if got.Records[0].SourceFile != "session.jsonl" || got.Records[1].ParentUUID != "turn-1" {
		t.Fatalf("enrichment fields missing: %+v", got.Records)
	}
	if got.Records[1].Model != "claude-sonnet-4-20250514" {
		t.Fatalf("model = %q, want claude-sonnet-4-20250514", got.Records[1].Model)
	}
	if state.GetOffset(sessionPath) != int64(len(first)) {
		t.Fatalf("state offset changed to %d, want %d", state.GetOffset(sessionPath), len(first))
	}
}

func TestSyncer_ReenrichOnce_returnsUnsupportedServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/sync/capabilities" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/sync" {
			t.Fatal("unsupported server must not receive reenrich POST")
		}
	}))
	defer srv.Close()

	cwd := t.TempDir()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	line := `{"type":"user","timestamp":"2026-07-06T10:00:00Z","sessionId":"legacy-s1","uuid":"turn-1","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	_, err = s.ReenrichOnce(context.Background())
	if !errors.Is(err, ErrReenrichUnsupported) {
		t.Fatalf("ReenrichOnce error = %v, want ErrReenrichUnsupported", err)
	}
}

func TestSyncOnce_SkipsAllExistingFilesOnFirstRun(t *testing.T) {
	var syncCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/sync" {
			syncCalls++
		}
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	defer srv.Close()

	// Non-git working dir keeps the test focused on the first-run skip (no
	// project-rule scan of a real repo tree).
	cwd := t.TempDir()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"s","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hi"}}` + "\n"
	for _, name := range []string{"a.jsonl", "b.jsonl", "c.jsonl"} {
		if err := os.WriteFile(filepath.Join(sessionDir, name), []byte(line), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// Fresh state file (does not exist) -> first run.
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 0 {
		t.Fatalf("first run must skip ALL existing files, synced %d", n)
	}
	if syncCalls != 0 {
		t.Fatalf("first run must make no /api/sync calls, got %d", syncCalls)
	}
}

// TestSyncOnce_LogsSkippedFilesOnFirstRun: the first-run skip is deliberate — it
// stops a new install from backfilling every old session — but it is also the
// path a brand-new user's own first session takes, because `cctrace init` is
// normally run from inside a live Claude Code session and the daemon starts
// while that session's file is already on disk. Everything written before that
// moment is dropped, including the human's opening turn, and the session still
// uploads: the user sees a truncated session with no sign anything is missing.
// Worse, the lost turn is the one carrying prompt_source=typed, so the session
// then reads as having no genuine human turn and the dashboard files it under
// headless — out of the default view entirely.
//
// The skip stays. What must not stay is its silence: the shrunk-file branch a
// few lines below already logs for exactly this reason.
func TestSyncOnce_LogsSkippedFilesOnFirstRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	defer srv.Close()

	cwd := t.TempDir()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"s","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hi"}}` + "\n"
	path := filepath.Join(sessionDir, "a.jsonl")
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 1): %v", err)
	}
	// Second pass: the file is in state now, so it is no longer a skip. Repeating
	// the notice every 30s would turn it into noise, and noise is how a real
	// notice stops being read.
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 2): %v", err)
	}

	out := buf.String()
	if n := strings.Count(out, "skipped (pre-existing"); n != 1 {
		t.Fatalf("expected exactly 1 skip log across 2 passes, got %d; log:\n%s", n, out)
	}
	// The byte count is what makes this read as data loss rather than as
	// bookkeeping: "a file was skipped" invites a shrug, "12KB was skipped" does not.
	if !strings.Contains(out, fmt.Sprintf("bytes=%d", len(line))) {
		t.Fatalf("skip log does not report how much was skipped; log:\n%s", out)
	}
	if !strings.Contains(out, path) {
		t.Fatalf("skip log does not name the file; log:\n%s", out)
	}
}

// TestSyncOnce_RecordsSkippedBytesInState: the log line from the skip is written
// once per file and sync.log rotates at 10MB, so on an active machine the only
// trace of a first-run skip eventually ages out. The state file is where the fact
// has to live — it is already the thing that knows the offset jumped, it never
// rotates, and `cctrace status` can read it to tell the user what is missing.
func TestSyncOnce_RecordsSkippedBytesInState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	defer srv.Close()

	cwd := t.TempDir()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"s","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hi"}}` + "\n"
	path := filepath.Join(sessionDir, "a.jsonl")
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	statePath := filepath.Join(t.TempDir(), "state.json")
	state, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	// Reload from disk: an in-memory field that never reaches the file would be
	// gone by the time the user runs `cctrace status` in another process.
	reloaded, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState after sync: %v", err)
	}
	fs := reloaded.Files[path]
	if fs == nil {
		t.Fatalf("state has no entry for %s", path)
	}
	if fs.SkippedAtFirstSync != int64(len(line)) {
		t.Fatalf("SkippedAtFirstSync = %d, want %d", fs.SkippedAtFirstSync, len(line))
	}

	// A later pass collects normally and must not erase the record. The content
	// skipped at first sync stays missing no matter how much arrives afterwards.
	if err := os.WriteFile(path, []byte(line+line), 0o644); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 2): %v", err)
	}
	reloaded, err = LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState after pass 2: %v", err)
	}
	if got := reloaded.Files[path].SkippedAtFirstSync; got != int64(len(line)) {
		t.Fatalf("SkippedAtFirstSync after later sync = %d, want %d preserved", got, len(line))
	}
}

// TestSyncOnce_FirstRunWindowClosesWithNoFiles: the first-run skip exists to stop
// a new install from backfilling history that predates it. That justification
// expires the moment the first pass ends — anything appearing afterwards is new
// and must be collected in full.
//
// It did not expire. The flag is state.IsNew(), and the state file was only ever
// written from inside syncFile, so a pass that found nothing to sync wrote
// nothing and left the state "new" forever. The next pass then treated the first
// session it ever saw as pre-existing history and skipped it whole.
//
// The users this hits are the ones who did everything right: a clean machine, or
// `cctrace init` run before starting an agent. Their very first session is the
// one that disappears, and the log says "pre-existing" about a file that did not
// exist when the first sync ran.
func TestSyncOnce_FirstRunWindowClosesWithNoFiles(t *testing.T) {
	var received int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync" {
			var body struct {
				Records []json.RawMessage `json:"records"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			received += len(body.Records)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	defer srv.Close()

	cwd := t.TempDir()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	statePath := filepath.Join(t.TempDir(), "state.json")
	state, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)

	// Pass 1: nothing on disk yet. This is the whole first run.
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (empty pass): %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state file was not written after the first pass: %v", err)
	}

	// A session starts now — after the first run, so it is not history.
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"s","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hi"}}` + "\n"
	path := filepath.Join(sessionDir, "a.jsonl")
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (second pass): %v", err)
	}
	if received == 0 {
		t.Fatal("a session created after the first run was skipped as pre-existing history")
	}
	if fs := state.Files[path]; fs != nil && fs.SkippedAtFirstSync != 0 {
		t.Errorf("SkippedAtFirstSync = %d, want 0 — this file did not exist at first sync", fs.SkippedAtFirstSync)
	}
}

func TestSyncOnce_StopsOnCancelledContext(t *testing.T) {
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"s1","cwd":"/tmp/repo","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionDir, "s.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	s := New(claudeDir, "u@example.com", "u1", state, NewClient("http://127.0.0.1:0", "", ""), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // graceful stop: context already cancelled before the scan loop

	if _, err := s.SyncOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("SyncOnce err = %v, want context.Canceled", err)
	}
}

func TestSyncOnce_SendsProjectRulesFromSessionCWD(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# Claude Rules\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}
	initGitRepo(t, repo)

	var ruleReq *store.ProjectRuleIngestRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync":
			_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
		case "/api/project-rules":
			var req store.ProjectRuleIngestRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode project rules: %v", err)
			}
			ruleReq = &req
			_ = json.NewEncoder(w).Encode(store.ProjectRuleIngestResponse{InsertedRules: 1, InsertedVersions: 1})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"sess-1","cwd":` + strconvQuote(repo) + `,"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.SetOffset(sessionPath, 0)
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("synced records = %d, want 1", n)
	}
	if ruleReq == nil {
		t.Fatal("project rule ingest request was not sent")
	}
	if ruleReq.Agent != "claude" {
		t.Fatalf("agent = %q, want claude", ruleReq.Agent)
	}
	if ruleReq.RepositoryID != "github.com/org/rules" {
		t.Fatalf("repository_id = %q, want github.com/org/rules", ruleReq.RepositoryID)
	}
	if ruleReq.RepositoryKey != "github.com/org/rules" {
		t.Fatalf("repository_key = %q, want github.com/org/rules", ruleReq.RepositoryKey)
	}
	if ruleReq.CommitSHA == "" {
		t.Fatal("commit_sha is empty")
	}
	if ruleReq.Branch != "main" {
		t.Fatalf("branch = %q, want main", ruleReq.Branch)
	}
	if len(ruleReq.Rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(ruleReq.Rules))
	}
	if ruleReq.Rules[0].RulePath != "CLAUDE.md" || ruleReq.Rules[0].Status != "active" {
		t.Fatalf("unexpected rule snapshot: %+v", ruleReq.Rules[0])
	}
}

// TestSyncOnce_ProjectRules403_NotRetriedWithinTTL verifies that a 403 from the
// project-rules endpoint is cached per repository: two session files pointing at
// the same repo must trigger only ONE project-rules request, not one per file.
// Before the fix, 403 was not cached and every file re-sent the request.
func TestSyncOnce_ProjectRules403_NotRetriedWithinTTL(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# Claude Rules\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}
	initGitRepo(t, repo)

	var ruleReqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
		case "/api/project-rules":
			ruleReqCount++
			w.WriteHeader(http.StatusForbidden)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for _, name := range []string{"session1.jsonl", "session2.jsonl"} {
		p := filepath.Join(sessionDir, name)
		line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"` + name + `","cwd":` + strconvQuote(repo) + `,"message":{"role":"user","content":"hello"}}` + "\n"
		if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
			t.Fatalf("write session: %v", err)
		}
		state.SetOffset(p, 0)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if ruleReqCount != 1 {
		t.Fatalf("project-rules requests = %d, want 1 (403 must be cached per repo)", ruleReqCount)
	}
}

func TestSyncOnce_PropagatesContextCancellationDuringSend(t *testing.T) {
	entered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sync" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		close(entered)
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer srv.Close()

	claudeDir := t.TempDir()
	repo := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"cancel-1","cwd":` + strconvQuote(repo) + `,"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.SetOffset(sessionPath, 0)
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	_, err = s.SyncOnce(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/org/rules.git")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "branch", "-M", "main")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, string(out))
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A shrunk session file is deliberately NOT rewound, matching codexsyncer.
//
// Rewinding re-sends bytes, which is only safe when every record's storage
// conflict key (session_id, ts, record_type, profile_email, uuid) comes from its
// content. Claude conversation records do carry a uuid and a timestamp, but the
// metadata lines do not: last-prompt, mode, ai-title, permission-mode,
// agent-name, file-history-snapshot and friends have neither, and toStoreRecord
// stamps those with time.Now(). A rescan gives them a new ts, the conflict key
// misses, and ON CONFLICT DO NOTHING never fires — one duplicate row per
// metadata line per rescan. A sample of 80 real session files (164,677 lines)
// found 15.2% of lines in that shape, and the duplication was reproduced
// against TimescaleDB.
//
// So a shrunk file stalls: it stops collecting new records, which loses nothing
// already stored, rather than duplicating what is. Fixing it needs a stable
// identity for those records — the problem PR #131 closed on the Codex side.
func TestSyncer_SyncOnce_DoesNotRewindShrunkFile(t *testing.T) {
	received := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync" {
			var body struct {
				Records []json.RawMessage `json:"records"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			received += len(body.Records)
		}
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	defer srv.Close()

	cwd := t.TempDir()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(sessionDir, "a.jsonl")

	line := func(text string) string {
		return `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"s","cwd":` +
			strconvQuote(cwd) + `,"message":{"role":"user","content":"` + text + `"}}` + "\n"
	}

	long := line("one") + line("two") + line("three")
	if err := os.WriteFile(path, []byte(long), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.SetOffset(path, int64(len(long)))

	short := line("fresh")
	if err := os.WriteFile(path, []byte(short), 0o644); err != nil {
		t.Fatalf("rewrite shorter: %v", err)
	}

	s := New(claudeDir, "user@test.local", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	if received != 0 {
		t.Fatalf("a shrunk file must not be re-sent; got %d record(s)", received)
	}
	// The offset drops to the file's current size rather than to zero: the
	// bytes still on disk were either already sent (a truncation) or are the
	// metadata remnant of a rewrite, so re-reading them would duplicate rows.
	// Leaving it at the stored value is what stranded later growth.
	if got := state.GetOffset(path); got != int64(len(short)) {
		t.Fatalf("offset = %d, want the current size %d (reset, not rewind)", got, len(short))
	}
}

// TestSyncer_SyncOnce_ResumesAfterShrunkFileGrowsAgain covers the loss this
// reset exists to prevent: a session file rewritten smaller and then resumed.
// Stalling at the stored offset strands every byte written until the file
// climbs back past it, and the log says only "stalled" while that happens.
func TestSyncer_SyncOnce_ResumesAfterShrunkFileGrowsAgain(t *testing.T) {
	var body struct {
		Records []json.RawMessage `json:"records"`
	}
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync" {
			body.Records = nil
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, raw := range body.Records {
				sent = append(sent, string(raw))
			}
		}
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	defer srv.Close()

	cwd := t.TempDir()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(sessionDir, "a.jsonl")

	line := func(text string) string {
		return `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"s","cwd":` +
			strconvQuote(cwd) + `,"message":{"role":"user","content":"` + text + `"}}` + "\n"
	}

	long := line("one") + line("two") + line("three")
	if err := os.WriteFile(path, []byte(long), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.SetOffset(path, int64(len(long)))

	// The rewrite: the file is replaced by a much smaller one, as a session
	// file is when its conversation body is dropped for a metadata remnant.
	short := line("remnant")
	if err := os.WriteFile(path, []byte(short), 0o644); err != nil {
		t.Fatalf("rewrite shorter: %v", err)
	}

	s := New(claudeDir, "user@test.local", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (shrunk): %v", err)
	}
	if len(sent) != 0 {
		t.Fatalf("the shrunk file must not be re-sent; got %d record(s)", len(sent))
	}

	// The session resumes. This content is new and has never been sent, yet it
	// sits below the stored offset.
	if err := os.WriteFile(path, []byte(short+line("resumed")), 0o644); err != nil {
		t.Fatalf("append after shrink: %v", err)
	}
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (regrown): %v", err)
	}

	var found bool
	for _, raw := range sent {
		if strings.Contains(raw, "resumed") {
			found = true
		}
		if strings.Contains(raw, "remnant") {
			t.Fatalf("content already on disk at reset time must not be sent: %s", raw)
		}
	}
	if !found {
		t.Fatalf("content written after the shrink was never collected; sent %d record(s): %v", len(sent), sent)
	}
}

// TestSyncer_SyncOnce_LogsShrunkFileOnceThenSuppresses verifies the shrunk-file
// reset is logged (so it is distinguishable from a quiet file) and that the log
// fires only once across repeated passes, not on every poll.
func TestSyncer_SyncOnce_LogsShrunkFileOnceThenSuppresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	defer srv.Close()

	cwd := t.TempDir()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(sessionDir, "a.jsonl")

	line := func(text string) string {
		return `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"s","cwd":` +
			strconvQuote(cwd) + `,"message":{"role":"user","content":"` + text + `"}}` + "\n"
	}

	long := line("one") + line("two") + line("three")
	if err := os.WriteFile(path, []byte(long), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.SetOffset(path, int64(len(long)))

	short := line("fresh")
	if err := os.WriteFile(path, []byte(short), 0o644); err != nil {
		t.Fatalf("rewrite shorter: %v", err)
	}

	s := New(claudeDir, "user@test.local", "u1", state, NewClient(srv.URL, "", ""), nil)

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 1): %v", err)
	}
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 2): %v", err)
	}

	out := buf.String()
	count := strings.Count(out, "shrunk, offset reset")
	if count != 1 {
		t.Fatalf("expected exactly 1 stall log across 2 passes, got %d; log:\n%s", count, out)
	}
}
