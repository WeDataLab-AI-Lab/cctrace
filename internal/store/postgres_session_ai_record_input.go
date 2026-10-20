package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"cctrace/internal/sessionfacts"
	"cctrace/internal/sessionrecord"
)

type recordSlimBlock struct {
	Type      string   `json:"type"`
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	ToolUseID string   `json:"tool_use_id"`
	Failed    *bool    `json:"failed"`
	Path      string   `json:"path"`
	Command   string   `json:"command"`
	Question  string   `json:"question"`
	Options   []string `json:"options"`
	Answers   string   `json:"answers"`
}

type recordSlimRow struct {
	ID            int64
	TS            time.Time
	Agent         string
	RecordType    string
	UUID          string
	MessageID     string
	CommandName   string
	CommandInvoke string
	CWD           string
	InputText     *string
	QueuedPrompt  *string
	FinalText     *string
	QuestionText  *string
	AnswerText    *string
	PlanText      *string
	Blocks        []recordSlimBlock
	Withheld      []byte
	Dropped       []byte
}

const recordSlimRowsSQL = `SELECT sr.id,sr.ts,COALESCE(NULLIF(sr.agent,''),'claude'),sr.record_type,sr.uuid,COALESCE(sr.message_id,''),
	COALESCE(sr.command_name,''),COALESCE(sr.command_invoke,''),COALESCE(sr.raw->>'cwd',''),
	CASE WHEN sr.record_type='user' AND sr.uuid=ANY($2::text[]) THEN textrow.input_text END,
	CASE WHEN sr.record_type='user' AND sr.uuid=ANY($2::text[]) THEN left(sr.raw->'attachment'->>'prompt',16384) END,
	CASE WHEN sr.record_type='assistant' AND sr.message_id=ANY($3::text[]) THEN textrow.final_text END,
	CASE WHEN sr.id=ANY($4::bigint[]) THEN textrow.question_text END,
	CASE WHEN sr.id=ANY($5::bigint[]) THEN textrow.answer_text END,
	CASE WHEN sr.id=ANY($6::bigint[]) THEN textrow.plan_text END,
	COALESCE(blocks.items,'[]'::jsonb),COALESCE(sr.raw->'cctraceWithheld','[]'::jsonb),COALESCE(sr.raw->'cctraceDropped','[]'::jsonb)
FROM visible_session_records sr
LEFT JOIN LATERAL (
	SELECT
		CASE WHEN COALESCE(NULLIF(sr.agent,''),'claude')='codex' THEN
			CASE WHEN sr.record_type='user' THEN COALESCE(
				CASE WHEN jsonb_typeof(sr.raw->'payload'->'content')='string' THEN sr.raw->'payload'->>'content' END,
				(SELECT left(string_agg(e.b->>'text',E'\n' ORDER BY e.ord),4096) FROM jsonb_array_elements(CASE
					WHEN jsonb_typeof(sr.raw->'payload'->'content')='array' THEN sr.raw->'payload'->'content' ELSE '[]'::jsonb END)
					WITH ORDINALITY e(b,ord) WHERE e.b->>'type'='input_text')) END
		ELSE CASE WHEN sr.record_type='user' THEN COALESCE(
			CASE WHEN jsonb_typeof(sr.raw->'message'->'content')='string' THEN sr.raw->'message'->>'content' END,
			(SELECT left(string_agg(e.b->>'text',E'\n' ORDER BY e.ord),4096) FROM jsonb_array_elements(CASE
				WHEN jsonb_typeof(sr.raw->'message'->'content')='array' THEN sr.raw->'message'->'content' ELSE '[]'::jsonb END)
				WITH ORDINALITY e(b,ord) WHERE e.b->>'type' IN ('text','input_text'))) END END AS input_text,
		CASE WHEN COALESCE(NULLIF(sr.agent,''),'claude')='codex' THEN
			CASE WHEN sr.record_type='assistant' THEN (SELECT left(string_agg(e.b->>'text',E'\n' ORDER BY e.ord),3072)
				FROM jsonb_array_elements(CASE WHEN jsonb_typeof(sr.raw->'payload'->'content')='array'
					THEN sr.raw->'payload'->'content' ELSE '[]'::jsonb END) WITH ORDINALITY e(b,ord)
				WHERE e.b->>'type'='output_text') END
		ELSE CASE WHEN sr.record_type='assistant' THEN (SELECT left(string_agg(e.b->>'text',E'\n' ORDER BY e.ord),3072)
			FROM jsonb_array_elements(CASE WHEN jsonb_typeof(sr.raw->'message'->'content')='array'
				THEN sr.raw->'message'->'content' ELSE '[]'::jsonb END) WITH ORDINALITY e(b,ord)
			WHERE e.b->>'type'='text') END END AS final_text,
	(SELECT left(string_agg(COALESCE(e.b->'input'->>'question',e.b->>'text'),E'\n' ORDER BY e.ord),2048)
		FROM jsonb_array_elements(CASE WHEN jsonb_typeof(sr.raw->'message'->'content')='array'
			THEN sr.raw->'message'->'content' ELSE '[]'::jsonb END) WITH ORDINALITY e(b,ord)
		WHERE e.b->>'name'='AskUserQuestion') AS question_text,
	(SELECT left(string_agg(v,E'\n' ORDER BY k),2048) FROM jsonb_each_text(CASE
		WHEN jsonb_typeof(sr.raw->'toolUseResult'->'answers')='object' THEN sr.raw->'toolUseResult'->'answers' ELSE '{}'::jsonb END) a(k,v)) AS answer_text,
	COALESCE(left(sr.raw->'attachment'->>'planContent',4096),left(sr.raw->'attachment'->>'plan',4096),
		left(sr.raw->'toolUseResult'->>'plan',4096),
		(SELECT left(e.b->'input'->>'plan',4096) FROM jsonb_array_elements(CASE
			WHEN jsonb_typeof(sr.raw->'message'->'content')='array' THEN sr.raw->'message'->'content' ELSE '[]'::jsonb END) e(b)
			WHERE e.b->>'name'='ExitPlanMode' LIMIT 1)) AS plan_text
) textrow ON true
LEFT JOIN LATERAL (
	SELECT CASE WHEN COALESCE(NULLIF(sr.agent,''),'claude')='codex' AND sr.record_type='tool_call' THEN
		jsonb_build_array(jsonb_build_object('type','tool_use','id',NULLIF(sr.tool_call_id,''),
			'name',NULLIF(sr.tool_name,''),'command',left(COALESCE(sr.raw->'payload'->>'arguments',sr.raw->'payload'->>'input'),160)))
	ELSE COALESCE(jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
		'type',e.b->>'type','id',e.b->>'id','name',e.b->>'name','tool_use_id',e.b->>'tool_use_id',
		'failed',e.b->'is_error',
		'path',CASE WHEN e.b->>'name' IN ('Edit','Write','MultiEdit') THEN e.b->'input'->>'file_path'
			WHEN e.b->>'name'='NotebookEdit' THEN e.b->'input'->>'notebook_path' END,
		'command',CASE WHEN e.b->>'name'='Bash' THEN left(e.b->'input'->>'command',160) END,
		'question',CASE WHEN e.b->>'name'='AskUserQuestion' THEN left(e.b->'input'->>'question',2048) END,
		'options',CASE WHEN e.b->>'name'='AskUserQuestion' THEN jsonb_path_query_array(e.b->'input','$.questions[*].options[*].label') END,
		'plan',CASE WHEN e.b->>'name'='ExitPlanMode' THEN left(e.b->'input'->>'plan',4096) END)) ORDER BY e.ord), '[]'::jsonb) END AS items
	FROM jsonb_array_elements(CASE WHEN jsonb_typeof(sr.raw->'message'->'content')='array'
		THEN sr.raw->'message'->'content' ELSE '[]'::jsonb END) WITH ORDINALITY e(b,ord)
	WHERE e.b->>'type' IN ('tool_use','tool_result')
) blocks ON true
WHERE sr.session_id=$1 AND NOT sr.is_sidechain
	AND (sr.record_type IN ('assistant','user','tool_call') OR sr.uuid=ANY($2::text[])
		OR sr.message_id=ANY($3::text[]) OR sr.id=ANY($4::bigint[]) OR sr.id=ANY($5::bigint[]) OR sr.id=ANY($6::bigint[]))
	AND (($8::boolean AND NULLIF($7::text,'') IS NOT NULL AND sr.user_id=$7)
		OR (NOT $8::boolean AND NULLIF($9::text,'') IS NOT NULL AND lower(sr.profile_email)=lower($9)))
	AND NOT (EXISTS(SELECT 1 FROM schema_backfills WHERE name='session_fact_copies_v1')
		AND EXISTS(SELECT 1 FROM session_record_copies c WHERE c.srec_id=sr.id))
ORDER BY sr.ts,sr.id`

