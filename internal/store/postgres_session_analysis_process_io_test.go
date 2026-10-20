package store

import (
	"context"
	"testing"
	"time"

	"cctrace/internal/sessionfacts"
)

func TestSessionAnalysisProcessIODistinguishesPerInstanceSourcesAndFreshness(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "analysis-process-io-owner"
	personalSource(t, s, id, "owner@example.com", "owner")
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, sessionfacts.Compute(load.Input), 1); err != nil || outcome != "written" {
		t.Fatalf("publish=%s, %v", outcome, err)
	}
	now := time.Now().UTC()
	_, err = s.pool.Exec(ctx, `INSERT INTO session_live_states
		(session_id,user_id,profile_email,pid,proc_start,status,waiting_kind,name,status_changed_at,heartbeat_at,identity_provenance,
		 io_epoch,io_as_of,io_source,io_mode,io_partial,io_failure_code,input_bytes,input_lines,output_bytes,output_lines,
		 stdout_bytes,stdout_lines,stderr_bytes,stderr_lines)
		VALUES
		($1,'owner','owner@example.com',11,'start-pty','busy','','PTY',now(),$2,'personal_token',
		 'epoch-pty',$2,'process_stream','pty',false,'',10,1,20,2,NULL,NULL,NULL,NULL),
		($1,'owner','owner@example.com',12,'start-pipes','busy','','pipes',now(),$2,'personal_token',
		 'epoch-pipes',$2-interval '1 minute','process_stream','pipes',true,'upload_failed',5,1,10,3,6,2,4,1)`, id, now)
	if err != nil {
		t.Fatal(err)
	}
	access := SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner", OwnerProfileEmail: "owner@example.com"}
	owner, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil {
		t.Fatal(err)
	}
	live := owner["live"].(map[string]interface{})
	instances := live["instances"].([]interface{})
	if len(instances) != 2 {
		t.Fatalf("owner instances = %d, want 2", len(instances))
	}
	byPID := make(map[int64]map[string]interface{}, len(instances))
	for _, raw := range instances {
		instance := raw.(map[string]interface{})
		process := instance["process"].(map[string]interface{})
		byPID[process["pid"].(int64)] = process
	}
	pty := byPID[11]
	ptyStdout, ptyStdoutOK := pty["stdout_bytes"].(*int64)
	ptyStderr, ptyStderrOK := pty["stderr_bytes"].(*int64)
	if pty == nil || pty["io_source"] != "process_stream" || pty["io_scope"] != "instance" || pty["io_mode"] != "pty" ||
		!ptyStdoutOK || ptyStdout != nil || !ptyStderrOK || ptyStderr != nil || pty["io_freshness"] != "fresh" {
		t.Fatalf("PTY process = %+v", pty)
	}
	pipes := byPID[12]
	stdoutBytes, stdoutOK := pipes["stdout_bytes"].(*int64)
	stderrBytes, stderrOK := pipes["stderr_bytes"].(*int64)
	if pipes == nil || pipes["io_source"] != "process_stream" || pipes["io_scope"] != "instance" || pipes["io_mode"] != "pipes" ||
		!stdoutOK || *stdoutBytes != 6 || !stderrOK || *stderrBytes != 4 || pipes["io_freshness"] != "partial" || pipes["io_failure_code"] != "upload_failed" {
		t.Fatalf("pipes process = %+v", pipes)
	}
	admin, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{OwnerUserID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := admin["live"]; ok {
		t.Fatal("nonowner received process I/O details")
	}
}
