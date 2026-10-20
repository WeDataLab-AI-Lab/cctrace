package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// SessionTraceIndex is a metadata-only projection for the conversation request
// rail. It intentionally omits bodies, tool arguments and trace nodes.
type SessionTraceIndex struct {
	SessionID      string                   `json:"session_id"`
	State          string                   `json:"state"`
	RequestTotal   *int                     `json:"request_total"`
	Requests       []SessionTraceIndexEntry `json:"requests"`
	From           *time.Time               `json:"from,omitempty"`
	To             *time.Time               `json:"to,omitempty"`
	Coverage       string                   `json:"coverage"`
	RequestLimit   int                      `json:"request_limit"`
	RequestsAreAll bool                     `json:"requests_complete"`
}

type SessionTraceIndexEntry struct {
	RequestNo int       `json:"request_no"`
	TurnKey   string    `json:"turn_key"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Preview   string    `json:"preview"`
	// When the message that opened the request was written. A Codex turn's own
	// start is its first lifecycle event, often seconds after that message.
	OpenedAt     time.Time `json:"opened_at"`
	PreviewState string    `json:"preview_state"`
	Status       string    `json:"status"`
	APIRequests  int       `json:"api_requests"`
	Tools        int       `json:"tools"`
	Subagents    int       `json:"subagents"`
	Abandoned    bool      `json:"abandoned"`
}

// requestOrdinalSQL numbers request turns by their order in the session when
// read. Stored Codex request_no values shared one counter, so every turn of an
// affected session holds the session's total; ordinal is always right.
const requestOrdinalSQL = `(row_number() OVER (PARTITION BY session_id ORDER BY ordinal))::integer`

type SessionTraceIndexStore interface {
	SessionTraceIndex(context.Context, string, SessionAnalysisAccess) (SessionTraceIndex, error)
}

// SessionTraceIndex reads only turn metadata. It never loads preview bodies or
// starts a trace demand, so opening a conversation stays independent of the
// trace worker.
func (s *PgStore) SessionTraceIndex(parent context.Context, id string, access SessionAnalysisAccess) (SessionTraceIndex, error) {
	out := SessionTraceIndex{SessionID: id, State: "unavailable", Coverage: "none", Requests: []SessionTraceIndexEntry{}, RequestLimit: 2001}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='2s'`); err != nil {
		return out, err
	}
	if err = authorizeTrace(ctx, tx, id, access); err != nil {
		return out, err
	}
	f, err := analysisFact(ctx, tx, id, access)
	if err != nil && err != pgx.ErrNoRows {
		return out, err
	}
	if err == pgx.ErrNoRows {
		_, agent, rollErr := analysisRollup(ctx, tx, id, access)
		if rollErr != nil && rollErr != pgx.ErrNoRows {
			return out, rollErr
		}
		out.State, err = analysisMissingState(ctx, tx, id, agent)
		if err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	out.State = analysisFactState(f)
	if out.State != "ready" && out.State != "stale" {
		return out, tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `SELECT turn_key,`+requestOrdinalSQL+`,started_at,ended_at,COALESCE((detail #>> '{inputs,0,ts}')::timestamptz,started_at),preview,preview_state,status,api_requests,tool_calls,subagents,branch,count(*) OVER() FROM session_turn_facts WHERE session_id=$1 AND is_request AND request_no IS NOT NULL ORDER BY ordinal LIMIT 2001`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var e SessionTraceIndexEntry
		var branch string
		var total int
		if err = rows.Scan(&e.TurnKey, &e.RequestNo, &e.StartedAt, &e.EndedAt, &e.OpenedAt, &e.Preview, &e.PreviewState, &e.Status, &e.APIRequests, &e.Tools, &e.Subagents, &branch, &total); err != nil {
			rows.Close()
			return out, err
		}
		e.Abandoned = branch == "abandoned"
		out.Requests = append(out.Requests, e)
		out.RequestTotal = &total
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, err
	}
	if out.RequestTotal == nil {
		zero := 0
		out.RequestTotal = &zero
	}
	out.RequestsAreAll = requestsComplete(out.RequestTotal, len(out.Requests), out.RequestLimit)
	out.Coverage = "complete"
	if !out.RequestsAreAll || out.State == "stale" {
		out.Coverage = "partial"
	}
	if len(out.Requests) > 0 {
		out.From = &out.Requests[0].StartedAt
		last := out.Requests[len(out.Requests)-1].EndedAt
		out.To = &last
	}
	return out, tx.Commit(ctx)
}

// requestsComplete distinguishes a fully read index from a capped page. A
// count smaller than the cap is whole even if trailing rows were unreadable;
// a count above it is whole only when the returned rows match the count.
func requestsComplete(total *int, returned, limit int) bool {
	if total == nil {
		return false
	}
	return *total <= limit || returned == *total
}
