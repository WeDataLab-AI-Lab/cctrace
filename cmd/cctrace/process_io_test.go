package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/processio"
	"cctrace/internal/syncer"
)

func TestProcessIOQueueRetriesCountersAndSettlesOnlyAcceptedSnapshots(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	queue := processio.Queue{Path: filepath.Join(dir, "io.json")}
	first := queuedProcessIOForTest("synthetic-session", "acct-a")
	if err := queue.Upsert(first); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	captured := make(chan syncer.SessionObservationsRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req syncer.SessionObservationsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "decode failed", http.StatusBadRequest)
			return
		}
		captured <- req
		if calls.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(syncer.SessionObservationsResponse{Accepted: len(req.LiveStates), Stored: len(req.LiveStates)})
	}))
	defer server.Close()
	client := syncer.NewClient(server.URL, "synthetic-token", "test")
	flush := func() error {
		return flushProcessIOQueue(context.Background(), queue, client, "owner@example.test", "user-1", nil)
	}
	if err := flush(); err == nil {
		t.Fatal("first upload unexpectedly succeeded")
	}
	if items, err := queue.List(); err != nil || len(items) != 1 {
		t.Fatalf("queue after upload failure = %d, %v; want one retryable report", len(items), err)
	}
	<-captured
	if err := flush(); err != nil {
		t.Fatalf("retry upload: %v", err)
	}
	retried := <-captured
	if len(retried.LiveStates) != 1 {
		t.Fatalf("retried items = %d, want 1", len(retried.LiveStates))
	}
	state := retried.LiveStates[0]
	if state.SessionID != first.SessionID || state.IOEpoch != first.IOEpoch || state.IOAsOf == nil || state.InputBytes == nil || *state.InputBytes != 5 {
		t.Fatalf("uploaded process counters = %+v", state)
	}
	if strings.Contains(string(mustMarshal(t, retried)), "synthetic prompt") {
		t.Fatal("process stream text was sent with counters")
	}
	if items, err := queue.List(); err != nil || len(items) != 0 {
		t.Fatalf("queue after successful upload = %d, %v; want empty", len(items), err)
	}
	if err := queue.Upsert(queuedProcessIOForTest("synthetic-old-profile", "acct-a")); err != nil {
		t.Fatal(err)
	}
	if err := flushProcessIOQueue(context.Background(), queue, client, "changed@example.test", "user-1", nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("changed profile identity caused %d uploads; want 2", calls.Load())
	}
	excluded := queuedProcessIOForTest("synthetic-excluded", "acct-excluded")
	if err := queue.Upsert(excluded); err != nil {
		t.Fatal(err)
	}
	if err := flushProcessIOQueue(context.Background(), queue, client, "owner@example.test", "user-1", []string{"openai:acct-excluded"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("excluded account caused %d uploads; want 2", calls.Load())
	}
	if items, err := queue.List(); err != nil || len(items) != 0 {
		t.Fatalf("excluded queue item remains: %d, %v", len(items), err)
	}
}

func TestProcessIOLegacyQueueDoesNotReplayBusyAsNativeStatus(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	queue := processio.Queue{Path: filepath.Join(dir, "io.json")}
	legacy := queuedProcessIOForTest("legacy-session", "acct-a")
	legacy.Status, legacy.StatusSource = "busy", ""
	if err := queue.Upsert(legacy); err != nil {
		t.Fatal(err)
	}
	uploaded := make(chan syncer.SessionLiveStateItem, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request syncer.SessionObservationsRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "decode failed", http.StatusBadRequest)
			return
		}
		uploaded <- request.LiveStates[0]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(syncer.SessionObservationsResponse{Accepted: 1, Stored: 1})
	}))
	defer server.Close()
	client := syncer.NewClient(server.URL, "synthetic-token", "test")
	if err := flushProcessIOQueue(context.Background(), queue, client, "owner@example.test", "user-1", nil); err != nil {
		t.Fatal(err)
	}
	state := <-uploaded
	if state.Status != "unknown" || state.StatusSource != "process_io_unknown" {
		t.Fatalf("legacy retry status/source = %q/%q, want unknown/process_io_unknown", state.Status, state.StatusSource)
	}
}

func queuedProcessIOForTest(sessionID, accountID string) processio.QueuedReport {
	stdoutBytes, stdoutLines := int64(2), int64(1)
	stderrBytes, stderrLines := int64(3), int64(1)
	return processio.QueuedReport{
		ProfileEmail: "owner@example.test", UserID: "user-1", SessionID: sessionID, Agent: "codex", ProjectHash: "repo-hash", BillingProvider: "openai", AccountID: accountID,
		PID: 41, ProcStart: "darwin:100:0", Status: "unknown", StatusSource: "process_io_unknown", IOEpoch: "epoch-1",
		Report: processio.Report{AsOf: time.Now().UTC(), Mode: processio.ModePipes, Counts: processio.Snapshot{
			InputBytes: 5, InputLines: 1, OutputBytes: 5, OutputLines: 2,
			StdoutBytes: &stdoutBytes, StdoutLines: &stdoutLines, StderrBytes: &stderrBytes, StderrLines: &stderrLines,
		}},
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
