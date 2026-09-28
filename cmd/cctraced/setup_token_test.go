package main

import (
	"encoding/base64"
	"testing"
)

func TestResolveSetupToken(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		count     int
		want      string
		generated bool
	}{
		{"configured", "operator-secret", 0, "operator-secret", false},
		{"completed", "operator-secret", 1, "", false},
		{"completed without env", "", 1, "", false},
		{"generated", "", 0, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, generated, err := resolveSetupToken(tc.env, tc.count)
			if err != nil || generated != tc.generated {
				t.Fatalf("generated=%v err=%v", generated, err)
			}
			if !generated && token != tc.want {
				t.Fatalf("unexpected token")
			}
			if generated {
				decoded, err := base64.RawURLEncoding.DecodeString(token)
				if err != nil || len(decoded) != 32 {
					t.Fatalf("expected 32 random bytes")
				}
			}
		})
	}
}
