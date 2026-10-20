package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

// computeSessionFactSnapshot reads payload twice from one stable snapshot.
// Only compact index metadata and completed facts survive each borrowed row;
// no source-row slice is reconstructed for the publisher.
func (s *PgStore) computeSessionFactSnapshot(parent context.Context, id, agent string) (load SessionFactLoad, out sessionfacts.TimelineCommitOutput, err error) {
	return s.computeSessionFactReadSnapshot(parent, id, agent, true)
}

// requireQueue is false only for explicit read-only measurement. The production
// worker requires a generation; replay never creates one or publishes output.
func (s *PgStore) computeSessionFactReadSnapshot(parent context.Context, id, agent string, requireQueue bool) (load SessionFactLoad, out sessionfacts.TimelineCommitOutput, err error) {
	ctx, progress, stop := sessionFactProgress(parent, sessionFactReadLimit)
	defer stop()
	phase := "snapshot"
	defer func() {
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				err = errors.Join(err, cause)
			}
			err = fmt.Errorf("session fact %s: %w", phase, err)
		}
	}()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return load, out, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err = tx.Exec(ctx, sessionFactStatementLimit); err != nil {
		return load, out, err
	}
	phase = "metadata"
	load.Input.Agent = agent
	load.LoadedKeys = []SessionTimelineKey{}
	if err = loadSessionFactSnapshotMetadata(ctx, tx, id, &load, progress, requireQueue); err != nil {
		return load, out, err
	}
	index := sessionfacts.NewIndexBuilder(load.Input, func(sessionfacts.Progress) { progress() })
	calls := make(map[sessionFactToolCallKey]bool)
	phase = "index"
	err = scanSessionFactSource(ctx, tx, id, agent,
		func(ordinal int64, row sessionfacts.ClaudeRow) error {
			load.LoadedKeys = append(load.LoadedKeys, SessionTimelineKey{Timestamp: row.TS, UUID: row.UUID, RecordID: row.ID, RecordType: row.RecordType, ProfileEmail: row.ProfileEmail})
			addSessionFactClaudeToolCalls(row, calls)
			progress()
			return index.AddClaude(ctx, ordinal, row)
		},
		func(ordinal int64, row sessionfacts.CodexRow) error {
			load.LoadedKeys = append(load.LoadedKeys, SessionTimelineKey{Timestamp: row.TS, UUID: row.UUID, RecordID: row.ID, RecordType: row.RecordType, ProfileEmail: row.ProfileEmail})
			addSessionFactCodexToolCalls(row, calls)
			progress()
			return index.AddCodex(ctx, ordinal, row)
		})
	if err != nil {
		return load, out, err
	}
	phase = "index_finalize"
	accumulator, err := index.Seal(ctx)
	if err != nil {
		return load, out, err
	}
	defer accumulator.Close()
	progress()
	phase = "evidence"
	if err = scanSessionFactSelectedResults(ctx, tx, id, agent, calls, func(result sessionfacts.CommitResult) error {
		progress()
		return accumulator.AddCommitResult(ctx, result)
	}); err != nil {
		return load, out, err
	}
	if err = scanSessionFactHeadEvidence(ctx, tx, id, agent, func(head sessionfacts.Head) error {
		progress()
		return accumulator.AddHead(ctx, head)
	}); err != nil {
		return load, out, err
	}
	if err = scanSessionFactEventEvidence(ctx, tx, id, func(event sessionfacts.OTELRow) error {
		progress()
		return accumulator.AddEvent(ctx, event)
	}); err != nil {
		return load, out, err
	}
	phase = "payload"
	claudeOrder, codexOrder := accumulator.ClaudeOrder(), accumulator.CodexOrder()
	err = scanSessionFactSource(ctx, tx, id, agent,
		func(ordinal int64, row sessionfacts.ClaudeRow) error {
			if int(ordinal) >= len(claudeOrder) || claudeOrder[ordinal].Ordinal != ordinal {
				return errors.New("claude replay order changed")
			}
			progress()
			return accumulator.ApplyClaude(ctx, ordinal, row)
		},
		func(ordinal int64, row sessionfacts.CodexRow) error {
			if int(ordinal) >= len(codexOrder) || codexOrder[ordinal].Ordinal != ordinal {
				return errors.New("codex replay order changed")
			}
			progress()
			return accumulator.ApplyCodex(ctx, ordinal, row)
		})
	if err != nil {
		return load, out, err
	}
	phase = "snapshot_commit"
	if err = tx.Commit(ctx); err != nil {
		return load, out, err
	}
	progress()
	phase = "finalize"
	out, err = accumulator.Finish(ctx)
	return load, out, err
}

