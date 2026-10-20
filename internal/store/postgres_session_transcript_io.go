package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
	"cctrace/internal/sessiontranscript"
)

// loadSessionTranscriptIO keeps raw JSON in the worker only until each row
// has been measured; cursor batches bound individual database statements.
func loadSessionTranscriptIO(ctx context.Context, tx pgx.Tx, sessionID string) (*sessionfacts.TranscriptIODetail, error) {
	return loadSessionTranscriptIOWithProgress(ctx, tx, sessionID, nil)
}

func loadSessionTranscriptIOWithProgress(ctx context.Context, tx pgx.Tx, sessionID string, progress func()) (*sessionfacts.TranscriptIODetail, error) {
	counters := make(map[personalIdentity]*sessiontranscript.Counter)
	query := `SELECT user_id,lower(profile_email),ts,COALESCE(agent,''),record_type,COALESCE(prompt_source,''),
		COALESCE(entrypoint,''),COALESCE(command_name,''),is_sidechain,is_meta,is_compact_summary,
		(COALESCE(model,'')='<synthetic>'),raw
		FROM visible_session_records
		WHERE session_id=$1
		AND record_type IN ('user','assistant')
		ORDER BY ts,id`
	err := scanSessionFactCursor(ctx, tx, query, []any{sessionID}, sessionFactBatchRows, func(row pgx.Row) error {
		var identity personalIdentity
		var record sessiontranscript.Record
		var raw json.RawMessage
		if err := row.Scan(&identity.userID, &identity.email, &record.ObservedAt, &record.Agent, &record.RecordType, &record.PromptSource,
			&record.Entrypoint, &record.CommandName, &record.IsSidechain, &record.IsMeta, &record.IsCompactSummary,
			&record.SyntheticOutput,
			&raw); err != nil {
			return err
		}
		record.Raw = raw
		counter := counters[identity]
		if counter == nil {
			counter = sessiontranscript.NewCounter()
			counters[identity] = counter
		}
		counter.Add(record)
		if progress != nil {
			progress()
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	result := &sessionfacts.TranscriptIODetail{Version: sessionfacts.TranscriptIOVersion,
		Owners: make([]sessionfacts.TranscriptIOOwner, 0, len(counters))}
	for identity, counter := range counters {
		result.Owners = append(result.Owners, sessionfacts.TranscriptIOOwner{
			UserID: identity.userID, ProfileEmail: identity.email, Measurement: counter.Snapshot()})
	}
	sort.Slice(result.Owners, func(i, j int) bool {
		if result.Owners[i].UserID != result.Owners[j].UserID {
			return result.Owners[i].UserID < result.Owners[j].UserID
		}
		return result.Owners[i].ProfileEmail < result.Owners[j].ProfileEmail
	})
	return result, nil
}

// analysisTranscriptIO reads only the scalar cache for the currently verified
// owner. Missing measurements remain unknown until the automatic worker publishes.
func analysisTranscriptIO(f *sessionfacts.Session, owner *personalOwner, stale bool) map[string]interface{} {
	measurement := sessiontranscript.Measurement{}
	if cache := f.Detail.TranscriptIO; cache != nil && cache.Version == sessionfacts.TranscriptIOVersion {
		for _, item := range cache.Owners {
			if item.UserID == owner.userID && strings.EqualFold(item.ProfileEmail, owner.email) {
				measurement = item.Measurement
				break
			}
		}
	}
	freshness := "unknown"
	if measurement.AsOf != nil {
		freshness = "fresh"
		if measurement.Partial {
			freshness = "partial"
		} else if measurement.InputBytes == nil || measurement.InputLines == nil || measurement.OutputBytes == nil || measurement.OutputLines == nil {
			freshness = "unknown"
		} else if stale {
			freshness = "stale"
		}
	}
	return map[string]interface{}{
		"pid": nil, "input_lines": measurement.InputLines, "output_lines": measurement.OutputLines,
		"input_bytes": measurement.InputBytes, "output_bytes": measurement.OutputBytes,
		"stdout_lines": nil, "stdout_bytes": nil, "stderr_lines": nil, "stderr_bytes": nil,
		"io_source": "transcript", "io_scope": "session", "io_mode": nil,
		"io_as_of": analysisTime(measurement.AsOf), "io_epoch": nil,
		"io_freshness": freshness, "io_partial": measurement.Partial, "io_failure_code": nil,
	}
}
