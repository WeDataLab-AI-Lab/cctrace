package codexauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAuth(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(content), 0600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
}

func TestReadAccountIDReturnsAccountID(t *testing.T) {
	dir := t.TempDir()
	writeAuth(t, dir, `{"tokens":{"account_id":"019db82c-66c5-7160-a6a7-b76dc2dd72c5"}}`)

	got, err := ReadAccountID(dir)
	if err != nil {
		t.Fatalf("ReadAccountID: %v", err)
	}
	if got != "019db82c-66c5-7160-a6a7-b76dc2dd72c5" {
		t.Fatalf("account id = %q", got)
	}
}

// A missing auth.json is the ordinary state of a machine that never logged in
// with ChatGPT, not a failure: it must not surface as an error the sync pass
// has to log every cycle.
func TestReadAccountIDMissingFileIsEmptyNotError(t *testing.T) {
	got, err := ReadAccountID(t.TempDir())
	if err != nil {
		t.Fatalf("ReadAccountID: %v", err)
	}
	if got != "" {
		t.Fatalf("account id = %q, want empty", got)
	}
}

func TestReadAccountIDMissingFieldIsEmptyNotError(t *testing.T) {
	dir := t.TempDir()
	writeAuth(t, dir, `{"tokens":{"access_token":"secret-access"}}`)

	got, err := ReadAccountID(dir)
	if err != nil {
		t.Fatalf("ReadAccountID: %v", err)
	}
	if got != "" {
		t.Fatalf("account id = %q, want empty", got)
	}
}

func TestReadAccountIDMalformedJSONIsError(t *testing.T) {
	dir := t.TempDir()
	writeAuth(t, dir, `{"tokens":`)

	if _, err := ReadAccountID(dir); err == nil {
		t.Fatal("expected malformed auth.json to error")
	}
}

// Only the account id may leave this package. auth.json holds live credentials,
// and a value or error message that carried them would put them into sync
// payloads and daemon logs.
func TestReadAccountIDNeverLeaksTokenMaterial(t *testing.T) {
	secrets := []string{"secret-access", "secret-id", "secret-refresh", "secret-api-key"}
	dir := t.TempDir()
	writeAuth(t, dir, `{
		"OPENAI_API_KEY":"secret-api-key",
		"tokens":{
			"id_token":"secret-id",
			"access_token":"secret-access",
			"refresh_token":"secret-refresh",
			"account_id":"acct-1"
		},
		"last_refresh":"2026-08-13T00:00:00Z"
	}`)

	got, err := ReadAccountID(dir)
	if err != nil {
		t.Fatalf("ReadAccountID: %v", err)
	}
	if got != "acct-1" {
		t.Fatalf("account id = %q", got)
	}
	for _, s := range secrets {
		if strings.Contains(got, s) {
			t.Fatalf("returned value leaked %q", s)
		}
	}

	// Same file, now truncated mid-token so the parse fails with the secrets in
	// the buffer: the error must not carry them either.
	writeAuth(t, dir, `{"tokens":{"access_token":"secret-access`)
	_, err = ReadAccountID(dir)
	if err == nil {
		t.Fatal("expected malformed auth.json to error")
	}
	for _, s := range secrets {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaked %q: %v", s, err)
		}
	}
}