func (s *PgStore) readRecordSlimRows(ctx context.Context, tx pgx.Tx, id string, inputIDs, finalIDs []string, questionIDs, answerIDs, planIDs []int64, access SessionAnalysisAccess) ([]recordSlimRow, error) {
	identity := access.ProfileEmail
	useUserID := access.UserID != "" && access.ProfileEmail == ""
	if useUserID {
		identity = access.UserID
	}
	rows, err := tx.Query(ctx, recordSlimRowsSQL, id, inputIDs, finalIDs, questionIDs, answerIDs, planIDs, identity, useUserID, access.ProfileEmail)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]recordSlimRow, 0)
	for rows.Next() {
		var row recordSlimRow
		var blocks []byte
		if err := rows.Scan(&row.ID, &row.TS, &row.Agent, &row.RecordType, &row.UUID, &row.MessageID, &row.CommandName, &row.CommandInvoke, &row.CWD,
			&row.InputText, &row.QueuedPrompt, &row.FinalText, &row.QuestionText, &row.AnswerText, &row.PlanText, &blocks, &row.Withheld, &row.Dropped); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(blocks, &row.Blocks); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func applyRecordSlimRows(input *sessionrecord.Input, factTurns []sessionfacts.Turn, rows []recordSlimRow, commits []recordCommitInput, tasks []recordTaskInput, fact *sessionfacts.Session) {
	byUUID := map[string]recordSlimRow{}
	byMessage := map[string]recordSlimRow{}
	byID := map[int64]recordSlimRow{}
	for _, row := range rows {
		if row.UUID != "" {
			byUUID[row.UUID] = row
		}
		if row.MessageID != "" {
			byMessage[row.MessageID] = row
		}
		byID[row.ID] = row
	}
	turnIndex := map[string]int{}
	activeStarts := make([]time.Time, len(input.Turns))
	for i := range input.Turns {
		turnIndex[input.Turns[i].Key] = i
		activeStarts[i] = input.Turns[i].StartedAt
	}
	for _, turn := range factTurns {
		if turn.Branch != "active" {
			continue
		}
		i, ok := turnIndex[turn.TurnKey]
		if !ok {
			continue
		}
		for _, in := range turn.Detail.Inputs {
			row, found := byUUID[in.UUID]
			text := ""
			unavailable := !found || turn.PreviewState == "redacted" || rowMarked(row, "message")
			if found {
				if row.QueuedPrompt != nil && in.Kind == "queued" {
					text = *row.QueuedPrompt
				} else if row.InputText != nil {
					text = *row.InputText
				}
			}
			entry := sessionrecord.Text{Kind: in.Kind, Text: text, Unavailable: unavailable}
			if found {
				entry.CommandName = row.CommandName
				entry.CommandInvoke = row.CommandInvoke
				if strings.Contains(text, "<bash-input>") {
					entry.BashInput = true
					entry.ProgramName = commandKind(text)
				}
			}
			input.Turns[i].Inputs = append(input.Turns[i].Inputs, entry)
		}
		for _, q := range turn.Detail.Questions {
			questionRow, qok := byID[q.SrecID]
			answerRow, aok := byID[q.AnswerSrecID]
			question := sessionrecord.Question{Headers: q.Headers, Unavailable: !qok || rowMarked(questionRow, "toolUseResult.answers")}
			if qok && questionRow.QuestionText != nil {
				question.Text = *questionRow.QuestionText
			} else {
				question.Text = strings.Join(q.Headers, " / ")
			}
			if aok && answerRow.AnswerText != nil && !rowMarked(answerRow, "toolUseResult.answers") {
				question.Answer = *answerRow.AnswerText
			} else if q.AnswerSrecID > 0 {
				question.Unavailable = true
			}
			if qok {
				for _, block := range questionRow.Blocks {
					if block.Name == "AskUserQuestion" {
						for _, option := range block.Options {
							question.Options = append(question.Options, sessionrecord.Option{Name: option})
						}
					}
				}
			}
			input.Turns[i].Questions = append(input.Turns[i].Questions, question)
		}
		if turn.LastAssistantMessageID != "" {
			if final, found := byMessage[turn.LastAssistantMessageID]; found && final.FinalText != nil && !rowMarked(final, "message") {
				input.Turns[i].Final = *final.FinalText
			}
		}
	}
	for _, p := range fact.Detail.Plans {
		i, ok := turnIndex[p.TurnKey]
		if !ok {
			continue
		}
		row, found := byID[p.SrecID]
		if !found && p.AttachmentSrecID > 0 {
			row, found = byID[p.AttachmentSrecID]
		}
		plan := &sessionrecord.Plan{Decision: p.Decision, Unavailable: !found || rowMarked(row, "attachment.plan", "attachment.planContent", "toolUseResult.plan")}
		if found && row.PlanText != nil {
			plan.Text = *row.PlanText
		}
		input.Turns[i].Plan = plan
	}
	type toolAccum struct {
		calls       int
		failures    int
		observed    int
		targets     []string
		unavailable bool
	}
	toolByTurn := map[int]map[string]*toolAccum{}
	callOwner := map[string]struct {
		turn int
		name string
	}{}
	for _, row := range rows {
		i := activeTurnAt(input.Turns, row.TS)
		if i < 0 {
			continue
		}
		for _, block := range row.Blocks {
			if block.Type == "tool_use" {
				if toolByTurn[i] == nil {
					toolByTurn[i] = map[string]*toolAccum{}
				}
				acc := toolByTurn[i][block.Name]
				if acc == nil {
					acc = &toolAccum{}
					toolByTurn[i][block.Name] = acc
				}
				acc.calls++
				if block.Path != "" {
					target := safeRecordPath(row.CWD, block.Path)
					if target != "" {
						acc.targets = appendUniqueRecordString(acc.targets, target, 10)
					} else {
						acc.unavailable = true
					}
				}
				if block.Command != "" {
					target := commandKind(block.Command)
					if target != "" {
						acc.targets = appendUniqueRecordString(acc.targets, target, 10)
					}
				}
				if block.ToolUseID != "" {
					callOwner[block.ToolUseID] = struct {
						turn int
						name string
					}{i, block.Name}
				}
			} else if block.Type == "tool_result" && block.Failed != nil && block.ToolUseID != "" {
				owner, ok := callOwner[block.ToolUseID]
				if !ok {
					continue
				}
				acc := toolByTurn[owner.turn][owner.name]
				acc.observed++
				if *block.Failed {
					acc.failures++
				}
			}
		}
	}
	for i, byName := range toolByTurn {
		names := make([]string, 0, len(byName))
		for name := range byName {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			acc := byName[name]
			var fail *int
			if acc.observed == acc.calls {
				fail = &acc.failures
			}
			input.Turns[i].Tools = append(input.Turns[i].Tools, sessionrecord.Tool{Name: name, Calls: acc.calls, Failures: fail, Targets: acc.targets, TargetsUnavailable: acc.unavailable})
		}
	}
	for _, commit := range commits {
		if i, ok := turnIndex[commit.TurnKey]; ok && commit.Subject != "" {
			input.Turns[i].Commits = append(input.Turns[i].Commits, commit.Subject)
		}
	}
	for _, task := range tasks {
		if task.TitleState != "ok" && task.TitleState != "none" {
			continue
		}
		title := task.Title
		if title == "" {
			title = task.DisplayID
		}
		for _, touch := range task.Touches {
			if i, ok := turnIndex[touch.TurnKey]; ok {
				if task.Kind == "pr" {
					input.Turns[i].PRs = append(input.Turns[i].PRs, task.DisplayID)
				} else {
					input.Turns[i].Tasks = append(input.Turns[i].Tasks, sessionrecord.Task{ID: task.DisplayID, Title: title, Action: touch.Action})
				}
			}
		}
	}
}

func activeTurnAt(turns []sessionrecord.Turn, ts time.Time) int {
	best := -1
	for i, turn := range turns {
		if !turn.StartedAt.After(ts) && (best < 0 || turn.StartedAt.After(turns[best].StartedAt)) {
			best = i
		}
	}
	return best
}

func rowMarked(row recordSlimRow, fields ...string) bool {
	for _, blob := range [][]byte{row.Withheld, row.Dropped} {
		for _, field := range fields {
			if strings.Contains(string(blob), field) {
				return true
			}
		}
	}
	return false
}

func safeRecordPath(cwd, path string) string {
	path = strings.TrimSpace(path)
	if path == "" || strings.Contains(path, "[redacted by cctrace client]") {
		return ""
	}
	if filepath.IsAbs(path) {
		if cwd != "" {
			rel, err := filepath.Rel(cwd, path)
			if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				path = rel
			} else {
				path = filepath.Base(path)
			}
		} else {
			path = filepath.Base(path)
		}
	}
	path = filepath.ToSlash(path)
	if strings.HasPrefix(path, "/") || strings.Contains(path, "://") {
		return ""
	}
	return path
}

