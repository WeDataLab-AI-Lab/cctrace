package aireport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"

	"cctrace/internal/airuntime"
)

const testKey = "sk-proj-test-0123456789-wxyz"

// Secrets of the minimum length keys are sealed with.
const (
	testSecret  = "test-secret-at-least-32-bytes-long!!"
	otherSecret = "other-secret-at-least-32-bytes-long!"
)

func TestKeyringRoundTrip(t *testing.T) {
	st := NewMemStore()
	kr := NewKeyring(st, testSecret, nil)
	ctx := context.Background()

	info, err := kr.Credential(ctx, ProviderOpenAI)
	if err != nil || info.Source != CredentialNone || info.KeyHint != "" || info.EnvVar != "CCTRACE_AI_OPENAI_API_KEY" {
		t.Fatalf("empty = %+v, %v", info, err)
	}
	if key, err := kr.APIKey(ProviderOpenAI)(ctx); key != "" || err != nil {
		t.Fatalf("no key = %q, %v", key, err)
	}

	info, err = kr.SetKey(ctx, ProviderOpenAI, "  "+testKey+"\n", "admin@example.com")
	if err != nil || info.Source != CredentialAdmin || info.KeyHint != "wxyz" || info.UpdatedAt == nil {
		t.Fatalf("set = %+v, %v", info, err)
	}
	stored := st.Credentials[ProviderOpenAI]
	if stored == nil || bytes.Contains(stored.Ciphertext, []byte(testKey)) || len(stored.Nonce) != 12 || stored.UpdatedBy != "admin@example.com" {
		t.Fatalf("stored = %+v", stored)
	}
	if key, err := kr.APIKey(ProviderOpenAI)(ctx); key != testKey || err != nil {
		t.Fatalf("key = %q, %v", key, err)
	}
	// Another provider has its own key.
	if key, _ := kr.APIKey(ProviderAnthropic)(ctx); key != "" {
		t.Fatalf("anthropic key = %q", key)
	}
	// The same secret after a restart opens it.
	if key, _ := NewKeyring(st, testSecret, nil).APIKey(ProviderOpenAI)(ctx); key != testKey {
		t.Fatalf("after restart = %q", key)
	}

	info, err = kr.DeleteKey(ctx, ProviderOpenAI)
	if err != nil || info.Source != CredentialNone || st.Credentials[ProviderOpenAI] != nil {
		t.Fatalf("delete = %+v, %v", info, err)
	}
}

// A sealed key is bound to its provider: moving the row does not open it.
func TestKeyringCiphertextBoundToProvider(t *testing.T) {
	st := NewMemStore()
	kr := NewKeyring(st, testSecret, nil)
	ctx := context.Background()
	if _, err := kr.SetKey(ctx, ProviderOpenAI, testKey, "a"); err != nil {
		t.Fatal(err)
	}
	moved := *st.Credentials[ProviderOpenAI]
	moved.Provider = ProviderAnthropic
	st.Credentials[ProviderAnthropic] = &moved
	if key, _ := kr.APIKey(ProviderAnthropic)(ctx); key != "" {
		t.Fatalf("moved ciphertext opened: %q", key)
	}
}

