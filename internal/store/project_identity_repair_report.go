package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"cctrace/internal/gitctx"
)

type ProjectIdentityRepairSnapshot struct {
	RowCount int    `json:"row_count"`
	Checksum string `json:"checksum"`
}

// ProjectIdentityRepairHistorySnapshot describes the cardinality and checksum
// of the grouped session_records identity evidence without exposing hashes or
// repository identities.
type ProjectIdentityRepairHistorySnapshot struct {
	AggregateCount int    `json:"aggregate_count"`
	RecordCount    int64  `json:"record_count"`
	Checksum       string `json:"checksum"`
}

// ProjectIdentityRepairReportResult omits raw project hashes and remote URLs.
// ScopeFingerprint remains stable so two runs can be compared without exposing
// the source key.
type ProjectIdentityRepairReportResult struct {
	Agent            string                         `json:"agent"`
	ScopeFingerprint string                         `json:"scope_fingerprint"`
	Category         ProjectIdentityRepairCategory  `json:"category"`
	Reason           ProjectIdentityRepairReason    `json:"reason"`
	Proposal         *ProjectIdentityRepairProposal `json:"proposal,omitempty"`
}

// ProjectIdentityRepairReport is the stable operator output produced inside a
// repeatable-read, read-only transaction.
type ProjectIdentityRepairReport struct {
	SchemaVersion         int                                  `json:"schema_version"`
	Mode                  string                               `json:"mode"`
	ReadOnlyVerified      bool                                 `json:"read_only_verified"`
	DatabaseName          string                               `json:"database_name"`
	DatabaseVersion       string                               `json:"database_version"`
	SourceRelation        string                               `json:"source_relation"`
	SourceColumns         []string                             `json:"source_columns"`
	HistorySourceRelation string                               `json:"history_source_relation"`
	HistorySourceColumns  []string                             `json:"history_source_columns"`
	SnapshotBefore        ProjectIdentityRepairSnapshot        `json:"snapshot_before"`
	SnapshotAfter         ProjectIdentityRepairSnapshot        `json:"snapshot_after"`
	HistorySnapshotBefore ProjectIdentityRepairHistorySnapshot `json:"history_snapshot_before"`
	HistorySnapshotAfter  ProjectIdentityRepairHistorySnapshot `json:"history_snapshot_after"`
	SnapshotConsistent    bool                                 `json:"snapshot_consistent"`
	WriteQueries          int                                  `json:"write_queries"`
	Counts                ProjectIdentityRepairCounts          `json:"counts"`
	Results               []ProjectIdentityRepairReportResult  `json:"results"`
}

func projectIdentityRepairSnapshot(rows []ProjectIdentityRepairRow) ProjectIdentityRepairSnapshot {
	stable := append([]ProjectIdentityRepairRow(nil), rows...)
	sort.Slice(stable, func(i, j int) bool {
		left, right := projectIdentityRepairStableFields(stable[i]), projectIdentityRepairStableFields(stable[j])
		for index := range left {
			if left[index] != right[index] {
				return left[index] < right[index]
			}
		}
		return false
	})
	hash := sha256.New()
	for _, row := range stable {
		for _, field := range projectIdentityRepairStableFields(row) {
			fmt.Fprintf(hash, "%d:", len(field))
			hash.Write([]byte(field))
		}
	}
	return ProjectIdentityRepairSnapshot{RowCount: len(rows), Checksum: "sha256:" + hex.EncodeToString(hash.Sum(nil))}
}

func projectIdentityRepairStableFields(row ProjectIdentityRepairRow) [7]string {
	return [7]string{
		row.Agent,
		row.ProjectHash,
		row.ProjectName,
		gitctx.SanitizeRemoteURL(row.GitRemoteURL),
		gitctx.SanitizeRemoteURL(row.RepositoryID),
		row.RepositoryName,
		row.RepoSubpath,
	}
}

func projectIdentityRepairHistorySnapshot(rows []ProjectIdentityRepairHistoryRow) ProjectIdentityRepairHistorySnapshot {
	stable := append([]ProjectIdentityRepairHistoryRow(nil), rows...)
	sort.Slice(stable, func(i, j int) bool {
		left, right := projectIdentityRepairStableHistoryFields(stable[i]), projectIdentityRepairStableHistoryFields(stable[j])
		for index := range left {
			if left[index] != right[index] {
				return left[index] < right[index]
			}
		}
		return false
	})
	hash := sha256.New()
	var recordCount int64
	for _, row := range stable {
		for _, field := range projectIdentityRepairStableHistoryFields(row) {
			fmt.Fprintf(hash, "%d:", len(field))
			hash.Write([]byte(field))
		}
		recordCount += row.RecordCount
	}
	return ProjectIdentityRepairHistorySnapshot{
		AggregateCount: len(rows),
		RecordCount:    recordCount,
		Checksum:       "sha256:" + hex.EncodeToString(hash.Sum(nil)),
	}
}

func projectIdentityRepairStableHistoryFields(row ProjectIdentityRepairHistoryRow) [7]string {
	return [7]string{
		row.Agent,
		row.ProjectHash,
		gitctx.SanitizeRemoteURL(strings.TrimSpace(row.RepositoryID)),
		strings.TrimSpace(row.RepoSubpath),
		strconv.FormatInt(row.RecordCount, 10),
		row.FirstSeen.UTC().Format(time.RFC3339Nano),
		row.LastSeen.UTC().Format(time.RFC3339Nano),
	}
}

func projectIdentityRepairReportResults(results []ProjectIdentityRepairResult) []ProjectIdentityRepairReportResult {
	report := make([]ProjectIdentityRepairReportResult, 0, len(results))
	for _, result := range results {
		sum := sha256.Sum256([]byte(result.Agent + "\x00" + result.ProjectHash))
		report = append(report, ProjectIdentityRepairReportResult{
			Agent:            projectIdentityRepairReportAgent(result.Agent),
			ScopeFingerprint: "sha256:" + hex.EncodeToString(sum[:]),
			Category:         result.Category,
			Reason:           result.Reason,
			Proposal:         result.Proposal,
		})
	}
	return report
}

func projectIdentityRepairReportAgent(raw string) string {
	agent := strings.TrimSpace(raw)
	if agent == "" || len(agent) > 32 || strings.ContainsAny(agent, "@/:\\\x00\r\n") {
		return "unknown"
	}
	return agent
}