func commandKind(command string) string {
	fields := strings.Fields(strings.ReplaceAll(strings.ReplaceAll(command, "\n", " "), ";", " && "))
	for len(fields) > 0 && strings.Contains(fields[0], "=") {
		fields = fields[1:]
	}
	for len(fields) > 0 && (fields[0] == "sudo" || fields[0] == "time" || fields[0] == "nohup") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return ""
	}
	if fields[0] == "cd" {
		for i, field := range fields {
			if field == "&&" && i+1 < len(fields) {
				fields = fields[i+1:]
				break
			}
		}
	}
	if len(fields) == 0 {
		return ""
	}
	program := filepath.Base(fields[0])
	allowed := map[string]bool{"git": true, "gh": true, "go": true, "npm": true, "pnpm": true, "yarn": true, "make": true, "docker": true, "kubectl": true, "cargo": true, "uv": true, "bun": true}
	if !allowed[program] {
		return program
	}
	if len(fields) > 1 && fields[1] != "&&" {
		return program + " " + fields[1]
	}
	return program
}

func hashRecordSource(factsHash []byte, input sessionrecord.Input, rows []recordSlimRow, commits []recordCommitInput, tasks []recordTaskInput, redacted bool) []byte {
	input.Vocabulary = sessionrecord.Vocabulary{}
	input.Prior = nil
	data, _ := json.Marshal(struct {
		Facts    []byte
		Input    sessionrecord.Input
		Rows     []recordSlimRow
		Commits  []recordCommitInput
		Tasks    []recordTaskInput
		Redacted bool
	}{factsHash, input, rows, commits, tasks, redacted})
	sum := sha256.Sum256(data)
	return sum[:]
}

