package otelrecv

import (
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func mkKV(key string, val *commonpb.AnyValue) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: val}
}

func strAV(s string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s}}
}

func intAV(i int64) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: i}}
}

func doubleAV(f float64) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: f}}
}

func boolAV(b bool) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: b}}
}

func TestFlattenAttrs(t *testing.T) {
	kvs := []*commonpb.KeyValue{
		mkKV("str_key", strAV("hello")),
		mkKV("int_key", intAV(42)),
		mkKV("double_key", doubleAV(3.14)),
		mkKV("bool_key", boolAV(true)),
	}

	m := flattenAttrs(kvs)

	if got, ok := m["str_key"].(string); !ok || got != "hello" {
		t.Errorf("str_key: got %v, want %q", m["str_key"], "hello")
	}
	if got, ok := m["int_key"].(int64); !ok || got != 42 {
		t.Errorf("int_key: got %v, want 42", m["int_key"])
	}
	if got, ok := m["double_key"].(float64); !ok || got != 3.14 {
		t.Errorf("double_key: got %v, want 3.14", m["double_key"])
	}
	if got, ok := m["bool_key"].(bool); !ok || got != true {
		t.Errorf("bool_key: got %v, want true", m["bool_key"])
	}
}

func TestFlattenAttrs_NilValue(t *testing.T) {
	kvs := []*commonpb.KeyValue{
		mkKV("good", strAV("ok")),
		{Key: "nil_val", Value: nil},
	}

	m := flattenAttrs(kvs)

	if _, ok := m["nil_val"]; ok {
		t.Error("expected nil_val to be skipped")
	}
	if got, ok := m["good"].(string); !ok || got != "ok" {
		t.Errorf("good: got %v, want %q", m["good"], "ok")
	}
}

func TestFlattenAttrs_Empty(t *testing.T) {
	m := flattenAttrs(nil)
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestStrVal(t *testing.T) {
	m := map[string]interface{}{
		"name": "alice",
		"age":  int64(30),
	}

	if got := strVal(m, "name"); got != "alice" {
		t.Errorf("got %q, want %q", got, "alice")
	}
	// Non-string value gets formatted
	if got := strVal(m, "age"); got != "30" {
		t.Errorf("got %q, want %q", got, "30")
	}
	// Missing key returns empty
	if got := strVal(m, "missing"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestStrValAny(t *testing.T) {
	m := map[string]interface{}{
		"tool_name": "bash",
		"tool.name": "read",
	}

	// First match wins
	got := strValAny(m, "tool.name", "tool_name")
	if got != "read" {
		t.Errorf("got %q, want %q", got, "read")
	}

	// Falls back to second key
	m2 := map[string]interface{}{
		"tool_name": "bash",
	}
	got = strValAny(m2, "tool.name", "tool_name")
	if got != "bash" {
		t.Errorf("got %q, want %q", got, "bash")
	}

	// All missing returns empty
	got = strValAny(m, "no.key", "also.no")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestAttrsGetAny(t *testing.T) {
	m := map[string]interface{}{
		"cost.usd": 1.23,
	}

	v, ok := attrsGetAny(m, "cost.usd", "cost_usd")
	if !ok || v != 1.23 {
		t.Errorf("got %v, %v; want 1.23, true", v, ok)
	}

	v, ok = attrsGetAny(m, "missing1", "missing2")
	if ok || v != nil {
		t.Errorf("got %v, %v; want nil, false", v, ok)
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want float64
	}{
		{"float64", float64(1.5), 1.5},
		{"int64", int64(42), 42.0},
		{"string", "3.14", 3.14},
		{"bad string", "nope", 0},
		{"nil", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toFloat64(tt.in); got != tt.want {
				t.Errorf("toFloat64(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestToInt(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want int
	}{
		{"int64", int64(100), 100},
		{"float64", float64(99.7), 99},
		{"string", "55", 55},
		{"bad string", "abc", 0},
		{"nil", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toInt(tt.in); got != tt.want {
				t.Errorf("toInt(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestToBool(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want bool
	}{
		{"bool true", true, true},
		{"bool false", false, false},
		{"string true", "true", true},
		{"string 1", "1", true},
		{"string false", "false", false},
		{"string 0", "0", false},
		{"int64 nonzero", int64(1), true},
		{"int64 zero", int64(0), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toBool(tt.in); got != tt.want {
				t.Errorf("toBool(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestToTime(t *testing.T) {
	ts := uint64(1700000000000000000) // ~2023-11-14

	// timeNano takes priority
	got := toTime(ts, 0)
	if got.Unix() != 1700000000 {
		t.Errorf("expected Unix 1700000000, got %d", got.Unix())
	}

	// Falls back to observedNano
	observed := uint64(1600000000000000000)
	got = toTime(0, observed)
	if got.Unix() != 1600000000 {
		t.Errorf("expected Unix 1600000000, got %d", got.Unix())
	}

	// Both zero returns ~now
	before := time.Now().Add(-time.Second)
	got = toTime(0, 0)
	after := time.Now().Add(time.Second)
	if got.Before(before) || got.After(after) {
		t.Errorf("expected ~now, got %v", got)
	}
}

func TestExtractResource(t *testing.T) {
	res := &resourcepb.Resource{
		Attributes: []*commonpb.KeyValue{
			mkKV("user.id", strAV("u123")),
			mkKV("user.email", strAV("alice@example.com")),
			mkKV("user.team", strAV("engineering")),
			mkKV("org.id", strAV("org-456")),
			mkKV("service.name", strAV("openai-codex")),
			mkKV("service.version", strAV("1.2.3")),
		},
	}

	ri := extractResource(res)

	if ri.userID != "u123" {
		t.Errorf("userID: got %q, want %q", ri.userID, "u123")
	}
	if ri.userEmail != "alice@example.com" {
		t.Errorf("userEmail: got %q, want %q", ri.userEmail, "alice@example.com")
	}
	if ri.userTeam != "engineering" {
		t.Errorf("userTeam: got %q, want %q", ri.userTeam, "engineering")
	}
	if ri.orgID != "org-456" {
		t.Errorf("orgID: got %q, want %q", ri.orgID, "org-456")
	}
	if ri.serviceName != "openai-codex" {
		t.Errorf("serviceName: got %q, want %q", ri.serviceName, "openai-codex")
	}
	if ri.serviceVersion != "1.2.3" {
		t.Errorf("serviceVersion: got %q, want %q", ri.serviceVersion, "1.2.3")
	}
}

func TestExtractResource_Nil(t *testing.T) {
	ri := extractResource(nil)
	if ri.userID != "" || ri.userEmail != "" || ri.userTeam != "" || ri.orgID != "" || ri.serviceName != "" || ri.serviceVersion != "" {
		t.Errorf("expected empty resourceInfo for nil, got %+v", ri)
	}
}

func TestExtractResource_Empty(t *testing.T) {
	res := &resourcepb.Resource{}
	ri := extractResource(res)
	if ri.userID != "" || ri.userEmail != "" {
		t.Errorf("expected empty resourceInfo for empty resource, got %+v", ri)
	}
}

func TestClassifyAgent_CodexResourceServiceName(t *testing.T) {
	for _, serviceName := range []string{"openai-codex", "codex_cli_rs", "codex-cli"} {
		t.Run(serviceName, func(t *testing.T) {
			agent, provider := classifyAgent(nil, resourceInfo{serviceName: serviceName})
			if agent != "codex" || provider != "openai" {
				t.Fatalf("got %s/%s, want codex/openai", agent, provider)
			}
		})
	}
}
