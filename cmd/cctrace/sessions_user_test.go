package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #810: a role=user token gets its own records back whatever --user says. The
// CLI cannot tell from the status, so it compares each record's profile_email.
func runSessionsAgainst(t *testing.T, body, user string, jsonOutput bool) (stdout, stderr string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	saveReadTokenProfile(t, srv.URL, "cct_read")
	return captureOutput(t, func() {
		if err := runSessions(user, "", 20, jsonOutput); err != nil {
			t.Errorf("runSessions: %v", err)
		}
	})
}

const (
	sessionsOwn   = `[{"ts":"2026-01-01T00:00:00Z","record_type":"user","profile_email":"me@example.com","raw":{}}]`
	sessionsMixed = `[{"ts":"2026-01-01T00:00:00Z","record_type":"user","profile_email":"x@example.com","raw":{}},{"ts":"2026-01-01T00:00:01Z","record_type":"user","profile_email":"me@example.com","raw":{}}]`
)

func TestSessionsUserMatchNoWarning(t *testing.T) {
	_, stderr := runSessionsAgainst(t, strings.ReplaceAll(sessionsOwn, "me@", "x@"), "x@example.com", false)
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestSessionsUserMismatchWarns(t *testing.T) {
	for name, body := range map[string]string{"own": sessionsOwn, "mixed": sessionsMixed} {
		stdout, stderr := runSessionsAgainst(t, body, "x@example.com", false)
		if !strings.Contains(stderr, "--user x@example.com") || !strings.Contains(stderr, "본인 세션만") {
			t.Errorf("%s: stderr = %q, want warning naming --user", name, stderr)
		}
		if strings.Contains(stdout, "요청한 --user") {
			t.Errorf("%s: warning leaked to stdout", name)
		}
	}
}

func TestSessionsUserMismatchJSONStdoutPure(t *testing.T) {
	stdout, stderr := runSessionsAgainst(t, sessionsOwn, "x@example.com", true)
	if strings.TrimSpace(stdout) != sessionsOwn {
		t.Errorf("stdout = %q, want the raw response only", stdout)
	}
	if !strings.Contains(stderr, "--user x@example.com") {
		t.Errorf("stderr = %q, want warning", stderr)
	}
}

func TestSessionsNoUserNoWarning(t *testing.T) {
	_, stderr := runSessionsAgainst(t, sessionsMixed, "", false)
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func sessionsRecord(profile string) string {
	return `{"ts":"2026-01-01T00:00:00Z","record_type":"user","profile_email":"` + profile + `","raw":{}}`
}

// 경고 판정은 정확 일치이고 모든 레코드를 본다: 대소문자만 다르거나 비어 있는 profile_email 도 일치가 아니며,
// 일치 레코드가 앞에 있든 뒤에 있든 불일치 하나면 경고한다.
func TestSessionsUserWarnsOnEveryNonExactMatch(t *testing.T) {
	cases := map[string]string{
		"case differs":        "[" + sessionsRecord("X@example.com") + "]",
		"empty profile":       "[" + sessionsRecord("") + "]",
		"mismatch after hit":  "[" + sessionsRecord("x@example.com") + "," + sessionsRecord("me@example.com") + "]",
		"mismatch before hit": "[" + sessionsRecord("me@example.com") + "," + sessionsRecord("x@example.com") + "]",
		"empty after hit":     "[" + sessionsRecord("x@example.com") + "," + sessionsRecord("") + "]",
	}
	for name, body := range cases {
		_, stderr := runSessionsAgainst(t, body, "x@example.com", false)
		if !strings.Contains(stderr, "--user x@example.com") {
			t.Errorf("%s: stderr = %q, want warning", name, stderr)
		}
	}
}
