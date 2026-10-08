package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// An install that cannot finish a self-update says so where someone can see it:
// the sync it is already making (#750). Locally the failure is a file nobody
// reads until the person is asked to go and look.

func TestUpdateStallReport_carriesTheRecordedFailure(t *testing.T) {
	// Given
	setupTestHome(t)
	failedAt := time.Date(2026, 9, 21, 4, 5, 6, 0, time.UTC)
	noteUpdateFailure("", "v0.7.56", "rename /usr/local/bin/cctrace: permission denied", failedAt)

	// When
	got := updateStallReport("")

	// Then
	if got == nil {
		t.Fatal("report = nil, want the recorded failure")
	}
	if got.TargetVersion != "v0.7.56" || got.Consecutive != 1 {
		t.Fatalf("target/consecutive = %q/%d, want v0.7.56/1", got.TargetVersion, got.Consecutive)
	}
	if !got.LastFailedAt.Equal(failedAt) {
		t.Fatalf("LastFailedAt = %v, want %v", got.LastFailedAt, failedAt)
	}
	if got.Reason != "rename /usr/local/bin/cctrace: permission denied" {
		t.Fatalf("Reason = %q, want the recorded message", got.Reason)
	}
}

// An install with nothing wrong still reports, and the report is empty rather
// than absent: absent is reserved for a build that cannot report at all, and
// reading the two as one is what left a month-old stall invisible.
func TestUpdateStallReport_saysNothingIsWrongRatherThanStayingSilent(t *testing.T) {
	// Given
	setupTestHome(t)

	// When
	got := updateStallReport("")

	// Then
	if got == nil {
		t.Fatal("report = nil, want an empty report (this build can report)")
	}
	if got.Consecutive != 0 || got.TargetVersion != "" || got.Reason != "" {
		t.Fatalf("report = %+v, want zero fields", *got)
	}
}

// The reporter has to be installed where every sync path picks it up, which is
// the one constructor they all go through.
func TestNewSyncClient_sendsTheUpdateStall(t *testing.T) {
	// Given
	setupTestHome(t)
	noteUpdateFailure("", "v0.7.56", "checksum mismatch", time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC))

	var body map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"inserted":0}`))
	}))
	defer server.Close()

	client, err := newSyncClient(&profile.Profile{}, server.URL, "v0.7.14", "")
	if err != nil {
		t.Fatal(err)
	}

	// When
	if _, err := client.Send(context.Background(), "claude", stallTestProfileEmail, "u", "hash", "proj",
		syncer.ProjectIdentity{}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Then
	raw, ok := body["update_stall"]
	if !ok {
		t.Fatal("update_stall missing from the sync payload")
	}
	var got struct {
		TargetVersion string `json:"target_version"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode update_stall: %v", err)
	}
	if got.TargetVersion != "v0.7.56" {
		t.Fatalf("target_version = %q, want v0.7.56", got.TargetVersion)
	}
}

// stallTestProfileEmail is the address these cases sync as; it carries no
// meaning beyond being well-formed.
const stallTestProfileEmail = "stalled@example.test"
