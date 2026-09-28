package aireport

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"cctrace/internal/airuntime"
	"cctrace/internal/store"
)

// Providers whose API keys the keyring holds.
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderNVIDIA    = "nvidia"
	ProviderLiteLLM   = "litellm"
)

// Where a provider's key comes from.
const (
	CredentialEnv   = "env"
	CredentialAdmin = "admin"
	CredentialNone  = "none"
)

// ProviderEnvVars are the dedicated variables for each provider's key. They are
// not OPENAI_API_KEY or ANTHROPIC_API_KEY, so a key a deployment carries for
// something else is never picked up by accident.
var ProviderEnvVars = map[string]string{
	ProviderOpenAI:    "CCTRACE_AI_OPENAI_API_KEY",
	ProviderAnthropic: "CCTRACE_AI_ANTHROPIC_API_KEY",
	ProviderNVIDIA:    "CCTRACE_AI_NVIDIA_API_KEY",
	ProviderLiteLLM:   "CCTRACE_AI_LITELLM_API_KEY",
}

// secretsKeyInfo versions the key derivation: a different scheme gets a
// different info string, and keys sealed under the old one read as undecryptable.
const secretsKeyInfo = "cctrace-ai-provider-key-v1"

const maxAPIKeyBytes = 2048

// MinSecretBytes is the shortest secret keys are sealed with; the same floor
// JWT_SECRET has. HKDF stretches a short secret to 32 bytes but adds no entropy.
const MinSecretBytes = 32

// ReasonReRegister tells the admin a stored key cannot be opened with this
// server's secret.
const ReasonReRegister = "저장된 API 키를 복호화하지 못했습니다. 키를 다시 등록하세요"

var (
	ErrUnknownProvider    = errors.New("unknown ai provider")
	ErrInvalidAPIKey      = errors.New("api key is empty, too long or contains whitespace")
	ErrSecretsUnavailable = errors.New("no secret to encrypt api keys with")
)

// CredentialStore is the part of Store the keyring uses.
type CredentialStore interface {
	GetAIProviderCredential(ctx context.Context, provider string) (*store.AIProviderCredential, error)
	SetAIProviderCredential(ctx context.Context, c store.AIProviderCredential) error
	DeleteAIProviderCredential(ctx context.Context, provider string) error
}

// CredentialInfo is what may be shown about a key: never the key. Reason is set
// when a stored key cannot be used.
type CredentialInfo struct {
	Provider  string
	Source    string
	KeyHint   string
	EnvVar    string
	Reason    string
	UpdatedAt *time.Time
}

// Which variable the sealing secret came from, stored beside each key.
const (
	KeySourceSecretsKey = "secrets_key"
	KeySourceJWTSecret  = "jwt_secret"
)

var keySourceVars = map[string]string{
	KeySourceSecretsKey: "CCTRACE_SECRETS_KEY",
	KeySourceJWTSecret:  "JWT_SECRET",
}

// SecretsSource picks the secret keys are sealed with and names its variable:
// CCTRACE_SECRETS_KEY when set, JWT_SECRET otherwise, "" when neither is.
func SecretsSource(dedicated, jwtSecret string) (secret, source string) {
	switch {
	case strings.TrimSpace(dedicated) != "":
		return dedicated, KeySourceSecretsKey
	case jwtSecret != "":
		return jwtSecret, KeySourceJWTSecret
	}
	return "", ""
}

// DeriveSecretsKey derives the AES-256 key from secret with HKDF-SHA256, so the
// JWT signing secret is never used as a cipher key directly.
func DeriveSecretsKey(secret string) ([]byte, error) {
	return hkdf.Key(sha256.New, []byte(secret), nil, secretsKeyInfo, 32)
}

// Keyring resolves provider API keys: the environment first, then the key an
// admin registered, sealed with AES-256-GCM. Keys are read on every call, so a
// change applies to the next request without a restart.
type Keyring struct {
	st   CredentialStore
	aead cipher.AEAD // nil when there is no usable secret
	env  map[string]string
	// secretsReason says why aead is nil.
	secretsReason string
	// source names the secret's variable; "" when the caller did not say.
	source string

	mu     sync.Mutex
	warned map[string]time.Time // provider -> updated_at of the row already logged as undecryptable
}

// NewKeyring builds a keyring. A secret "" or shorter than MinSecretBytes
// leaves registration refused, with SecretsReason saying why; env maps a
// provider to its environment key.
func NewKeyring(st CredentialStore, secret string, env map[string]string) *Keyring {
	return NewSourcedKeyring(st, secret, "", env)
}

// NewSourcedKeyring is NewKeyring with the secret's source (KeySourceSecretsKey
// or KeySourceJWTSecret), stored with each key so a key that no longer opens
// can name the variable that changed.
func NewSourcedKeyring(st CredentialStore, secret, source string, env map[string]string) *Keyring {
	k := &Keyring{st: st, env: map[string]string{}, warned: map[string]time.Time{}, source: source}
	for p, v := range env {
		k.env[p] = strings.TrimSpace(v)
	}
	switch {
	case secret == "":
		k.secretsReason = "키를 암호화할 비밀값(CCTRACE_SECRETS_KEY 또는 JWT_SECRET)이 없습니다"
	case len(secret) < MinSecretBytes:
		k.secretsReason = fmt.Sprintf("키 암호화 비밀값(CCTRACE_SECRETS_KEY 또는 JWT_SECRET)이 %d바이트 미만이라 키를 등록할 수 없습니다", MinSecretBytes)
	default:
		if key, err := DeriveSecretsKey(secret); err == nil {
			if block, err := aes.NewCipher(key); err == nil {
				k.aead, _ = cipher.NewGCM(block)
			}
		}
		if k.aead == nil {
			k.secretsReason = "키 암호화를 준비하지 못했습니다"
		}
	}
	return k
}

