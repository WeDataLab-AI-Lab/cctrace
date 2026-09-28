package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

func (s *PgStore) InsertEvent(ctx context.Context, e *OtelEvent) error {
	return s.InsertEvents(ctx, []*OtelEvent{e})
}

func (s *PgStore) InsertEvents(ctx context.Context, events []*OtelEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := s.InsertEventsTx(ctx, tx, events); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// InsertEventsTx inserts events within an existing transaction.
func (s *PgStore) InsertEventsTx(ctx context.Context, tx pgx.Tx, events []*OtelEvent) error {
	if len(events) == 0 {
		return nil
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	live, err := liveSessionIDs(ctx, tx, sessionIDsFromEvents(events))
	if err != nil {
		return err
	}
	events = filterLiveEvents(events, live)
	if len(events) == 0 {
		return nil
	}

	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"otel_events"},
		[]string{
			"ts", "event_name", "session_id", "prompt_id",
			"user_id", "profile_email", "login_email", "user_name", "user_team", "org_id",
			"model", "cost_usd", "input_tokens", "output_tokens",
			"cache_read_tokens", "cache_create_tokens", "duration_ms",
			"tool_name", "tool_decision", "tool_success",
			"speed", "service_version", "attrs",
			"agent", "billing_provider",
		},
		&eventCopySource{events: events},
	)
	if err != nil {
		return err
	}
	return refreshSessionOverviewRollups(ctx, tx, eventSessionIDs(events))
}

func eventSessionIDs(events []*OtelEvent) []string {
	seen := make(map[string]struct{}, len(events))
	ids := make([]string, 0, len(events))
	for _, event := range events {
		if event.SessionID == "" {
			continue
		}
		if _, ok := seen[event.SessionID]; ok {
			continue
		}
		seen[event.SessionID] = struct{}{}
		ids = append(ids, event.SessionID)
	}
	return ids
}

func (s *PgStore) InsertMetric(ctx context.Context, m *OtelMetric) error {
	return s.InsertMetrics(ctx, []*OtelMetric{m})
}

func (s *PgStore) InsertMetrics(ctx context.Context, metrics []*OtelMetric) error {
	if len(metrics) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := s.InsertMetricsTx(ctx, tx, metrics); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// InsertMetricsTx inserts metrics within an existing transaction.
func (s *PgStore) InsertMetricsTx(ctx context.Context, tx pgx.Tx, metrics []*OtelMetric) error {
	if len(metrics) == 0 {
		return nil
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	live, err := liveSessionIDs(ctx, tx, sessionIDsFromMetrics(metrics))
	if err != nil {
		return err
	}
	metrics = filterLiveMetrics(metrics, live)
	if len(metrics) == 0 {
		return nil
	}

	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"otel_metrics"},
		[]string{
			"ts", "metric_name", "session_id",
			"user_id", "profile_email", "login_email", "user_team", "model",
			"value_double", "value_int", "agent", "billing_provider", "dimensions",
			"account_id",
		},
		&metricCopySource{metrics: metrics},
	)
	return err
}

type eventCopySource struct {
	events []*OtelEvent
	idx    int
}

func (cs *eventCopySource) Next() bool {
	cs.idx++
	return cs.idx <= len(cs.events)
}

func (cs *eventCopySource) Values() ([]interface{}, error) {
	e := cs.events[cs.idx-1]
	attrs, _ := json.Marshal(e.Attrs)
	agent := e.Agent
	if agent == "" {
		agent = "claude"
	}
	bp := e.BillingProvider
	if bp == "" {
		bp = "anthropic"
	}
	return []interface{}{
		e.Ts, e.EventName, e.SessionID, e.PromptID,
		e.UserID, e.ProfileEmail, e.LoginEmail, e.UserName, e.UserTeam, e.OrgID,
		e.Model, e.CostUSD, e.InputTokens, e.OutputTokens,
		e.CacheReadTokens, e.CacheCreateTokens, e.DurationMs,
		e.ToolName, e.ToolDecision, e.ToolSuccess,
		e.Speed, e.ServiceVersion, attrs,
		agent, bp,
	}, nil
}

func (cs *eventCopySource) Err() error { return nil }

type metricCopySource struct {
	metrics []*OtelMetric
	idx     int
}

func (cs *metricCopySource) Next() bool {
	cs.idx++
	return cs.idx <= len(cs.metrics)
}

func (cs *metricCopySource) Values() ([]interface{}, error) {
	m := cs.metrics[cs.idx-1]
	dims, _ := json.Marshal(m.Dimensions)
	return []interface{}{
		m.Ts, m.MetricName, m.SessionID,
		m.UserID, m.ProfileEmail, m.LoginEmail, m.UserTeam, m.Model,
		m.ValueDouble, m.ValueInt, m.Agent, m.BillingProvider, dims,
		m.AccountID,
	}, nil
}

func (cs *metricCopySource) Err() error { return nil }