func hashRecordBasis(input sessionrecord.Input, commits []recordCommitInput, tasks []recordTaskInput) ([]byte, []byte) {
	data, _ := json.Marshal(struct {
		Turns   []sessionrecord.Turn
		Commits []recordCommitInput
		Tasks   []recordTaskInput
	}{input.Turns, commits, tasks})
	sum := sha256.Sum256(data)
	prefix := input
	if len(prefix.Turns) > 0 {
		prefix.Turns = prefix.Turns[:len(prefix.Turns)-1]
	}
	pdata, _ := json.Marshal(struct {
		Turns   []sessionrecord.Turn
		Commits []recordCommitInput
		Tasks   []recordTaskInput
	}{prefix.Turns, commits, tasks})
	psum := sha256.Sum256(pdata)
	return sum[:], psum[:]
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
func uniqueIDs(values []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(values))
	for _, v := range values {
		if v > 0 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func appendUniqueRecordString(values []string, value string, limit int) []string {
	for _, old := range values {
		if old == value {
			return values
		}
	}
	if len(values) < limit {
		return append(values, value)
	}
	return values
}

func sameRecordHash(a, b []byte) bool { return bytes.Equal(a, b) }

func sessionRecordPolicyCurrent(ctx context.Context, tx pgx.Tx, job sessionrecord.Job) error {
	var enabled bool
	var recordRuntime, weeklyRuntime string
	if err := tx.QueryRow(ctx, `SELECT record_enabled,record_runtime,runtime FROM ai_settings WHERE id=1`).
		Scan(&enabled, &recordRuntime, &weeklyRuntime); err != nil {
		if err == pgx.ErrNoRows {
			return sessionrecord.ErrRecordSendNotAllowed
		}
		return err
	}
	runtime := strings.SplitN(job.RuntimeKey, ":", 2)[0]
	if !enabled || (recordRuntime != "" && recordRuntime != runtime) || (recordRuntime == "" && weeklyRuntime != "" && weeklyRuntime != runtime) {
		return sessionrecord.ErrRecordSendNotAllowed
	}
	var recordModel, recordEffort, weeklyModel string
	err := tx.QueryRow(ctx, `SELECT record_model,record_reasoning_effort,model FROM ai_runtime_settings WHERE runtime=$1`, runtime).
		Scan(&recordModel, &recordEffort, &weeklyModel)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if (recordModel != "" && recordModel != job.Model) || (recordModel == "" && weeklyModel != "" && weeklyModel != job.Model) ||
		(recordEffort != "" && recordEffort != job.ReasoningEffort) {
		return sessionrecord.ErrRecordSendNotAllowed
	}
	return nil
}

func (s *PgStore) LoadSessionRecordSnapshot(parent context.Context, job sessionrecord.Job) (sessionrecord.Snapshot, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return sessionrecord.Snapshot{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	snapshot, err := s.readSessionRecordSnapshotTx(ctx, tx, job)
	if err != nil {
		return sessionrecord.Snapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sessionrecord.Snapshot{}, err
	}
	return snapshot, nil
}

func (s *PgStore) readSessionRecordSnapshotTx(ctx context.Context, tx pgx.Tx, job sessionrecord.Job) (sessionrecord.Snapshot, error) {
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='10s'`); err != nil {
		return sessionrecord.Snapshot{}, err
	}
	var activeClaim bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_ai_record_jobs WHERE session_id=$1 AND claim=$2)`, job.SessionID, job.Claim).Scan(&activeClaim); err != nil {
		return sessionrecord.Snapshot{}, err
	}
	if !activeClaim {
		return sessionrecord.Snapshot{}, sessionrecord.ErrRecordSendNotAllowed
	}
	if err := sessionRecordPolicyCurrent(ctx, tx, job); err != nil {
		return sessionrecord.Snapshot{}, err
	}
	userID, profileEmail := job.Owner.UserID, job.Owner.ProfileEmail
	access := SessionAnalysisAccess{User: true, DashboardUserID: job.Owner.DashboardUserID}
	if job.Owner.UseUserIDAccessControl {
		access.UserID, access.OwnerUserID = userID, userID
	} else {
		access.ProfileEmail, access.OwnerProfileEmail = profileEmail, profileEmail
	}
	foreign, err := analysisForeignIdentity(ctx, tx, job.SessionID, access)
	if err != nil {
		return sessionrecord.Snapshot{}, err
	}
	if foreign {
		return sessionrecord.Snapshot{}, sessionrecord.ErrRecordSendNotAllowed
	}
	var consented bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_ai_record_consents c
		JOIN dashboard_users u ON u.id=c.dashboard_user_id
		WHERE u.id=$1 AND u.is_active AND c.runtime_key=$2 AND c.disclosure_version=$3
		AND (($4::boolean AND u.cctrace_user_id=$5) OR (NOT $4::boolean AND lower(u.email)=lower($6))))`,
		job.Owner.DashboardUserID, job.RuntimeKey, job.DisclosureVersion, job.Owner.UseUserIDAccessControl,
		job.Owner.UserID, job.Owner.ProfileEmail).Scan(&consented); err != nil {
		return sessionrecord.Snapshot{}, err
	}
	if !consented {
		return sessionrecord.Snapshot{}, sessionrecord.ErrRecordSendNotAllowed
	}
	f, err := analysisFact(ctx, tx, job.SessionID, access)
	if err != nil {
		return sessionrecord.Snapshot{}, err
	}
	var factsHash []byte
	if err := tx.QueryRow(ctx, `SELECT output_hash FROM session_analysis_facts WHERE session_id=$1`, job.SessionID).Scan(&factsHash); err != nil {
		return sessionrecord.Snapshot{}, err
	}
	if !sameRecordHash(factsHash, job.OutputHash) {
		return sessionrecord.Snapshot{}, sessionrecord.ErrRecordSendNotAllowed
	}
	var private bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM privacy_settings p
		WHERE (p.user_id=$1 OR lower(p.profile_email)=lower($2)) AND
		(p.scope_type='user' OR (p.scope_type='session' AND p.scope_value=$3)
			OR (p.scope_type='project' AND p.scope_value=$4)))`,
		job.Owner.UserID, job.Owner.ProfileEmail, job.SessionID, f.ProjectHash).Scan(&private); err != nil {
		return sessionrecord.Snapshot{}, err
	}
	if private {
		return sessionrecord.Snapshot{}, sessionrecord.ErrRecordSendNotAllowed
	}
	retentionState, err := sessionRecordRetentionState(ctx, tx)
	if err != nil {
		return sessionrecord.Snapshot{}, err
	}
	turns, err := readSessionRecordTurns(ctx, tx, job.SessionID)
	if err != nil {
		return sessionrecord.Snapshot{}, err
	}
	var commits []recordCommitInput
	var tasks []recordTaskInput
	if err := readSessionRecordP4Facts(ctx, tx, job.SessionID, &commits, &tasks); err != nil {
		return sessionrecord.Snapshot{}, err
	}
	ownerMismatch, err := sessionRecordSourceMismatch(ctx, tx, job.SessionID, access)
	if err != nil {
		return sessionrecord.Snapshot{}, err
	}
	if ownerMismatch || f.OwnerUserID == nil && f.OwnerProfileEmail == nil || f.Coverage != "full" || f.TurnBoundaries != "observed" || f.Requests == nil || *f.Requests <= 0 {
		return sessionrecord.Snapshot{}, sessionrecord.ErrRecordSendNotAllowed
	}
	accountKeys := f.AccountKeys
	if accountKeys == nil {
		accountKeys = []sessionfacts.AccountKey{}
	}
	accountJSON, err := json.Marshal(accountKeys)
	if err != nil {
		return sessionrecord.Snapshot{}, err
	}
	input := sessionrecord.Input{Vocabulary: sessionrecord.Vocabulary{ComponentsClosed: f.RepositoryKey == "", ScreensClosed: f.RepositoryKey == ""}}
	for i := range turns {
		if turns[i].Branch != "active" {
			continue
		}
		input.Turns = append(input.Turns, sessionrecord.Turn{Name: fmt.Sprintf("t%d", len(input.Turns)+1),
			Key: turns[i].TurnKey, Ordinal: turns[i].Ordinal, Request: turns[i].IsRequest, StartedAt: turns[i].StartedAt,
			Status: turns[i].Status, InputKind: turns[i].InputKind, PermissionMode: turns[i].PermissionMode,
			Denied: turns[i].Denied, PreviewState: turns[i].PreviewState})
	}
	if len(input.Turns) == 0 {
		return sessionrecord.Snapshot{}, sessionrecord.ErrRecordSendNotAllowed
	}
	inputIDs, finalIDs, questionIDs, answerIDs, planIDs := recordSourceIdentifiers(f, turns)
	redacted := allRequestInputsRedacted(input.Turns)
	var slim []recordSlimRow
	if !redacted {
		slim, err = s.readRecordSlimRows(ctx, tx, job.SessionID, inputIDs, finalIDs, questionIDs, answerIDs, planIDs, access)
		if err != nil {
			return sessionrecord.Snapshot{}, err
		}
		applyRecordSlimRows(&input, turns, slim, commits, tasks, f)
	}
	input.Vocabulary, err = readRecordVocabulary(ctx, tx, access, f.RepositoryKey)
	if err != nil {
		return sessionrecord.Snapshot{}, err
	}
	built, err := sessionrecord.BuildInput(input)
	if err != nil && !redacted {
		return sessionrecord.Snapshot{}, err
	}
	inputHash, err := sessionrecord.InputFingerprint(input)
	if err != nil {
		return sessionrecord.Snapshot{}, err
	}
	sourceHash := hashRecordSource(factsHash, input, slim, commits, tasks, redacted)
	basisHash, prefixHash := hashRecordBasis(input, commits, tasks)
	var existing *sessionrecord.PersistedRecord
	if !redacted {
		existing, err = readExistingSessionAIRecord(ctx, tx, job.SessionID, factsHash, access, f.ProjectHash)
		if err != nil {
			return sessionrecord.Snapshot{}, err
		}
	}
	snapshot := sessionrecord.Snapshot{Input: input, Agent: f.Agent, RepositoryKey: f.RepositoryKey, AccountKeys: accountJSON,
		FactsHash: factsHash, FactsRulesVersion: f.RulesVersion, BasisHash: basisHash, PrefixHash: prefixHash,
		InputHash: inputHash, SourceInputHash: sourceHash, CoversUntil: f.LastActivityAt, CoversTurnKey: f.LastTurnKey,
		CoveredRequests: valueInt(f.Requests), SourceFirstAt: f.FirstRecordAt, RetentionState: retentionState, AllRequestsRedacted: redacted,
		Existing: existing}
	if f.OwnerUserID != nil {
		ownerID := *f.OwnerUserID
		snapshot.OwnerUserID = &ownerID
	}
	if f.OwnerProfileEmail != nil {
		ownerEmail := *f.OwnerProfileEmail
		snapshot.OwnerProfileEmail = &ownerEmail
	}
	if !redacted && err == nil {
		snapshot.Gaps = built.Gaps
	}
	return snapshot, nil
}

