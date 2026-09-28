package api

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"cctrace/internal/store"
)

type volumeInfo struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

type storageResponse struct {
	Tables []*store.TableRetention `json:"tables"`
	Volume *volumeInfo             `json:"volume"`
	// EnvManaged flags axes pinned by an env var (OTEL_/SESSION_RETENTION_DAYS):
	// the boot reconcile re-asserts the env value, so a UI edit would be reverted.
	// The UI disables editing for a pinned axis.
	EnvManaged map[string]bool `json:"env_managed"`
}

// envRetentionDays parses an axis env var the SAME way boot does (cmd/cctraced
// envIntOpt): blank or non-integer => nil (not pinned). Keeping this rule in sync
// ensures env_managed ⟺ the boot reconcile actually pins the axis.
func envRetentionDays(key string) *int {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return nil
	}
	return &n
}

// axisEnvManaged reports whether an axis's retention is pinned by a VALID env var,
// in which case boot reconcile overrides any edit — so both the UI and the write
// handler refuse to edit it.
func axisEnvManaged() map[string]bool {
	return map[string]bool{
		"otel":    envRetentionDays("OTEL_RETENTION_DAYS") != nil,
		"session": envRetentionDays("SESSION_RETENTION_DAYS") != nil,
	}
}

// handleRetention is an admin-only, read-only view of retention/compression
// policies, hypertable sizes, data-volume free space, and which axes are
// env-pinned. It never mutates any policy.
func (s *Server) handleRetention(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}

	resp, err := s.buildStorageResponse(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// buildStorageResponse assembles the full storage snapshot (tables + volume +
// env_managed) used by BOTH the GET read and the PATCH apply response, so the
// two share one shape (matching the TS StorageReport type).
func (s *Server) buildStorageResponse(ctx context.Context) (storageResponse, error) {
	report, err := s.store.RetentionInfo(ctx)
	if err != nil {
		return storageResponse{}, err
	}
	resp := storageResponse{Tables: report.Tables, EnvManaged: axisEnvManaged()}
	if s.dataPath != "" {
		if total, free, used, derr := diskUsage(s.dataPath); derr != nil {
			log.Printf("[cctrace] disk usage probe failed for %s: %v", s.dataPath, derr)
		} else if total > 0 {
			// total==0 means the platform has no statfs support -> omit the panel.
			resp.Volume = &volumeInfo{Path: s.dataPath, TotalBytes: total, FreeBytes: free, UsedBytes: used}
		}
	}
	return resp, nil
}
