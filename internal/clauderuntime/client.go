// Package clauderuntime runs a weekly report turn on the Anthropic Messages API
// with client tools: cctraced sends the request, executes each tool_use block
// itself and loops until the model answers. It uses net/http only.
package clauderuntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"cctrace/internal/airuntime"
)

// RuntimeKey identifies this runtime in Info, consent keys and the API.
const RuntimeKey = "claude-api"

const (
	defaultBaseURL        = "https://api.anthropic.com"
	defaultModel          = "claude-sonnet-5"
	defaultAPIVersion     = "2023-06-01"
	defaultRequestTimeout = 10 * time.Minute
	// defaultMaxTokens caps one response (thinking plus text). The curated
	// models accept up to 128K output tokens (Haiku 4.5: 64K), but a
	// non-streaming request that large can outlast the 10-minute request
	// timeout, and the SDKs require streaming for it. Requests here are not
	// streamed, so the cap stays at the non-streaming size the SDKs recommend
	// for every effort. At xhigh or max, thinking can use it up before the
	// answer; that ends the turn as output_limit. Raising it needs streaming.
	defaultMaxTokens = 16000
	maxLoggedBody    = 512
	maxResponseBytes = 32 << 20
)

type Config struct {
	// APIKey returns the key for each request; "" means not configured.
	APIKey         func(ctx context.Context) (string, error)
	BaseURL        string
	HTTPClient     *http.Client
	DefaultModel   string
	APIVersion     string
	RequestTimeout time.Duration
	MaxTokens      int64
	// Logf receives provider error details with the key masked. Default log.Printf.
	Logf func(format string, args ...any)
}

type Runtime struct {
	cfg Config
	now func() time.Time

	mu      sync.Mutex
	models  []airuntime.Model
	modErr  error
	modTill time.Time
	modKey  string // sha256 of the key the cache was filled with

	modelsFlight  singleflight.Group
	modelsTimeout time.Duration
}

func New(cfg Config) *Runtime {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	// Go drops only Authorization and Cookie on a cross-host redirect, so
	// x-api-key would follow one; no redirect is followed at all.
	client := http.Client{}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cfg.HTTPClient = &client
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = defaultModel
	}
	if cfg.APIVersion == "" {
		cfg.APIVersion = defaultAPIVersion
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultRequestTimeout
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	return &Runtime{cfg: cfg, now: time.Now, modelsTimeout: modelsRequestTimeout}
}

func (r *Runtime) Info() airuntime.Info {
	return airuntime.Info{Key: RuntimeKey, Model: r.cfg.DefaultModel, AuthMode: airuntime.AuthModeAPIKey}
}

// apiKey errors: errNoKey means nothing is configured; errKeyLookup means the
// key source itself failed, which is an outage, not a missing setting.
var (
	errNoKey     = errors.New("no Anthropic API key configured")
	errKeyLookup = errors.New("could not read the Anthropic API key")
)

func (r *Runtime) apiKey(ctx context.Context) (string, error) {
	if r.cfg.APIKey == nil {
		return "", errNoKey
	}
	key, err := r.cfg.APIKey(ctx)
	if err != nil {
		r.cfg.Logf("clauderuntime: read API key: %v", err)
		return "", errKeyLookup
	}
	if key == "" {
		return "", errNoKey
	}
	return key, nil
}

// httpError is a non-2xx answer. The body is only logged, never kept here, so
// nothing the provider echoed (a key prefix, a prompt) reaches a user message.
type httpError struct {
	status  int
	errType string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("anthropic API returned %d %s", e.status, e.errType)
}

// transportError is a request that got no HTTP answer.
type transportError struct{ err error }

func (e *transportError) Error() string { return "anthropic API unreachable" }
func (e *transportError) Unwrap() error { return e.err }

// do sends one JSON request and decodes a 2xx body into out.
func (r *Runtime) do(ctx context.Context, key, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	reqCtx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, r.cfg.BaseURL+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", r.cfg.APIVersion)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := r.cfg.HTTPClient.Do(req)
	if err != nil {
		r.cfg.Logf("clauderuntime: %s %s: %s", method, path, redact(err.Error(), key))
		return &transportError{err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		r.cfg.Logf("clauderuntime: %s %s: read body: %s", method, path, redact(err.Error(), key))
		return &transportError{err: err}
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		r.cfg.Logf("clauderuntime: %s %s: status %d request-id %q: %s", method, path, resp.StatusCode,
			resp.Header.Get("request-id"), loggedBody(raw, key))
		return &httpError{status: resp.StatusCode, errType: e.Error.Type}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: undecodable response from %s", airuntime.ErrProtocol, path)
	}
	return nil
}

// loggedBody is a non-2xx body as it goes to the log. It masks before cutting,
// or a key straddling the cut survives as a prefix, and cuts on a rune boundary.
func loggedBody(raw []byte, key string) string {
	return airuntime.TruncateText(redact(string(raw), key), maxLoggedBody, "")
}

func redact(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[redacted]")
}

// runError maps a request failure to the stored code and a fixed message.
func runError(err error) *airuntime.RunError {
	var he *httpError
	var te *transportError
	switch {
	case errors.As(err, &he):
		switch {
		case he.status == 401:
			return &airuntime.RunError{Code: "auth_failed", Message: "Anthropic API rejected the API key", Err: airuntime.ErrNotLoggedIn}
		case he.status == 403:
			// permission_error: the key is valid but not allowed this model or resource.
			return &airuntime.RunError{Code: "permission_denied", Message: "Anthropic API key is not permitted to make this request", Err: airuntime.ErrUnavailable}
		case he.status == 429:
			return &airuntime.RunError{Code: "rate_limited", Message: "Anthropic API rate limit reached", Err: airuntime.ErrUnavailable}
		case he.status == 529 || he.status >= 500:
			return &airuntime.RunError{Code: "provider_unavailable", Message: "Anthropic API is unavailable", Err: airuntime.ErrUnavailable}
		default:
			return &airuntime.RunError{Code: "invalid_request", Message: fmt.Sprintf("Anthropic API rejected the request (%d)", he.status), Err: airuntime.ErrProtocol}
		}
	case errors.As(err, &te):
		return &airuntime.RunError{Code: "provider_unavailable", Message: "Anthropic API is unreachable", Err: airuntime.ErrUnavailable}
	case errors.Is(err, airuntime.ErrProtocol):
		return &airuntime.RunError{Code: "protocol_error", Message: "undecodable Anthropic API response", Err: airuntime.ErrProtocol}
	default:
		return &airuntime.RunError{Code: "protocol_error", Message: "Anthropic API request failed", Err: airuntime.ErrProtocol}
	}
}
