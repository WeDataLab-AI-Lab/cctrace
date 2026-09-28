package main

import (
	"os"
	"testing"
)

// TestMain neutralizes the Codex environment for the whole package before any
// test runs.
//
// Redirecting HOME per test is not enough and is easy to get wrong: several
// tests hand-roll their own temp home, and codexlog.DefaultCodexDir prefers
// CODEX_CONFIG_DIR over HOME while ResolveScanDirs additionally scans
// CODEX_HOME. A developer with either variable set therefore had this suite
// read their real Codex tree — and runCodexPatch/autoMigrateCodex write an
// [otel] block containing an auth token into the config.toml they point at.
//
// Clearing them here makes the isolation impossible to bypass by forgetting a
// helper. Tests that need a specific Codex directory still set
// CODEX_CONFIG_DIR themselves via t.Setenv, which takes precedence.
func TestMain(m *testing.M) {
	if err := os.Unsetenv("CODEX_CONFIG_DIR"); err != nil {
		panic(err)
	}
	if err := os.Unsetenv("CODEX_HOME"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
