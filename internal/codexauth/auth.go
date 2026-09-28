// Package codexauth reads the billing account id Codex records locally.
//
// ~/.codex/auth.json also holds live credentials (access/id/refresh tokens and
// OPENAI_API_KEY). Only the account id ever leaves this package: nothing here
// returns, logs, or embeds token material, so callers cannot forward it into
// sync payloads or daemon logs by accident.
package codexauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// authFile is the subset of auth.json this package parses. The token fields are
// deliberately absent so they are never held in memory here at all.
type authFile struct {
	Tokens struct {
		AccountID string `json:"account_id"`
	} `json:"tokens"`
}

// ReadAccountID returns tokens.account_id from <codexHome>/auth.json: the
// value Codex sends as the ChatGPT-Account-Id header, i.e. the account the usage
// is billed to.
//
// A missing file or a missing account_id returns an empty string with no error:
// a machine that never logged in with ChatGPT is an ordinary state, not a
// failure. Only unreadable or malformed input is an error.
func ReadAccountID(codexHome string) (string, error) {
	path := filepath.Join(codexHome, "auth.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var af authFile
	if err := json.Unmarshal(data, &af); err != nil {
		// json errors carry an offset and at most one character of input, never
		// a token value, so wrapping is safe.
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	return af.Tokens.AccountID, nil
}
