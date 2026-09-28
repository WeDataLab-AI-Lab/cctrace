package store

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestAISettingsRoundTrip(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// No row is "no admin override", not an error.
	got, err := s.GetAISettings(ctx, "codex-app-server")
	if err != nil || got != nil {
		t.Fatalf("empty = %+v, %v", got, err)
	}

	if err := s.SetAISettings(ctx, "codex-app-server", "gpt-5.6-terra", "high", "", "admin@example.com"); err != nil {
		t.Fatalf("SetAISettings: %v", err)
	}
	got, err = s.GetAISettings(ctx, "codex-app-server")
	if err != nil || got == nil {
		t.Fatalf("after set = %+v, %v", got, err)
	}
	if got.Runtime != "codex-app-server" || got.Model != "gpt-5.6-terra" || got.ReasoningEffort != "high" || got.UpdatedBy != "admin@example.com" || got.UpdatedAt.IsZero() {
		t.Fatalf("settings = %+v", got)
	}

	// Settings belong to one runtime: another runtime's model never shows here.
	if err := s.SetAISettings(ctx, "claude-api", "claude-sonnet-5", "low", "", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetAISettings(ctx, "codex-app-server"); got.Model != "gpt-5.6-terra" {
		t.Fatalf("codex settings after claude write = %+v", got)
	}

	// A second write replaces the runtime's row; empty values clear the override.
	if err := s.SetAISettings(ctx, "codex-app-server", "", "", "", "other@example.com"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAISettings(ctx, "codex-app-server")
	if got.Model != "" || got.ReasoningEffort != "" || got.UpdatedBy != "other@example.com" {
		t.Fatalf("cleared = %+v", got)
	}
	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ai_runtime_settings`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("rows = %d, %v", rows, err)
	}
}

func TestAIRuntimeChoiceRoundTrip(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if got, err := s.GetAIRuntimeChoice(ctx); err != nil || got != nil {
		t.Fatalf("empty = %+v, %v", got, err)
	}
	if err := s.SetAIRuntimeChoice(ctx, "claude-api", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAIRuntimeChoice(ctx)
	if err != nil || got == nil || got.Runtime != "claude-api" || got.UpdatedBy != "admin@example.com" || got.UpdatedAt.IsZero() {
		t.Fatalf("choice = %+v, %v", got, err)
	}
	// Clearing leaves no choice, the same as never choosing.
	if err := s.SetAIRuntimeChoice(ctx, "", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetAIRuntimeChoice(ctx); err != nil || got != nil {
		t.Fatalf("cleared = %+v, %v", got, err)
	}
}

// The admin's AI report switch: no row and NULL are "no admin word", and it
// lives beside the runtime choice without either write clearing the other.
func TestAIEnabledChoiceRoundTrip(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if got, err := s.GetAIEnabledChoice(ctx); err != nil || got != nil {
		t.Fatalf("empty = %+v, %v", got, err)
	}
	on, off := true, false
	if err := s.SetAIEnabledChoice(ctx, &on, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAIEnabledChoice(ctx)
	if err != nil || got == nil || !got.Enabled || got.UpdatedBy != "admin@example.com" || got.UpdatedAt.IsZero() {
		t.Fatalf("on = %+v, %v", got, err)
	}
	if err := s.SetAIRuntimeChoice(ctx, "claude-api", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAIEnabledChoice(ctx, &off, "other@example.com"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetAIEnabledChoice(ctx); got == nil || got.Enabled || got.UpdatedBy != "other@example.com" {
		t.Fatalf("off = %+v", got)
	}
	if c, _ := s.GetAIRuntimeChoice(ctx); c == nil || c.Runtime != "claude-api" {
		t.Fatalf("runtime choice after switch write = %+v", c)
	}
	if err := s.SetAIEnabledChoice(ctx, nil, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetAIEnabledChoice(ctx); err != nil || got != nil {
		t.Fatalf("cleared = %+v, %v", got, err)
	}
}

// The model and effort an admin saved before settings became per runtime were
// all Codex's; the migration hands them to codex-app-server once.
func TestAISettingsLegacyRowMovesToCodex(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO ai_settings (id, model, reasoning_effort, updated_by) VALUES (1, 'gpt-legacy', 'low', 'old@example.com')`); err != nil {
		t.Fatal(err)
	}
	copied := false
	for _, q := range migrations {
		if strings.Contains(q, "INSERT INTO ai_runtime_settings") {
			copied = true
			for i := 0; i < 2; i++ {
				if _, err := s.pool.Exec(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if !copied {
		t.Fatal("legacy settings migration missing")
	}
	got, err := s.GetAISettings(ctx, "codex-app-server")
	if err != nil || got == nil || got.Model != "gpt-legacy" || got.ReasoningEffort != "low" || got.UpdatedBy != "old@example.com" {
		t.Fatalf("codex settings = %+v, %v", got, err)
	}
	// A later admin change is not overwritten by the next boot's migration pass.
	if err := s.SetAISettings(ctx, "codex-app-server", "", "", "", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, q := range migrations {
		if strings.Contains(q, "INSERT INTO ai_runtime_settings") {
			if _, err := s.pool.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got, _ = s.GetAISettings(ctx, "codex-app-server"); got.Model != "" {
		t.Fatalf("migration overwrote the admin's change: %+v", got)
	}
}

func TestAIProviderCredentialRoundTrip(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if got, err := s.GetAIProviderCredential(ctx, "openai"); err != nil || got != nil {
		t.Fatalf("empty = %+v, %v", got, err)
	}
	c := AIProviderCredential{Provider: "openai", Ciphertext: []byte{1, 2, 3}, Nonce: []byte{4, 5}, KeyHint: "abcd", UpdatedBy: "admin@example.com"}
	if err := s.SetAIProviderCredential(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.Ciphertext, c.KeyHint = []byte{9}, "wxyz"
	if err := s.SetAIProviderCredential(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAIProviderCredential(ctx, "openai")
	if err != nil || got == nil || !bytes.Equal(got.Ciphertext, []byte{9}) || !bytes.Equal(got.Nonce, []byte{4, 5}) ||
		got.KeyHint != "wxyz" || got.UpdatedBy != "admin@example.com" || got.UpdatedAt.IsZero() {
		t.Fatalf("credential = %+v, %v", got, err)
	}
	if err := s.SetAIProviderCredential(ctx, AIProviderCredential{Provider: "gemini", Ciphertext: []byte{1}, Nonce: []byte{1}}); err == nil {
		t.Fatal("unknown provider must be refused")
	}
	if err := s.DeleteAIProviderCredential(ctx, "openai"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetAIProviderCredential(ctx, "openai"); err != nil || got != nil {
		t.Fatalf("after delete = %+v, %v", got, err)
	}
	// Deleting nothing is not an error.
	if err := s.DeleteAIProviderCredential(ctx, "anthropic"); err != nil {
		t.Fatal(err)
	}
}

// A sealed key records which secret sealed it, so a failed decrypt can say
// which variable changed. Rows sealed before the column read as "".
func TestAIProviderCredentialKeySource(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	c := AIProviderCredential{Provider: "openai", Ciphertext: []byte{1}, Nonce: []byte{2}, KeyHint: "abcd", UpdatedBy: "a", KeySource: "secrets_key"}
	if err := s.SetAIProviderCredential(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetAIProviderCredential(ctx, "openai"); err != nil || got == nil || got.KeySource != "secrets_key" {
		t.Fatalf("got = %+v, %v", got, err)
	}
	c.KeySource = "jwt_secret"
	if err := s.SetAIProviderCredential(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAIProviderCredential(ctx, "openai"); got.KeySource != "jwt_secret" {
		t.Fatalf("replaced = %+v", got)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO ai_provider_credentials (provider, ciphertext, nonce) VALUES ('anthropic', '\x01', '\x02')`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAIProviderCredential(ctx, "anthropic"); got == nil || got.KeySource != "" {
		t.Fatalf("legacy row = %+v", got)
	}
}

// Migration extends provider CHECK to include nvidia and litellm, and adds
// base_url column. Existing rows are preserved; old data is not affected.
func TestAIMigrationProviderCheckAndBaseURL(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// Seed existing openai/anthropic rows before migration
	if _, err := s.pool.Exec(ctx, `INSERT INTO ai_provider_credentials (provider, ciphertext, nonce) VALUES ('openai', '\x01', '\x02')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO ai_runtime_settings (runtime, model) VALUES ('openai-api', 'gpt-5.6-terra')`); err != nil {
		t.Fatal(err)
	}

	// Run migrations to apply provider CHECK expansion and base_url column
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Verify existing openai row is preserved
	if got, err := s.GetAIProviderCredential(ctx, "openai"); err != nil || got == nil || got.Provider != "openai" {
		t.Fatalf("openai credential lost: %+v, %v", got, err)
	}

	// Verify existing runtime settings are preserved
	if got, err := s.GetAISettings(ctx, "openai-api"); err != nil || got == nil || got.Model != "gpt-5.6-terra" || got.BaseURL != "" {
		t.Fatalf("runtime settings lost or base_url not initialized: %+v, %v", got, err)
	}

	// New providers can now be saved
	if err := s.SetAIProviderCredential(ctx, AIProviderCredential{Provider: "nvidia", Ciphertext: []byte{1}, Nonce: []byte{2}}); err != nil {
		t.Fatalf("nvidia credential rejected: %v", err)
	}
	if err := s.SetAIProviderCredential(ctx, AIProviderCredential{Provider: "litellm", Ciphertext: []byte{1}, Nonce: []byte{2}}); err != nil {
		t.Fatalf("litellm credential rejected: %v", err)
	}

	// Verify new credentials are saved
	if got, _ := s.GetAIProviderCredential(ctx, "nvidia"); got == nil || got.Provider != "nvidia" {
		t.Fatalf("nvidia credential not saved")
	}
	if got, _ := s.GetAIProviderCredential(ctx, "litellm"); got == nil || got.Provider != "litellm" {
		t.Fatalf("litellm credential not saved")
	}

	// base_url can be set and retrieved
	if err := s.SetAISettings(ctx, "litellm-api", "llama-2", "", "https://proxy.example.test/v1", "admin"); err != nil {
		t.Fatalf("SetAISettings litellm: %v", err)
	}
	// The address has to survive the round trip: it is the only way an admin can
	// point the runtime at their proxy, and a column that silently drops it
	// leaves the runtime reaching nowhere.
	if got, _ := s.GetAISettings(ctx, "litellm-api"); got == nil || got.BaseURL != "https://proxy.example.test/v1" {
		t.Fatalf("litellm base_url did not round-trip: %+v", got)
	}
}
