package store

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"cctrace/internal/sessionrecord"
)

func TestPgStoreAutomaticSessionRecordCandidatesClaimLiveAndBackfill(t *testing.T) {
	s := acquireTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	owner := sessionrecord.Owner{UserID: "synthetic-auto-owner", ProfileEmail: "synthetic-auto@example.test", UseUserIDAccessControl: true}
	days := 30
	settings := sessionrecord.WorkerSettings{Enabled: true, BackfillEnabled: true, BackfillDays: &days}

	for _, lane := range []string{"live", "backfill"} {
		t.Run(lane, func(t *testing.T) {
			truncateTables(t, s)
			ctx := context.Background()
			sessionID := "synthetic-auto-candidate-" + lane
			lastActivity := now.Add(-45 * time.Minute)
			outputHash := sha256.Sum256([]byte("synthetic-auto-candidate-" + lane))
			if _, err := s.pool.Exec(ctx, `INSERT INTO session_analysis_facts
				(session_id,agent,coverage,user_ids,profile_emails,owner_user_id,owner_profile_email,project_hash,transcript,
				title_source,title_state,first_activity_at,last_activity_at,turn_boundaries,turns,requests,inputs,cost_source,detail,
				output_hash,source_generation,rules_version,computed_at)
				VALUES($1,'claude','full',ARRAY[$2],ARRAY[$3],$2,$3,'synthetic-auto-project',true,
				'first_request','ok',$4,$4,'observed',1,1,1,'none',
				'{"edited_files":{"state":"observed","items":[]},"plans":[]}'::jsonb,$5,1,1,$4)`,
				sessionID, owner.UserID, owner.ProfileEmail, lastActivity, outputHash[:]); err != nil {
				t.Fatalf("seed synthetic automatic candidate: %v", err)
			}

			job, claimed, err := s.ClaimNextSessionRecordJob(ctx, owner, lane, settings, now)
			if err != nil {
				t.Fatalf("claim automatic candidate: %v", err)
			}
			if !claimed || job.SessionID != sessionID || job.Lane != lane {
				t.Fatalf("claimed=%v job=%+v, want synthetic %s candidate", claimed, job, lane)
			}
			if job.Force || job.RangeRequest || job.RangeRequestedAt != nil {
				t.Fatalf("automatic candidate inherited request flags: %+v", job)
			}
		})
	}
}
