package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

func sameRetentionState(a, b SessionFactRetentionState) bool {
	return sameNullableTime(a.LastRunFinished, b.LastRunFinished) && sameNullableTime(a.OldestSourceTS, b.OldestSourceTS)
}

func sameNullableTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func (s *PgStore) publishEmptySessionFact(ctx context.Context, id string, generation int64) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionFactPublish(ctx, tx); err != nil {
		return "", err
	}
	if err := lockSessionDeletionTargets(ctx, tx, []string{id}); err != nil {
		return "", err
	}
	var tombstoned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deleted_sessions WHERE session_id = $1)`, id).Scan(&tombstoned); err != nil {
		return "", err
	}
	var current, removal int64
	if err := tx.QueryRow(ctx, `SELECT generation, removal_generation FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&current, &removal); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "stale", nil
		}
		return "", err
	}
	if !tombstoned && (current != generation || removal > generation) {
		return "stale", nil
	}
	if !tombstoned {
		var hasSource bool
		if err := tx.QueryRow(ctx, `SELECT
			EXISTS(SELECT 1 FROM visible_session_records WHERE session_id = $1 AND COALESCE(NULLIF(agent,''),'claude') IN ('claude','codex'))
			OR EXISTS(SELECT 1 FROM visible_events WHERE session_id = $1 AND event_name = 'api_request'
				AND agent IN ('claude','codex')
				AND NOT EXISTS (SELECT 1 FROM excluded_sessions x WHERE x.session_id = $1))
			OR EXISTS(SELECT 1 FROM session_snapshots ss WHERE `+sessionFactSnapshotVisibleSQL+`
				AND ss.agent IN ('claude','codex'))`, id).Scan(&hasSource); err != nil {
			return "", err
		}
		if hasSource {
			return "source", nil
		}
		if err := deleteSessionFacts(ctx, tx, []string{id}); err != nil {
			return "", err
		}
	}
	deleted, err := tx.Exec(ctx, `DELETE FROM session_fact_dirty WHERE session_id = $1 AND generation = $2`, id, generation)
	if err != nil {
		return "", err
	}
	if !tombstoned && deleted.RowsAffected() == 0 {
		return "stale", nil
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if tombstoned {
		return "tombstone", nil
	}
	return "empty", nil
}

func lockSessionFactPublish(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_retention'))`); err != nil {
		return err
	}
	return lockSessionOverviewSourceMutation(ctx, tx, false)
}

// publishSessionFact publishes a base-only result with no timeline or P4 facts;
// the session's mapping is removed, so it reads as not computed.
func (s *PgStore) publishSessionFact(ctx context.Context, id string, picked int64, load SessionFactLoad, output sessionfacts.Output, computeMS int) (string, error) {
	return s.publishSessionFactWithTimeline(ctx, id, picked, load, output, nil, computeMS)
}

