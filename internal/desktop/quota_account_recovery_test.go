package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/usage"
)

func TestClaudeRecoveryCapabilityAndExplicitOnly(t *testing.T) {
	t.Parallel()
	s := accountTestService(t, t.TempDir())
	var owners atomic.Int32
	s.recoverClaudeSession = func(context.Context, string, string) error { owners.Add(1); return nil }
	a := personalAccount(t, s, "claude")
	payload := map[string]any{"account_ref": a["account_ref"], "generation": a["generation"]}
	for _, op := range []string{"hello", "quota.accounts.list", "quota.account.read"} {
		call(t, s, "ordinary", op, map[string]any{})
	}
	call(t, s, "read-selected", "quota.account.read", payload)
	if owners.Load() != 0 {
		t.Fatal("implicit recovery")
	}
	hello := call(t, s, "hello", "hello", map[string]any{"quota_accounts": true})
	b, _ := json.Marshal(hello.Result)
	var wire struct {
		Capabilities map[string]bool `json:"capabilities"`
	}
	_ = json.Unmarshal(b, &wire)
	if wire.Capabilities["claude_session_recovery"] {
		t.Fatal("fixture advertised recovery")
	}
	r := call(t, s, "recover", "quota.account.recover", payload)
	if r.Error == nil || r.Error.Code != "recovery_blocked" {
		t.Fatalf("fixture recovery: %+v", r)
	}
	if owners.Load() != 0 {
		t.Fatal("fixture launched owner")
	}
	s.providerMode = true
	hello = call(t, s, "provider-hello", "hello", map[string]any{"quota_accounts": true})
	if e := json.Unmarshal(hello.Result, &wire); e != nil {
		t.Fatal(e)
	}
	if wire.Capabilities["claude_session_recovery"] != usage.ClaudeSessionRecoverySupported() {
		t.Fatal("provider capability missing or unsupported")
	}
	legacy := call(t, s, "legacy", "hello", map[string]any{})
	if bytes.Contains(legacy.Result, []byte("claude_session_recovery")) {
		t.Fatal("legacy hello bytes changed")
	}

}

func TestClaudeRecoveryReturnsReadingAndPreservesSelectedHome(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	s := accountTestService(t, t.TempDir())
	a := personalAccount(t, s, "claude")
	s.providerMode = true
	var selected string
	s.accountsMu.Lock()
	selected = s.accounts.Accounts[0].Home
	s.accountsMu.Unlock()
	// A cancelled ordinary read must not supply its pre-recovery epoch.
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	s.accountFlights = map[string]*accountFlight{a["account_ref"].(string): {ctx: cancelled, cancel: stop, done: make(chan struct{})}}
	s.accountRetry = map[string]time.Time{}
	s.accountSlots = make(chan struct{}, 4)
	s.recoverClaudeSession = func(_ context.Context, h, root string) error {
		if h != selected || root != s.appSupportRoot {
			t.Error("wrong recovery root")
		}
		return nil
	}
	s.accountReader = func(_ context.Context, p, h string) (accountSample, error) {
		if h != selected {
			t.Error("alias expansion")
		}
		return accountSample{Quota: quotaResult{Provider: p, State: "fresh", LastSuccessAt: &stamp, Windows: []quotaWindow{{Name: "five_hour", Utilization: 10, ResetsAt: now.Add(time.Hour).Format(time.RFC3339Nano)}}}, Proof: "claude\x00acct\x00org\x00scope"}, nil
	}
	r := call(t, s, "recover", "quota.account.recover", map[string]any{"account_ref": a["account_ref"], "generation": a["generation"]})
	if !usage.ClaudeSessionRecoverySupported() {
		if r.Error == nil {
			t.Fatal("unsupported OS recovered")
		}
		return
	}
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	var reading accountReading
	if json.Unmarshal(r.Result, &reading) != nil || reading.Ref != a["account_ref"] || reading.State != "fresh" {
		t.Fatalf("not same reading DTO: %+v", r.Result)
	}
}

func TestClaudeRecoveryCooldownCancellationAndDisabled(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"failure", "disabled", "cancel", "changed"} {
		t.Run(kind, func(t *testing.T) {
			s := accountTestService(t, t.TempDir())
			a := personalAccount(t, s, "claude")
			s.providerMode = true
			var calls atomic.Int32
			entered := make(chan struct{})
			s.recoverClaudeSession = func(ctx context.Context, _, _ string) error {
				calls.Add(1)
				close(entered)
				if kind == "cancel" {
					<-ctx.Done()
					return ctx.Err()
				}
				if kind == "changed" {
					s.accountsMu.Lock()
					s.accounts.Accounts[0].Generation++
					s.accountsMu.Unlock()
					return nil
				}
				return usage.ErrRecoveryBlocked
			}
			payload := map[string]any{"account_ref": a["account_ref"], "generation": a["generation"]}
			if kind == "disabled" {
				s.accountsMu.Lock()
				s.accounts.Accounts[0].Enabled = false
				s.accountsMu.Unlock()
			}
			if kind == "cancel" && usage.ClaudeSessionRecoverySupported() {
				done := make(chan responseView, 1)
				go func() { done <- call(t, s, "recover", "quota.account.recover", payload) }()
				<-entered
				r := call(t, s, "cancel", "quota.account.cancel", payload)
				if r.Error != nil {
					t.Fatal(r.Error)
				}
				select {
				case r := <-done:
					if r.Error == nil {
						t.Fatal("cancel succeeded")
					}
				case <-time.After(time.Second):
					t.Fatal("cancel did not stop")
				}
				return
			}
			r := call(t, s, "recover", "quota.account.recover", payload)
			if r.Error == nil {
				t.Fatalf("%s accepted", kind)
			}
			if kind == "disabled" && calls.Load() != 0 {
				t.Fatal("disabled owner launched")
			}
			if kind == "failure" && usage.ClaudeSessionRecoverySupported() {
				call(t, s, "again", "quota.account.recover", payload)
				if calls.Load() != 1 {
					t.Fatal("failure cooldown missing")
				}
			}
		})
	}
}