// A replaced JWT_SECRET cannot open the old key: the runtime reads no key, so
// it reports itself unconfigured, and the admin is told to register again.
func TestKeyringDecryptFailure(t *testing.T) {
	st := NewMemStore()
	ctx := context.Background()
	if _, err := NewKeyring(st, testSecret, nil).SetKey(ctx, ProviderAnthropic, testKey, "a"); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	kr := NewKeyring(st, otherSecret, nil)
	key, err := kr.APIKey(ProviderAnthropic)(ctx)
	if key != "" || err != nil {
		t.Fatalf("key = %q, %v", key, err)
	}
	info, err := kr.Credential(ctx, ProviderAnthropic)
	if err != nil || info.Source != CredentialAdmin || !strings.Contains(info.Reason, "키를 다시 등록하세요") || info.KeyHint != "wxyz" {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if strings.Contains(logs.String(), testKey) {
		t.Fatal("log carries the key")
	}
	// Registering again fixes it.
	if _, err := kr.SetKey(ctx, ProviderAnthropic, testKey, "a"); err != nil {
		t.Fatal(err)
	}
	if info, _ = kr.Credential(ctx, ProviderAnthropic); info.Reason != "" {
		t.Fatalf("after re-register = %+v", info)
	}
}

// A key sealed with a known secret says which variable changed when it no
// longer opens; a key from before key_source keeps the generic reason.
func TestKeyringDecryptReasonNamesSecret(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	if _, err := NewSourcedKeyring(st, testSecret, KeySourceJWTSecret, nil).SetKey(ctx, ProviderOpenAI, testKey, "a"); err != nil {
		t.Fatal(err)
	}
	if got := st.Credentials[ProviderOpenAI].KeySource; got != KeySourceJWTSecret {
		t.Fatalf("stored key_source = %q", got)
	}
	cases := []struct {
		name, secret, source string
		want                 []string
	}{
		{"same variable, new value", otherSecret, KeySourceJWTSecret, []string{"JWT_SECRET", "바뀌", "키를 다시 등록하세요"}},
		{"moved to the dedicated variable", otherSecret, KeySourceSecretsKey, []string{"JWT_SECRET", "CCTRACE_SECRETS_KEY", "키를 다시 등록하세요"}},
	}
	for _, c := range cases {
		info, err := NewSourcedKeyring(st, c.secret, c.source, nil).Credential(ctx, ProviderOpenAI)
		for _, w := range c.want {
			if err != nil || !strings.Contains(info.Reason, w) {
				t.Errorf("%s: reason %q lacks %q (%v)", c.name, info.Reason, w, err)
			}
		}
	}
	legacy := *st.Credentials[ProviderOpenAI]
	legacy.KeySource = ""
	st.Credentials[ProviderOpenAI] = &legacy
	if info, _ := NewSourcedKeyring(st, otherSecret, KeySourceJWTSecret, nil).Credential(ctx, ProviderOpenAI); info.Reason != ReasonReRegister {
		t.Fatalf("legacy row reason = %q", info.Reason)
	}
	// The same secret still opens it whatever the row says.
	if key, _ := NewSourcedKeyring(st, testSecret, KeySourceJWTSecret, nil).APIKey(ProviderOpenAI)(ctx); key != testKey {
		t.Fatalf("same secret = %q", key)
	}
}

// CCTRACE_SECRETS_KEY, when set, is the secret instead of JWT_SECRET.
func TestSecretsKeyPrefersDedicatedVariable(t *testing.T) {
	if got, source := SecretsSource("dedicated", "jwt"); got != "dedicated" || source != KeySourceSecretsKey {
		t.Fatalf("got %q %q", got, source)
	}
	if got, source := SecretsSource(" ", "jwt"); got != "jwt" || source != KeySourceJWTSecret {
		t.Fatalf("blank dedicated = %q %q", got, source)
	}
	if got, source := SecretsSource("", ""); got != "" || source != "" {
		t.Fatalf("none = %q %q", got, source)
	}
	a, err := DeriveSecretsKey("jwt")
	if err != nil || len(a) != 32 {
		t.Fatalf("key len %d, %v", len(a), err)
	}
	b, _ := DeriveSecretsKey("other")
	if bytes.Equal(a, b) || bytes.Contains(a, []byte("jwt")) {
		t.Fatal("derived key must depend on and not contain the secret")
	}
}

func TestKeyringEnvWins(t *testing.T) {
	st := NewMemStore()
	kr := NewKeyring(st, testSecret, map[string]string{ProviderOpenAI: " env-key-abcd1234 "})
	ctx := context.Background()
	info, err := kr.Credential(ctx, ProviderOpenAI)
	if err != nil || info.Source != CredentialEnv || info.KeyHint != "1234" {
		t.Fatalf("env = %+v, %v", info, err)
	}
	if key, _ := kr.APIKey(ProviderOpenAI)(ctx); key != "env-key-abcd1234" {
		t.Fatalf("key = %q", key)
	}
	if _, err := kr.SetKey(ctx, ProviderOpenAI, testKey, "a"); !errors.Is(err, airuntime.ErrEnvManaged) {
		t.Fatalf("set over env: %v", err)
	}
	if _, err := kr.DeleteKey(ctx, ProviderOpenAI); !errors.Is(err, airuntime.ErrEnvManaged) {
		t.Fatalf("delete over env: %v", err)
	}
	if len(st.Credentials) != 0 {
		t.Fatalf("stored %+v", st.Credentials)
	}
}

func TestKeyringRefusals(t *testing.T) {
	ctx := context.Background()
	kr := NewKeyring(NewMemStore(), testSecret, nil)
	if _, err := kr.SetKey(ctx, "gemini", testKey, "a"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown provider: %v", err)
	}
	if _, err := kr.Credential(ctx, "gemini"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown provider read: %v", err)
	}
	for _, bad := range []string{"", "   ", "sk key", "sk\x00key", strings.Repeat("k", 2049)} {
		if _, err := kr.SetKey(ctx, ProviderOpenAI, bad, "a"); !errors.Is(err, ErrInvalidAPIKey) {
			t.Errorf("key %q: %v", bad, err)
		}
	}
	// No secret at all: nothing can be sealed, and the refusal says so.
	if _, err := NewKeyring(NewMemStore(), "", nil).SetKey(ctx, ProviderOpenAI, testKey, "a"); !errors.Is(err, ErrSecretsUnavailable) {
		t.Fatalf("no secret: %v", err)
	}
}

// A secret shorter than 32 bytes seals nothing, and says why; a key sealed
// before stays unreadable rather than opening under a weak secret.
func TestKeyringRefusesShortSecret(t *testing.T) {
	ctx := context.Background()
	if r := NewKeyring(NewMemStore(), testSecret, nil).SecretsReason(); r != "" {
		t.Fatalf("32-byte secret reason = %q", r)
	}
	if r := NewKeyring(NewMemStore(), "", nil).SecretsReason(); r == "" {
		t.Fatal("no secret must have a reason")
	}
	short := NewKeyring(NewMemStore(), strings.Repeat("s", MinSecretBytes-1), nil)
	if r := short.SecretsReason(); !strings.Contains(r, "32") {
		t.Fatalf("short secret reason = %q", r)
	}
	if _, err := short.SetKey(ctx, ProviderOpenAI, testKey, "a"); !errors.Is(err, ErrSecretsUnavailable) {
		t.Fatalf("short secret set: %v", err)
	}
}

// A store failure is reported without the key in the error.
func TestKeyringStoreErrorOmitsKey(t *testing.T) {
	st := NewMemStore()
	st.CredentialErr = fmt.Errorf("db down")
	_, err := NewKeyring(st, testSecret, nil).SetKey(context.Background(), ProviderOpenAI, testKey, "a")
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("err = %v", err)
	}
}
