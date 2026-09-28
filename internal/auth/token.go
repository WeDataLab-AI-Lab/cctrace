package auth

import (
	"crypto/rand"
	"encoding/hex"
)

// GenerateAPIToken creates a per-user API token with "cct_" prefix + 32 hex chars (128-bit).
func GenerateAPIToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "cct_" + hex.EncodeToString(b), nil
}