func sessionRecordRetentionState(ctx context.Context, tx pgx.Tx) ([]byte, error) {
	var state []byte
	err := tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_array(table_name,last_run_finished,oldest_source_ts) ORDER BY table_name),'[]'::jsonb)
		FROM session_overview_retention_state WHERE table_name IN ('session_records','otel_events')`).Scan(&state)
	return state, err
}

func readSessionRecordTurns(ctx context.Context, tx pgx.Tx, id string) ([]sessionfacts.Turn, error) {
	rows, err := tx.Query(ctx, `SELECT turn_key,ordinal,request_no,branch,started_at,ended_at,input_kind,is_request,status,
		permission_mode,denied,image_count,preview_state,last_assistant_message_id,detail
		FROM session_turn_facts WHERE session_id=$1 ORDER BY ordinal`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	turns := make([]sessionfacts.Turn, 0)
	for rows.Next() {
		var turn sessionfacts.Turn
		var branch string
		var detail []byte
		if err := rows.Scan(&turn.TurnKey, &turn.Ordinal, &turn.RequestNo, &branch, &turn.StartedAt, &turn.EndedAt,
			&turn.InputKind, &turn.IsRequest, &turn.Status, &turn.PermissionMode, &turn.Denied, &turn.ImageCount,
			&turn.PreviewState, &turn.LastAssistantMessageID, &detail); err != nil {
			return nil, err
		}
		turn.Branch = branch
		if err := json.Unmarshal(detail, &turn.Detail); err != nil {
			return nil, err
		}
		turns = append(turns, turn)
	}
	return turns, rows.Err()
}

type recordCommitInput struct {
	TurnKey     string    `json:"turn_key"`
	Short       string    `json:"short"`
	Subject     string    `json:"subject"`
	CommittedAt time.Time `json:"committed_at"`
}

type recordTaskTouch struct {
	TurnKey string    `json:"turn_key"`
	Action  string    `json:"a"`
	At      time.Time `json:"ts"`
}

type recordTaskInput struct {
	ItemKey    string            `json:"item_key"`
	Kind       string            `json:"kind"`
	DisplayID  string            `json:"display_id"`
	Title      string            `json:"title"`
	TitleState string            `json:"title_state"`
	Touches    []recordTaskTouch `json:"touches"`
}

func readSessionRecordP4Facts(ctx context.Context, tx pgx.Tx, id string, commits *[]recordCommitInput, tasks *[]recordTaskInput) error {
	var hasCommits, hasTasks bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('session_commit_facts') IS NOT NULL,to_regclass('session_task_facts') IS NOT NULL`).Scan(&hasCommits, &hasTasks); err != nil {
		return err
	}
	if hasCommits {
		rows, err := tx.Query(ctx, `SELECT turn_key,short,subject,committed_at FROM session_commit_facts
			WHERE session_id=$1 ORDER BY committed_at,ordinal`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c recordCommitInput
			if err := rows.Scan(&c.TurnKey, &c.Short, &c.Subject, &c.CommittedAt); err != nil {
				rows.Close()
				return err
			}
			*commits = append(*commits, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if hasTasks {
		rows, err := tx.Query(ctx, `SELECT item_key,kind,display_id,title,title_state,touches FROM session_task_facts WHERE session_id=$1 ORDER BY first_ts,item_key`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var task recordTaskInput
			var touches []byte
			if err := rows.Scan(&task.ItemKey, &task.Kind, &task.DisplayID, &task.Title, &task.TitleState, &touches); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(touches, &task.Touches); err != nil {
				rows.Close()
				return err
			}
			*tasks = append(*tasks, task)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

func recordSourceIdentifiers(f *sessionfacts.Session, turns []sessionfacts.Turn) ([]string, []string, []int64, []int64, []int64) {
	inputs := []string{}
	finals := []string{}
	questions := []int64{}
	answers := []int64{}
	plans := []int64{}
	for _, turn := range turns {
		if turn.Branch == "active" {
			for _, in := range turn.Detail.Inputs {
				inputs = append(inputs, in.UUID)
			}
			if turn.LastAssistantMessageID != "" {
				finals = append(finals, turn.LastAssistantMessageID)
			}
			for _, q := range turn.Detail.Questions {
				questions = append(questions, q.SrecID)
				answers = append(answers, q.AnswerSrecID)
			}
		}
	}
	for _, p := range f.Detail.Plans {
		plans = append(plans, p.SrecID, p.ResultSrecID, p.AttachmentSrecID)
	}
	return uniqueStrings(inputs), uniqueStrings(finals), uniqueIDs(questions), uniqueIDs(answers), uniqueIDs(plans)
}

func allRequestInputsRedacted(turns []sessionrecord.Turn) bool {
	requested := 0
	for _, turn := range turns {
		if turn.Request {
			requested++
			if turn.PreviewState != "redacted" {
				return false
			}
		}
	}
	return requested > 0
}

func valueInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func sessionRecordSourceMismatch(ctx context.Context, tx pgx.Tx, id string, access SessionAnalysisAccess) (bool, error) {
	var mismatch bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM visible_session_records sr WHERE sr.session_id=$1 AND
		(($2::boolean AND sr.user_id IS DISTINCT FROM $3) OR (NOT $2::boolean AND lower(sr.profile_email)<>lower($4))))
		OR EXISTS(SELECT 1 FROM visible_events e WHERE e.session_id=$1 AND
		(($2::boolean AND e.user_id IS DISTINCT FROM $3) OR (NOT $2::boolean AND lower(e.profile_email)<>lower($4))))
		OR EXISTS(SELECT 1 FROM session_snapshots n WHERE n.session_id=$1 AND
		(($2::boolean AND n.user_id IS DISTINCT FROM $3) OR (NOT $2::boolean AND lower(n.profile_email)<>lower($4))))`,
		id, access.OwnerUserID != "", access.OwnerUserID, access.OwnerProfileEmail).Scan(&mismatch)
	return mismatch, err
}

func readRecordVocabulary(ctx context.Context, tx pgx.Tx, access SessionAnalysisAccess, repositoryKey string) (sessionrecord.Vocabulary, error) {
	out := sessionrecord.Vocabulary{Components: []sessionrecord.VocabularyTerm{}, Screens: []sessionrecord.VocabularyTerm{}}
	if repositoryKey == "" {
		out.ComponentsClosed, out.ScreensClosed = true, true
		return out, nil
	}
	var userID, email string
	if access.UserID != "" {
		userID = access.UserID
	} else if access.OwnerUserID != "" {
		userID = access.OwnerUserID
	}
	if access.ProfileEmail != "" {
		email = access.ProfileEmail
	} else if access.OwnerProfileEmail != "" {
		email = access.OwnerProfileEmail
	}
	for _, axis := range []string{"component", "screen"} {
		rows, err := tx.Query(ctx, `SELECT term FROM session_ai_record_vocab_pins
			WHERE repository_key=$1 AND axis=$2 ORDER BY position,term LIMIT 24`, repositoryKey, axis)
		if err != nil {
			return out, err
		}
		pinned := make([]sessionrecord.VocabularyTerm, 0)
		for rows.Next() {
			var term string
			if err := rows.Scan(&term); err != nil {
				rows.Close()
				return out, err
			}
			pinned = append(pinned, sessionrecord.VocabularyTerm{Term: term})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return out, err
		}
		rows.Close()
		closed := len(pinned) > 0
		terms := pinned
		if !closed {
			column := "components"
			if axis == "screen" {
				column = "screens"
			}
			query := `SELECT term,count(*) FROM session_ai_records r JOIN session_analysis_facts f USING(session_id)
				CROSS JOIN LATERAL unnest(r.` + column + `) AS term
				WHERE r.outcome='ok' AND r.repository_key=$3
				AND (($1::boolean AND NULLIF($2::text,'') IS NOT NULL AND r.owner_user_id=$2)
					OR (NOT $1::boolean AND NULLIF($4::text,'') IS NOT NULL AND lower(r.owner_profile_email)=lower($4)))
				AND ` + recordVisibilityPredicate("r", "f") + `
				GROUP BY term ORDER BY count(*) DESC,term LIMIT 24`
			vrows, err := tx.Query(ctx, query, userID != "", userID, repositoryKey, email)
			if err != nil {
				return out, err
			}
			terms = make([]sessionrecord.VocabularyTerm, 0, 24)
			for vrows.Next() {
				var term string
				var count int
				if err := vrows.Scan(&term, &count); err != nil {
					vrows.Close()
					return out, err
				}
				terms = append(terms, sessionrecord.VocabularyTerm{Term: term, Sessions: count})
			}
			if err := vrows.Err(); err != nil {
				vrows.Close()
				return out, err
			}
			vrows.Close()
		}
		if axis == "component" {
			out.Components, out.ComponentsClosed = terms, closed
		} else {
			out.Screens, out.ScreensClosed = terms, closed
		}
	}
	return out, nil
}

func recordVisibilityPredicate(recordAlias, factsAlias string) string {
	return fmt.Sprintf(`%s.prompt_version >= %d
		AND NOT EXISTS(SELECT 1 FROM excluded_sessions x WHERE x.session_id=%s.session_id)
		AND NOT EXISTS(SELECT 1 FROM privacy_settings p WHERE (p.user_id=%s.owner_user_id OR lower(p.profile_email)=lower(%s.owner_profile_email))
			AND (p.scope_type='user' OR (p.scope_type='session' AND p.scope_value=%s.session_id)
				OR (p.scope_type='project' AND p.scope_value=%s.project_hash)))
		AND NOT EXISTS(SELECT 1 FROM jsonb_to_recordset(%s.account_keys) AS k(login_email text,billing_provider text,account_id text)
			WHERE NOT (%s))`, recordAlias, sessionrecord.MinServablePromptVersion, recordAlias, factsAlias, factsAlias,
		recordAlias, factsAlias, recordAlias, excludedAccountPredicateSQL("k"))
}

func readExistingSessionAIRecord(ctx context.Context, tx pgx.Tx, id string, factsHash []byte, access SessionAnalysisAccess, _ string) (*sessionrecord.PersistedRecord, error) {
	userID, email := "", ""
	if access.UserID != "" {
		userID = access.UserID
	} else if access.OwnerUserID != "" {
		userID = access.OwnerUserID
	}
	if access.ProfileEmail != "" {
		email = access.ProfileEmail
	} else if access.OwnerProfileEmail != "" {
		email = access.OwnerProfileEmail
	}
	query := `SELECT r.outcome,r.reason,r.agent,r.owner_user_id,r.owner_profile_email,r.repository_key,r.account_keys,
		r.prompt_version,r.runtime,r.model,r.facts_hash,r.facts_rules_version,r.basis_hash,r.prefix_hash,r.input_hash,r.source_input_hash,
		r.covers_until,r.covers_turn_key,r.covered_requests,r.source_first_at,r.generated_at,r.generations,r.folds,r.language,r.headline,r.content,
		r.kinds,r.components,r.screens,r.work_total,r.work_done,r.work_open,r.work_unsettled,r.uncovered_requests,r.search_text,
		r.calls,r.input_bytes,r.tokens_reported,r.input_tokens,r.cached_input_tokens,r.output_tokens,r.duration_ms
		FROM session_ai_records r JOIN session_analysis_facts f USING(session_id)
		WHERE r.session_id=$1 AND f.output_hash=$2
		AND (($3::boolean AND NULLIF($4::text,'') IS NOT NULL AND r.owner_user_id=$4)
			OR (NOT $3::boolean AND NULLIF($5::text,'') IS NOT NULL AND lower(r.owner_profile_email)=lower($5)))
		AND ` + recordVisibilityPredicate("r", "f")
	var outcome, reason, agent, repository, runtime, model, language, turnKey, searchTextValue string
	var ownerUID, ownerEmail, headline *string
	var accountKeys, storedContent []byte
	var promptVersion, rulesVersion, generations, folds, covered, workTotal, workDone, workOpen, workUnsettled, uncovered, calls, inputBytes, duration int
	var storedFactsHash, basis, prefix, inputHash, sourceHash []byte
	var coversUntil, sourceFirst *time.Time
	var kinds, components, screens []string
	var reported bool
	var inputTokens, cachedTokens, outputTokens pgtype.Int8
	record := &sessionrecord.PersistedRecord{}
	err := tx.QueryRow(ctx, query, id, factsHash, userID != "", userID, email).
		Scan(&outcome, &reason, &agent, &ownerUID, &ownerEmail, &repository, &accountKeys,
			&promptVersion, &runtime, &model, &storedFactsHash, &rulesVersion, &basis, &prefix, &inputHash, &sourceHash,
			&coversUntil, &turnKey, &covered, &sourceFirst, &record.GeneratedAt, &generations, &folds, &language, &headline, &storedContent,
			&kinds, &components, &screens, &workTotal, &workDone, &workOpen, &workUnsettled, &uncovered, &searchTextValue,
			&calls, &inputBytes, &reported, &inputTokens, &cachedTokens, &outputTokens, &duration)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record.SessionID, record.Outcome, record.Reason, record.Agent = id, outcome, reason, agent
	record.OwnerUserID, record.OwnerProfileEmail, record.RepositoryKey, record.AccountKeys = ownerUID, ownerEmail, repository, accountKeys
	record.PromptVersion, record.Runtime, record.Model = promptVersion, runtime, model
	record.FactsHash, record.FactsRulesVersion, record.BasisHash, record.PrefixHash = storedFactsHash, rulesVersion, basis, prefix
	record.InputHash, record.SourceInputHash, record.CoversUntil, record.CoversTurnKey = inputHash, sourceHash, coversUntil, turnKey
	record.CoveredRequests, record.SourceFirstAt, record.Generations, record.Folds = covered, sourceFirst, generations, folds
	record.Language, record.Kinds, record.Components, record.Screens = language, kinds, components, screens
	record.WorkTotal, record.WorkDone, record.WorkOpen, record.WorkUnsettled, record.UncoveredRequests = workTotal, workDone, workOpen, workUnsettled, uncovered
	record.SearchText, record.Calls, record.InputBytes, record.TokensReported, record.DurationMS = searchTextValue, calls, inputBytes, reported, duration
	if headline != nil {
		record.Headline = *headline
	}
	if inputTokens.Valid {
		value := inputTokens.Int64
		record.InputTokens = &value
	}
	if cachedTokens.Valid {
		value := cachedTokens.Int64
		record.CachedInputTokens = &value
	}
	if outputTokens.Valid {
		value := outputTokens.Int64
		record.OutputTokens = &value
	}
	if len(storedContent) > 0 {
		if err := json.Unmarshal(storedContent, &record.Content); err != nil {
			return nil, err
		}
	}
	return record, nil
}