// SecretsReason says why no key can be registered, "" when one can.
func (k *Keyring) SecretsReason() string { return k.secretsReason }

func knownProvider(p string) bool { _, ok := ProviderEnvVars[p]; return ok }

// keyHint is the last four characters, and nothing for a key too short to
// spare them.
func keyHint(key string) string {
	if len(key) < 12 {
		return ""
	}
	return key[len(key)-4:]
}

// APIKey is the runtimes' key source. "" with no error means no usable key --
// also for a key that cannot be decrypted, so the runtime reports itself
// unconfigured rather than unreachable.
func (k *Keyring) APIKey(provider string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		key, _, err := k.resolve(ctx, provider)
		return key, err
	}
}

func (k *Keyring) resolve(ctx context.Context, provider string) (string, CredentialInfo, error) {
	info := CredentialInfo{Provider: provider, Source: CredentialNone, EnvVar: ProviderEnvVars[provider]}
	if !knownProvider(provider) {
		return "", info, ErrUnknownProvider
	}
	if key := k.env[provider]; key != "" {
		info.Source, info.KeyHint = CredentialEnv, keyHint(key)
		return key, info, nil
	}
	c, err := k.st.GetAIProviderCredential(ctx, provider)
	if err != nil || c == nil {
		return "", info, err
	}
	at := c.UpdatedAt
	info.Source, info.KeyHint, info.UpdatedAt = CredentialAdmin, c.KeyHint, &at
	key, ok := k.open(provider, c)
	if !ok {
		info.Reason = k.reRegisterReason(c.KeySource)
		k.warnOnce(provider, c.UpdatedAt)
		return "", info, nil
	}
	return key, info, nil
}

// reRegisterReason names the change that left a key sealed under sealedBy
// unreadable; a key from before key_source, or a keyring that does not know its
// own source, gets ReasonReRegister.
func (k *Keyring) reRegisterReason(sealedBy string) string {
	was, now := keySourceVars[sealedBy], keySourceVars[k.source]
	switch {
	case was == "" || now == "":
		return ReasonReRegister
	case was == now:
		return fmt.Sprintf("%s 값이 바뀌어 저장된 API 키를 복호화하지 못했습니다. 키를 다시 등록하세요", was)
	default:
		return fmt.Sprintf("키 암호화 비밀이 %s 에서 %s 로 바뀌어 저장된 API 키를 복호화하지 못했습니다. 키를 다시 등록하세요", was, now)
	}
}

func (k *Keyring) open(provider string, c *store.AIProviderCredential) (string, bool) {
	if k.aead == nil || len(c.Nonce) != k.aead.NonceSize() {
		return "", false
	}
	plain, err := k.aead.Open(nil, c.Nonce, c.Ciphertext, []byte(provider))
	if err != nil {
		return "", false
	}
	return string(plain), true
}

func (k *Keyring) warnOnce(provider string, updatedAt time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.warned[provider].Equal(updatedAt) {
		return
	}
	k.warned[provider] = updatedAt
	log.Printf("[aireport] the %s API key registered in Admin > AI cannot be decrypted (JWT_SECRET or CCTRACE_SECRETS_KEY changed?); register it again", provider)
}

// Credential describes provider's key without revealing it.
func (k *Keyring) Credential(ctx context.Context, provider string) (CredentialInfo, error) {
	_, info, err := k.resolve(ctx, provider)
	return info, err
}

// SetKey seals and stores key for provider. Errors never carry the key.
func (k *Keyring) SetKey(ctx context.Context, provider, key, actor string) (CredentialInfo, error) {
	if err := k.writable(provider); err != nil {
		return CredentialInfo{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > maxAPIKeyBytes || strings.IndexFunc(key, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return CredentialInfo{}, ErrInvalidAPIKey
	}
	if k.aead == nil {
		return CredentialInfo{}, ErrSecretsUnavailable
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return CredentialInfo{}, fmt.Errorf("api key nonce: %w", err)
	}
	c := store.AIProviderCredential{
		Provider: provider, Nonce: nonce, KeyHint: keyHint(key), KeySource: k.source, UpdatedBy: actor,
		// The provider is the associated data: a row copied to another provider does not open.
		Ciphertext: k.aead.Seal(nil, nonce, []byte(key), []byte(provider)),
	}
	if err := k.st.SetAIProviderCredential(ctx, c); err != nil {
		return CredentialInfo{}, err
	}
	return k.Credential(ctx, provider)
}

// DeleteKey removes the admin's key for provider.
func (k *Keyring) DeleteKey(ctx context.Context, provider string) (CredentialInfo, error) {
	if err := k.writable(provider); err != nil {
		return CredentialInfo{}, err
	}
	if err := k.st.DeleteAIProviderCredential(ctx, provider); err != nil {
		return CredentialInfo{}, err
	}
	return k.Credential(ctx, provider)
}

func (k *Keyring) writable(provider string) error {
	if !knownProvider(provider) {
		return ErrUnknownProvider
	}
	if k.env[provider] != "" {
		return fmt.Errorf("%w: %s", airuntime.ErrEnvManaged, ProviderEnvVars[provider])
	}
	return nil
}