func TestClaudeRecoveryDeduplicatesAndRejectsWrongIdentity(t *testing.T) {
	t.Parallel()
	if !usage.ClaudeSessionRecoverySupported() {
		return
	}
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprint(wrong), func(t *testing.T) {
			s := accountTestService(t, t.TempDir())
			a := personalAccount(t, s, "claude")
			s.providerMode = true
			payload := map[string]any{"account_ref": a["account_ref"], "generation": a["generation"]}
			proof := "claude\x00expected\x00org\x00scope"
			key := sha256.Sum256([]byte(proof))
			s.accountsMu.Lock()
			home := s.accounts.Accounts[0].Home
			s.accounts.Accounts[0].IdentityKey = hex.EncodeToString(key[:])
			s.accounts.Accounts[0].Epoch = "old"
			s.accountsMu.Unlock()
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			s.recoverClaudeSession = func(ctx context.Context, _, _ string) error {
				if calls.Add(1) == 1 {
					close(entered)
				}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			now := time.Now().UTC()
			stamp := now.Format(time.RFC3339Nano)
			s.accountReader = func(context.Context, string, string) (accountSample, error) {
				p := proof
				if wrong {
					p = "other-account"
				}
				return accountSample{Epoch: "new", Proof: p, Quota: quotaResult{Provider: "claude", State: "fresh", LastSuccessAt: &stamp, Windows: []quotaWindow{{Name: "five_hour", Utilization: 5, ResetsAt: now.Add(time.Hour).Format(time.RFC3339Nano)}}}}, nil
			}
			done := make(chan responseView, 2)
			go func() { done <- call(t, s, "one", "quota.account.recover", payload) }()
			<-entered
			go func() { done <- call(t, s, "two", "quota.account.recover", payload) }()
			deadline := time.Now().Add(time.Second)
			for {
				s.accountFlightMu.Lock()
				f := s.recoveryFlights[home]
				joined := f != nil && f.waiters == 2
				s.accountFlightMu.Unlock()
				if joined {
					break
				}
				if time.Now().After(deadline) {
					close(release)
					t.Fatal("second waiter did not join")
				}
				runtime.Gosched()
			}
			close(release)
			for range 2 {
				select {
				case r := <-done:
					if wrong && r.Error == nil {
						t.Fatal("wrong identity accepted")
					}
					if !wrong && r.Error != nil {
						t.Fatal(r.Error)
					}
				case <-time.After(time.Second):
					t.Fatal("waiter stuck")
				}
			}
			if calls.Load() != 1 {
				t.Fatal("owner startup duplicated")
			}
		})
	}
}

func TestClaudeRecoveryWireNegotiation(t *testing.T) {
	t.Parallel()
	s := accountTestService(t, t.TempDir())

	input, requests := io.Pipe()
	responsesReader, output := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), s, input, output); _ = output.Close() }()
	t.Cleanup(func() { _ = requests.Close(); _ = responsesReader.Close() })
	decoder := json.NewDecoder(responsesReader)
	responses := map[string]responseView{}
	for _, request := range []string{
		`{"version":1,"kind":"request","id":"before","op":"quota.account.recover","payload":{"account_ref":"none","generation":1}}`,
		`{"version":1,"kind":"request","id":"hello","op":"hello","payload":{"quota_accounts":true}}`,
		`{"version":1,"kind":"request","id":"after","op":"quota.account.recover","payload":{"account_ref":"none","generation":1}}`,
	} {
		if _, err := io.WriteString(requests, request+"\n"); err != nil {
			t.Fatal(err)
		}
		var r responseView
		if err := decoder.Decode(&r); err != nil {
			t.Fatal(err)
		}
		responses[r.ID] = r
	}
	_ = requests.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if responses["before"].Error == nil || responses["before"].Error.Code != "capability_not_negotiated" {
		t.Fatal("recovery before negotiation accepted")
	}
	if responses["after"].Error == nil || responses["after"].Error.Code != "recovery_blocked" {
		t.Fatal("fixture recovered after negotiation")
	}
}
