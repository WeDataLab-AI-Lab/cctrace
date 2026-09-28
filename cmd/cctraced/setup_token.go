package main

import (
	"crypto/rand"
	"encoding/base64"
)

func resolveSetupToken(envValue string, userCount int) (token string, generated bool, err error) {
	if userCount != 0 {
		return "", false, nil
	}
	if envValue != "" {
		return envValue, false, nil
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", false, err
	}
	return base64.RawURLEncoding.EncodeToString(secret[:]), true, nil
}
