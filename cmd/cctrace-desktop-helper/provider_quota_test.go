package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/codexappserver"
	"cctrace/internal/desktop"
	"cctrace/internal/usage"
)

type helperQuotaReader struct {
	claude func(context.Context, string) (*usage.Response, error)
	codex  func(context.Context, string) (*codexappserver.Snapshot, error)

	claudeHomes []string
	codexHomes  []string
}

func (r *helperQuotaReader) ReadClaudeQuota(ctx context.Context, home string) (*usage.Response, error) {
	r.claudeHomes = append(r.claudeHomes, home)
	if r.claude == nil {
		return nil, nil
	}
	return r.claude(ctx, home)
}

func (r *helperQuotaReader) ReadCodexQuota(ctx context.Context, home string) (*codexappserver.Snapshot, error) {
	r.codexHomes = append(r.codexHomes, home)
	if r.codex == nil {
		return nil, nil
	}
	return r.codex(ctx, home)
}

func TestRunDefaultModeNeverInvokesProviderReader(t *testing.T) {
	root := t.TempDir()
	reader := &helperQuotaReader{
		claude: func(context.Context, string) (*usage.Response, error) {
			t.Fatal("default helper mode called Claude provider reader")
			return nil, nil
		},
		codex: func(context.Context, string) (*codexappserver.Snapshot, error) {
			t.Fatal("default helper mode called Codex provider reader")
			return nil, nil
		},
	}
	var stdout, diagnostics bytes.Buffer
	request := `{"version":1,"kind":"request","id":"quota","op":"quota.read","payload":{"provider":"claude"}}` + "\n"
	code := runWithProviderReader([]string{"--app-support", filepath.Join(root, "app support")}, strings.NewReader(request), &stdout, &diagnostics, reader)
	if code != 0 || diagnostics.Len() != 0 {
		t.Fatalf("default run code=%d diagnostics=%q", code, diagnostics.String())
	}
	responses := parseResponses(t, stdout.Bytes())
	if len(responses) != 1 || responses[0].Error != nil {
		t.Fatalf("default response = %+v", responses)
	}
	var quota struct {
		State string `json:"state"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responses[0].Result, &quota); err != nil || quota.State != "unavailable" || quota.Error == nil || quota.Error.Code != "fixture_unavailable" {
		t.Fatalf("default quota=%+v err=%v", quota, err)
	}
	if len(reader.claudeHomes) != 0 || len(reader.codexHomes) != 0 {
		t.Fatalf("default mode invoked readers: Claude=%v Codex=%v", reader.claudeHomes, reader.codexHomes)
	}
}

func TestRunProviderModeUsesOnlyExplicitConfiguredHome(t *testing.T) {
	root := t.TempDir()
	claudeHome := filepath.Join(root, "Claude Explicit 日本語")
	if err := os.Mkdir(claudeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	claudeHome, err := filepath.EvalSymlinks(claudeHome)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reader := &helperQuotaReader{claude: func(_ context.Context, home string) (*usage.Response, error) {
		if home != claudeHome {
			t.Fatalf("provider reader home=%q, want %q", home, claudeHome)
		}
		return &usage.Response{
			FetchedAt: now,
			FiveHour:  &usage.UsageWindow{Utilization: 0, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
		}, nil
	}}
	args := []string{"--app-support", filepath.Join(root, "app support"), "--provider-mode", "--profile-root", filepath.Join(root, "registry"), "--claude-home", claudeHome}
	request := `{"version":1,"kind":"request","id":"quota","op":"quota.read","payload":{"provider":"claude"}}` + "\n"
	var stdout, diagnostics bytes.Buffer
	code := runWithProviderReader(args, strings.NewReader(request), &stdout, &diagnostics, reader)
	if code != 0 || diagnostics.Len() != 0 {
		t.Fatalf("provider run code=%d diagnostics=%q", code, diagnostics.String())
	}
	responses := parseResponses(t, stdout.Bytes())
	if len(responses) != 1 || responses[0].Error != nil {
		t.Fatalf("provider response = %+v", responses)
	}
	var quota struct {
		State   string `json:"state"`
		Windows []struct {
			Name        string  `json:"name"`
			Utilization float64 `json:"utilization"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(responses[0].Result, &quota); err != nil || quota.State != "fresh" || len(quota.Windows) != 1 || quota.Windows[0].Name != "five_hour" || quota.Windows[0].Utilization != 0 {
		t.Fatalf("provider quota=%+v err=%v", quota, err)
	}
	if len(reader.claudeHomes) != 1 || reader.claudeHomes[0] != claudeHome || len(reader.codexHomes) != 0 {
		t.Fatalf("reader calls: Claude=%v Codex=%v", reader.claudeHomes, reader.codexHomes)
	}
	var hello struct {
		Capabilities struct {
			FixtureOnly bool `json:"fixture_only"`
		} `json:"capabilities"`
	}
	var helloLine bytes.Buffer
	if err := json.NewEncoder(&helloLine).Encode(map[string]any{"version": 1, "kind": "request", "id": "hello", "op": "hello", "payload": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	var helloOut, helloDiagnostics bytes.Buffer
	code = runWithProviderReader(args, bytes.NewReader(helloLine.Bytes()), &helloOut, &helloDiagnostics, reader)
	if code != 0 || helloDiagnostics.Len() != 0 {
		t.Fatalf("provider hello code=%d diagnostics=%q", code, helloDiagnostics.String())
	}
	helloResponses := parseResponses(t, helloOut.Bytes())
	if len(helloResponses) != 1 || json.Unmarshal(helloResponses[0].Result, &hello) != nil || hello.Capabilities.FixtureOnly {
		t.Fatalf("provider hello capabilities=%+v", hello.Capabilities)
	}
}

func TestRunProviderModeAllowsUnconfiguredHomesAndRejectsInvalidConfiguration(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "fixture")
	if err := os.Mkdir(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	reader := &helperQuotaReader{}
	var unconfiguredStdout, unconfiguredDiagnostics bytes.Buffer
	unconfiguredRequest := `{"version":1,"kind":"request","id":"unconfigured","op":"quota.read","payload":{"provider":"claude"}}` + "\n"
	code := runWithProviderReader([]string{"--app-support", filepath.Join(root, "unconfigured app"), "--provider-mode", "--profile-root", filepath.Join(root, "registry")}, strings.NewReader(unconfiguredRequest), &unconfiguredStdout, &unconfiguredDiagnostics, reader)
	if code != 0 || unconfiguredDiagnostics.Len() != 0 {
		t.Fatalf("unconfigured provider run code=%d diagnostics=%q", code, unconfiguredDiagnostics.String())
	}
	unconfiguredResponses := parseResponses(t, unconfiguredStdout.Bytes())
	if len(unconfiguredResponses) != 1 || unconfiguredResponses[0].Error != nil {
		t.Fatalf("unconfigured provider response=%+v", unconfiguredResponses)
	}
	var unconfiguredQuota struct {
		State string `json:"state"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(unconfiguredResponses[0].Result, &unconfiguredQuota); err != nil || unconfiguredQuota.State != "unavailable" || unconfiguredQuota.Error == nil || unconfiguredQuota.Error.Code != "provider_not_configured" {
		t.Fatalf("unconfigured quota=%+v err=%v", unconfiguredQuota, err)
	}
	for _, args := range [][]string{
		{"--app-support", filepath.Join(root, "b"), "--provider-mode", "--profile-root", filepath.Join(root, "registry"), "--claude-home", "relative"},
		{"--app-support", filepath.Join(root, "c"), "--provider-mode", "--profile-root", filepath.Join(root, "registry"), "--claude-home", filepath.Join(root, "home"), "--fixture-root", fixture},
		{"--app-support", filepath.Join(root, "d"), "--provider-mode", "--profile-root", filepath.Join(root, "registry"), "--claude-home", fixture, "--clock", "2026-10-06T12:00:00Z"},
	} {
		var stdout, diagnostics bytes.Buffer
		code := runWithProviderReader(args, strings.NewReader(""), &stdout, &diagnostics, reader)
		if code != 2 || stdout.Len() != 0 || diagnostics.String() != "configuration_error\n" {
			t.Fatalf("run args %v => code=%d stdout=%q diagnostics=%q", args, code, stdout.String(), diagnostics.String())
		}
	}
	if len(reader.claudeHomes) != 0 || len(reader.codexHomes) != 0 {
		t.Fatalf("invalid configuration invoked provider reader: Claude=%v Codex=%v", reader.claudeHomes, reader.codexHomes)
	}
}

var _ desktop.ProviderQuotaReader = (*helperQuotaReader)(nil)