func loadSessionFactSnapshotMetadata(ctx context.Context, tx pgx.Tx, id string, load *SessionFactLoad, progress func(), requireQueue bool) error {
	in := &load.Input
	if err := loadSessionFactRetention(ctx, tx, load); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&in.ReadAt); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT generation,removal_generation,reasons,scan_from_id FROM session_fact_dirty WHERE session_id=$1`, id).
		Scan(&load.Generation, &load.RemovalGeneration, &load.Reasons, &load.ScanFromID); err != nil && (requireQueue || !errors.Is(err, pgx.ErrNoRows)) {
		return err
	}
	if err := tx.QueryRow(ctx, sessionFactAgentSQL, id).Scan(&load.SourceAgent); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var oldest *time.Time
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name='session_fact_copies_v1'),
		EXISTS(SELECT 1 FROM otel_events WHERE session_id=$1),(SELECT min(ts) FROM otel_events)`, id).
		Scan(&in.CopyGate, &in.HasOTEL, &oldest); err != nil {
		return err
	}
	if oldest != nil {
		in.OTELOldest = *oldest
	}
	progress()
	if err := loadSessionFactSnapshots(ctx, tx, id, in); err != nil {
		return err
	}
	if err := loadSessionFactCommitClaims(ctx, tx, id, load); err != nil {
		return err
	}
	progress()
	rows, err := tx.Query(ctx, `SELECT srec_id FROM session_record_copies WHERE session_id=$1 ORDER BY srec_id`, id)
	if err != nil {
		return err
	}
	in.CopiedIDs, err = pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	load.CopyGroup, err = loadSessionFactCopyGroup(ctx, tx, id, load.ScanFromID)
	if err != nil {
		return err
	}
	in.CopyState, in.CopySource = "observed", "session_records"
	if load.CopyGroup.Skipped {
		in.CopyState = "skipped_large_group"
	}
	progress()
	witness, err := readSessionTimelineState(ctx, tx, id)
	if err != nil {
		return err
	}
	load.TimelineWitness = &witness
	progress()
	in.TranscriptIO, err = loadSessionTranscriptIOWithProgress(ctx, tx, id, progress)
	progress()
	return err
}

func scanSessionFactSource(ctx context.Context, tx pgx.Tx, id, agent string, claude func(int64, sessionfacts.ClaudeRow) error, codex func(int64, sessionfacts.CodexRow) error) error {
	query := sessionFactClaudeSlimSQL + ` ORDER BY sr.ts,sr.uuid COLLATE "C",sr.id,sr.record_type COLLATE "C",sr.profile_email COLLATE "C"`
	if agent == "codex" {
		query = sessionFactCodexSlimSQL + ` ORDER BY sr.ts,sr.id,sr.uuid COLLATE "C",sr.record_type COLLATE "C",sr.profile_email COLLATE "C"`
	}
	var ordinal int64
	return scanSessionFactCursor(ctx, tx, query, []any{id}, sessionFactBatchRows, func(row pgx.Row) error {
		defer func() { ordinal++ }()
		if agent == "codex" {
			decoded, err := scanSessionFactCodexRow(row)
			if err != nil {
				return err
			}
			return codex(ordinal, decoded)
		}
		decoded, err := scanSessionFactClaudeRow(row)
		if err != nil {
			return err
		}
		return claude(ordinal, decoded)
	})
}

func scanSessionFactHeadEvidence(ctx context.Context, tx pgx.Tx, id, agent string, visit func(sessionfacts.Head) error) error {
	query := sessionFactClaudeHeadsSQL + ` ORDER BY h.ts,h.id`
	if agent == "codex" {
		query = sessionFactCodexHeadsSQL + ` ORDER BY sr.ts,sr.id`
	}
	return scanSessionFactCursor(ctx, tx, query, []any{id}, sessionFactBatchRows, func(row pgx.Row) error {
		var head sessionfacts.Head
		if err := row.Scan(&head.ID, &head.UUID, &head.TS, &head.IsSidechain, &head.AgentID, &head.SourceFile, &head.Model,
			&head.InputTokens, &head.OutputTokens, &head.CacheReadTokens, &head.CacheCreateTokens,
			&head.Imputed, &head.CostUSD, &head.HasRate); err != nil {
			return err
		}
		return visit(head)
	})
}

func scanSessionFactEventEvidence(ctx context.Context, tx pgx.Tx, id string, visit func(sessionfacts.OTELRow) error) error {
	query := sessionFactVisibleOTELSQL + ` ORDER BY e.ts,e.prompt_id COLLATE "C",COALESCE(e.cost_usd,0)`
	return scanSessionFactCursor(ctx, tx, query, []any{id}, sessionFactBatchRows, func(row pgx.Row) error {
		var event sessionfacts.OTELRow
		if err := row.Scan(&event.TS, &event.PromptID, &event.CostUSD, &event.UserID, &event.ProfileEmail, &event.LoginEmail, &event.BillingProvider); err != nil {
			return err
		}
		return visit(event)
	})
}