// publishSessionFactWithTimeline publishes the facts and the timeline in one
// transaction. The session's deletion-target lock follows the retention and
// source locks before any source check, so the witness the timeline is checked
// against cannot move under a cooperative writer. A timeline whose source moved
// since the load rolls everything back and leaves the queue row for a fresh
// computation ("timeline_source"), as does a queue that moved before the final
// delete.
func (s *PgStore) publishSessionFactWithTimeline(ctx context.Context, id string, picked int64, load SessionFactLoad, output sessionfacts.Output, timeline *sessionFactTimeline, computeMS int) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionFactPublish(ctx, tx); err != nil {
		return "", err
	}
	copyLockIDs := []string{id}
	if load.CopyGroup != nil && !load.CopyGroup.Skipped {
		copyLockIDs = append(copyLockIDs, load.CopyGroup.Members...)
	}
	if err := lockSessionDeletionTargets(ctx, tx, copyLockIDs); err != nil {
		return "", err
	}
	var tombstoned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deleted_sessions WHERE session_id = $1)`, id).Scan(&tombstoned); err != nil {
		return "", err
	}
	if tombstoned {
		if _, err := tx.Exec(ctx, `DELETE FROM session_fact_dirty WHERE session_id = $1`, id); err != nil {
			return "", err
		}
		return "tombstone", tx.Commit(ctx)
	}
	var removal int64
	if err := tx.QueryRow(ctx, `SELECT removal_generation FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&removal); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "stale", nil
		}
		return "", err
	}
	if removal > load.Generation || load.RemovalGeneration > load.Generation || picked > load.Generation {
		return "removal", nil
	}
	if picked != load.Generation {
		return "stale", nil
	}
	retention := SessionFactLoad{}
	if err := loadSessionFactRetention(ctx, tx, &retention); err != nil {
		return "", err
	}
	if len(load.Retention) != len(retention.Retention) {
		return "retention", nil
	}
	for name, was := range load.Retention {
		if now, ok := retention.Retention[name]; !ok || !sameRetentionState(was, now) {
			return "retention", nil
		}
	}
	var copyGate bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name = 'session_fact_copies_v1')`).Scan(&copyGate); err != nil {
		return "", err
	}
	if copyGate != load.Input.CopyGate {
		return "copy_gate", nil
	}
	// A timeline publish belongs to the generation the loader observed. Check it
	// before the broader copy-group witness so a requeue is reported as the
	// timeline's own stale-source abort instead of masking it as copy_source.
	if storesSessionTimeline(load, timeline) {
		var current int64
		if err := tx.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&current); err != nil {
			return "", err
		}
		if current != load.Generation {
			return "timeline_source", nil
		}
	}
	if err := verifySessionFactCopyGroup(ctx, tx, load.CopyGroup, id); errors.Is(err, errSessionFactCopyGroupChanged) {
		return "copy_source", nil
	} else if err != nil {
		return "", err
	}
	// The immutable output is computed from visible source rows. Recheck the
	// exclusion gate and source identity under the source lock before publishing.
	var excluded bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM excluded_sessions WHERE session_id = $1)`, id).Scan(&excluded); err != nil {
		return "", err
	}
	if excluded {
		changedCopySessions, err := applySessionFactCopyMarkers(ctx, tx, load.CopyGroup)
		if err != nil {
			return "", err
		}
		if copyGate && len(changedCopySessions) > 0 {
			if err := refreshSessionFactCopyDerived(ctx, tx, changedCopySessions); err != nil {
				return "", err
			}
		}
		if err := deleteSessionFacts(ctx, tx, []string{id}); err != nil {
			return "", err
		}
		if copyGate && len(changedCopySessions) > 0 {
			if err := markSessionFactsDirty(ctx, tx, changedCopySessions, nil, dirtyPublishPeer); err != nil {
				return "", err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM session_fact_dirty WHERE session_id = $1 AND generation = $2`, id, load.Generation); err != nil {
			return "", err
		}
		return "excluded", tx.Commit(ctx)
	}
	var sourceAgent string
	if err := tx.QueryRow(ctx, sessionFactAgentSQL, id).Scan(&sourceAgent); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "source", nil
		}
		return "", err
	}
	if sourceAgent != load.SourceAgent {
		return "source", nil
	}
	var currentUsers, currentProfiles []string
	// A truncated fallback summarizes every visible agent; full computation
	// reads only the chosen agent's records. Both retain shared OTEL/snapshots.
	if err := tx.QueryRow(ctx, `SELECT
		ARRAY(SELECT user_id FROM (SELECT DISTINCT user_id FROM visible_session_records WHERE session_id = $1
			AND ($3 OR agent = $2 OR ($2 = 'claude' AND COALESCE(NULLIF(agent,''),'claude') = 'claude'))
			UNION SELECT DISTINCT user_id FROM visible_events WHERE session_id = $1 AND event_name = 'api_request'
			AND NOT EXISTS (SELECT 1 FROM excluded_sessions x WHERE x.session_id = $1)
			UNION SELECT DISTINCT ss.user_id FROM session_snapshots ss WHERE `+sessionFactSnapshotVisibleSQL+`) u ORDER BY user_id COLLATE "C"),
		ARRAY(SELECT profile_email FROM (SELECT DISTINCT profile_email FROM visible_session_records WHERE session_id = $1
			AND ($3 OR agent = $2 OR ($2 = 'claude' AND COALESCE(NULLIF(agent,''),'claude') = 'claude'))
			UNION SELECT DISTINCT profile_email FROM visible_events WHERE session_id = $1 AND event_name = 'api_request'
			AND NOT EXISTS (SELECT 1 FROM excluded_sessions x WHERE x.session_id = $1)
			UNION SELECT DISTINCT ss.profile_email FROM session_snapshots ss WHERE `+sessionFactSnapshotVisibleSQL+`) p ORDER BY profile_email COLLATE "C")`, id, load.Input.Agent, load.Truncated != nil).Scan(&currentUsers, &currentProfiles); err != nil {
		return "", err
	}
	if !sameStrings(currentUsers, output.Session.UserIDs) || !sameStrings(currentProfiles, output.Session.ProfileEmails) {
		return "identity", nil
	}
	if err := verifySessionCommitClaims(ctx, tx, id, load); errors.Is(err, ErrSessionCommitClaimsChanged) {
		return "commit_claims_source", nil
	} else if err != nil {
		return "", err
	}
	// The shared source witness and commit-claim set are checked before any fact is
	// deleted or written.
	source, err := readSessionTimelineSourceTx(ctx, tx, id, load, timeline)
	if errors.Is(err, ErrSessionTimelineChanged) {
		return "timeline_source", nil
	}
	if err != nil {
		return "", err
	}
	changedCopySessions, err := applySessionFactCopyMarkers(ctx, tx, load.CopyGroup)
	if err != nil {
		return "", err
	}
	sessionCopyChanged := containsSessionID(changedCopySessions, id)
	if copyGate && len(changedCopySessions) > 0 {
		if err := refreshSessionFactCopyDerived(ctx, tx, changedCopySessions); err != nil {
			return "", err
		}
		if sessionCopyChanged {
			if err := markSessionFactsDirty(ctx, tx, changedCopySessions, nil, dirtyPublishPeer); err != nil {
				return "", err
			}
			return "copy_requeued", tx.Commit(ctx)
		}
	}
	var existingHash []byte
	var oldCount int64
	err = tx.QueryRow(ctx, `SELECT output_hash, recompute_count FROM session_analysis_facts WHERE session_id = $1`, id).Scan(&existingHash, &oldCount)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	outcome := "written"
	if bytes.Equal(existingHash, output.Hash) && err == nil {
		_, err = tx.Exec(ctx, `UPDATE session_analysis_facts SET computed_at = now(), source_generation = $2,
			compute_ms = $3, recompute_count = recompute_count + 1 WHERE session_id = $1`, id, load.Generation, computeMS)
		outcome = "unchanged"
	} else {
		err = deleteSessionComputedFacts(ctx, tx, []string{id})
		if err == nil {
			err = insertSessionFactRows(ctx, tx, id, output, timeline, load.Generation, computeMS, oldCount+1)
		}
	}
	if err == nil {
		err = publishSessionTimelineTx(ctx, tx, id, load, output, timeline, source)
	}
	if errors.Is(err, ErrSessionTimelineChanged) {
		return "timeline_source", nil
	}
	if err != nil {
		return "", err
	}
	if copyGate && len(changedCopySessions) > 0 {
		if err := markSessionFactsDirty(ctx, tx, changedCopySessions, nil, dirtyPublishPeer); err != nil {
			return "", err
		}
	}
	deleted, err := tx.Exec(ctx, `DELETE FROM session_fact_dirty WHERE session_id = $1 AND generation = $2`, id, load.Generation)
	if err != nil {
		return "", err
	}
	if deleted.RowsAffected() == 0 {
		// The queue moved after the generation check. A stored timeline rolls back
		// with the facts; once the delete succeeds, its row lock holds a later
		// requeue until this commit.
		if source != nil {
			return "timeline_source", nil
		}
		if _, err := tx.Exec(ctx, `UPDATE session_fact_dirty SET first_dirty_at = now(), retry_at = now() + interval '30 seconds',
			attempts = 0, last_error = '', reasons = reasons & ~512
			WHERE session_id = $1 AND generation <> $2`, id, load.Generation); err != nil {
			return "", err
		}
		outcome = "requeued"
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return outcome, nil
}

var ErrSessionCommitClaimsChanged = errors.New("session commit claims changed")

func verifySessionCommitClaims(ctx context.Context, tx pgx.Tx, id string, load SessionFactLoad) error {
	complete, err := sessionCommitClaimsComplete(ctx, tx)
	if err != nil {
		return err
	}
	if complete != load.CommitClaimsComplete {
		return ErrSessionCommitClaimsChanged
	}
	if !complete {
		return nil
	}
	if _, err := prepareSessionClaimMutation(ctx, tx, []string{id}); err != nil {
		return err
	}
	_, current, err := sessionFactCommitClaimChecks(ctx, tx, id, load.Input.Snapshots)
	if err != nil {
		return err
	}
	return compareSessionCommitClaims(current, load.Input.CommitClaims)
}

func compareSessionCommitClaims(a, b []sessionfacts.CommitClaim) error {
	key := func(c sessionfacts.CommitClaim) string { return c.Repository + "\x00" + c.Hash }
	values := make(map[string]bool, len(a))
	for _, claim := range a {
		values[key(claim)] = claim.Conflicting
	}
	if len(values) != len(b) {
		return ErrSessionCommitClaimsChanged
	}
	for _, claim := range b {
		if conflict, ok := values[key(claim)]; !ok || conflict != claim.Conflicting {
			return ErrSessionCommitClaimsChanged
		}
	}
	return nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsSessionID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func refreshSessionFactCopyDerived(ctx context.Context, tx pgx.Tx, sessionIDs []string) error {
	if err := refreshPluginInvocationFacts(ctx, tx, sessionIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, taskSegmentTokensUpdateSQL, sessionIDs); err != nil {
		return fmt.Errorf("refresh copied task segment tokens: %w", err)
	}
	return nil
}

func insertSessionFactRows(ctx context.Context, tx pgx.Tx, id string, output sessionfacts.Output, extended *sessionFactTimeline, generation int64, computeMS int, count int64) error {
	s := output.Session
	commitsState, tasksState := "not_collected", "not_collected"
	var commitCount *int
	commitsTruncated := false
	if extended != nil {
		commitsState, tasksState = extended.Commits.State, extended.Tasks.State
		commitCount, commitsTruncated = extended.Commits.Total, extended.Commits.Truncated
		if commitsState == "" {
			commitsState = "undeterminable"
		}
		if tasksState == "" {
			tasksState = "undeterminable"
		}
	}
	accounts, err := json.Marshal(s.AccountKeys)
	if err != nil {
		return err
	}
	detail, err := json.Marshal(s.Detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO session_analysis_facts (
		session_id, agent, coverage, user_ids, profile_emails, login_emails, owner_user_id, owner_profile_email,
		login_email_inferred, account_keys, project_hash, repository_key, branch, model, transcript,
		title, title_source, title_state, first_activity_at, last_activity_at, first_record_at,
		active_seconds, turn_boundaries, turn_boundaries_reason, turns, requests, inputs, abandoned_turns,
		api_requests, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, cost_usd, cost_source,
		last_turn_key, last_turn_status, last_context_tokens, last_context_window, last_context_source,
		large_window_seen, open_questions, open_question_turn_key, has_plan_file, used_plan_mode,
		commits_state, commit_count, commits_truncated, tasks_state,
		detail, output_hash, compute_ms, recompute_count, source_generation, rules_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44,$45,$46,$47,$48,$49,$50,$51,$52,$53,$54,$55)`,
		id, s.Agent, s.Coverage, nonnil(s.UserIDs), nonnil(s.ProfileEmails), nonnil(s.LoginEmails), s.OwnerUserID, s.OwnerProfileEmail,
		s.LoginEmailInferred, accounts, s.ProjectHash, s.RepositoryKey, s.Branch, s.Model, s.Transcript,
		s.Title, s.TitleSource, s.TitleState, s.FirstActivityAt, s.LastActivityAt, s.FirstRecordAt,
		s.ActiveSeconds, s.TurnBoundaries, s.TurnBoundariesReason, s.Turns, s.Requests, s.Inputs, s.AbandonedTurns,
		s.APIRequests, s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheCreateTokens, s.CostUSD, s.CostSource,
		s.LastTurnKey, s.LastTurnStatus, s.LastContextTokens, s.LastContextWindow, s.LastContextSource,
		s.LargeWindowSeen, s.OpenQuestions, s.OpenQuestionTurnKey, s.HasPlanFile, s.UsedPlanMode,
		commitsState, commitCount, commitsTruncated, tasksState,
		detail, output.Hash, computeMS, count, generation, s.RulesVersion)
	if err != nil {
		return fmt.Errorf("insert session fact: %w", err)
	}
	for _, turn := range output.Turns {
		body, err := json.Marshal(turn.Detail)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO session_turn_facts (
			session_id,turn_key,agent,ordinal,request_no,branch,opener_copied,prompt_id,prompt_id_shared,opener_uuid,
			started_at,ended_at,input_kind,is_request,login_email,status,status_reason,permission_mode,denied,image_count,
			preview,preview_state,api_requests,input_tokens,output_tokens,cache_read_tokens,cache_create_tokens,cost_usd,
			cost_source,cost_reason,context_tokens,context_window,context_source,tool_calls,subagents,last_assistant_message_id,
			detail,rules_version)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38)`,
			id, turn.TurnKey, s.Agent, turn.Ordinal, turn.RequestNo, turn.Branch, turn.OpenerCopied, turn.PromptID, turn.PromptIDShared, turn.OpenerUUID,
			turn.StartedAt, turn.EndedAt, turn.InputKind, turn.IsRequest, turn.LoginEmail, turn.Status, turn.StatusReason, turn.PermissionMode, turn.Denied, turn.ImageCount,
			turn.Preview, turn.PreviewState, turn.APIRequests, turn.InputTokens, turn.OutputTokens, turn.CacheReadTokens, turn.CacheCreateTokens, turn.CostUSD,
			turn.CostSource, turn.CostReason, turn.ContextTokens, turn.ContextWindow, turn.ContextSource, turn.ToolCalls, turn.Subagents, turn.LastAssistantMessageID,
			body, s.RulesVersion)
		if err != nil {
			return fmt.Errorf("insert turn fact: %w", err)
		}
	}
	for _, seg := range output.Segments {
		if _, err := tx.Exec(ctx, `INSERT INTO session_activity_segments
			(session_id,seg_start,seg_end,owner_user_id,owner_profile_email,agent,records,human_inputs,rules_version)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, seg.SegStart, seg.SegEnd,
			s.OwnerUserID, s.OwnerProfileEmail, s.Agent, seg.Records, seg.HumanInputs, s.RulesVersion); err != nil {
			return fmt.Errorf("insert activity segment: %w", err)
		}
	}
	if extended != nil {
		for _, commit := range extended.Commits.Items {
			refs := nonnil(commit.Refs)
			var callSrecID *int64
			if commit.CallSrecID > 0 {
				value := commit.CallSrecID
				callSrecID = &value
			}
			if _, err := tx.Exec(ctx, `INSERT INTO session_commit_facts
				(session_id,ordinal,turn_key,call_id,call_srec_id,hash,short,subject,subject_state,committed_at,kind,source,repository,branch,refs,rules_version)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
				id, commit.Ordinal, commit.TurnKey, commit.Call, callSrecID, commit.Hash, commit.Short,
				commit.Subject, commit.SubjectState, commit.CommittedAt, commit.Kind, commit.Source,
				commit.Repository, commit.Branch, refs, s.RulesVersion); err != nil {
				return fmt.Errorf("insert session commit fact: %w", err)
			}
		}
		for _, task := range extended.Tasks.Items {
			touches, err := json.Marshal(task.Touches)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO session_task_facts
				(session_id,item_key,kind,display_id,title,title_state,summary,url,added,completed,edited,commit_linked,first_ts,touches,rules_version)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
				id, task.Key, task.Kind, task.ID, task.Title, task.TitleState, task.Summary, task.URL,
				task.Actions.Added, task.Actions.Completed, task.Actions.Edited, task.Actions.CommitLinked,
				task.FirstAt, touches, s.RulesVersion); err != nil {
				return fmt.Errorf("insert session task fact: %w", err)
			}
		}
	}
	return nil
}

func nonnil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
