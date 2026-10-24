package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The weekly page's AI report is the one answer in the demo the server does not
// write. The public CI that records the demo has no AI runtime, so the server
// answers that AI reports are off. The recorder keeps that answer's facts -- the
// week and its segment count -- and writes the rest: the runtime state, the run
// and a report built from the week's own segments with fixed wording.

const (
	reportRuntime = "codex-app-server"
	reportModel   = "gpt-6.1-sol"
)

// reportSegment is one task segment of the viewer's week, as the report's tools
// see it.
type reportSegment struct {
	ID                  int64
	SessionID           string
	StartTs             time.Time
	ProjectName, Agent  string
	TaskType            string // the segment's most frequent turn task type
	Typed, Tools, Fails int64
}

// weekSegments reads the viewer's segments of one week the way the server's
// report tools do (aiSegmentWhere in internal/store).
func weekSegments(ctx context.Context, db *pgx.Conn, p *persona, since, until time.Time) ([]reportSegment, error) {
	rows, err := db.Query(ctx, `SELECT f.boundary_record_id, f.session_id, f.start_ts,
		COALESCE((SELECT MAX(pr.project_name) FROM projects pr WHERE pr.agent=f.agent AND pr.project_hash=f.project_hash),''),
		f.agent,
		COALESCE((SELECT sr.task_type FROM session_records sr
			WHERE sr.session_id=f.session_id AND sr.ts >= f.start_ts AND (f.end_ts IS NULL OR sr.ts <= f.end_ts)
			  AND sr.task_type NOT IN ('', 'non-task', 'unknown')
			GROUP BY sr.task_type ORDER BY count(*) DESC, sr.task_type LIMIT 1), ''),
		f.typed_turn_count, f.tool_call_count, f.tool_fail_count
		FROM task_segment_facts f
		WHERE f.start_ts >= $1 AND f.start_ts < $2 AND (f.user_id=$3 OR f.profile_email=$4)
		  AND EXISTS (SELECT 1 FROM visible_session_records vsr WHERE vsr.id=f.boundary_record_id)
		ORDER BY f.start_ts`, since, until, p.UserID, p.Email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []reportSegment
	for rows.Next() {
		var s reportSegment
		if err := rows.Scan(&s.ID, &s.SessionID, &s.StartTs, &s.ProjectName, &s.Agent, &s.TaskType, &s.Typed, &s.Tools, &s.Fails); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

var taskTypeNames = map[string]string{
	"implementation": "구현", "debugging": "디버깅", "testing": "테스트", "review": "리뷰",
	"documentation": "문서", "planning": "계획", "question": "질문", "operations": "운영",
	"drafting": "초안", "status": "현황 정리",
}

func taskTypeName(a string) string {
	if n, ok := taskTypeNames[a]; ok {
		return n
	}
	return "작업"
}

type reportItem struct {
	seg           reportSegment
	title, reason string
}

// pickItems chooses the segments worth a second look: the one with the most
// tool failures, the longest back-and-forth, work that leaves no code behind, and
// one done in Codex.
func pickItems(segs []reportSegment) []reportItem {
	var items []reportItem
	add := func(s reportSegment, title, reason string) {
		for _, it := range items {
			if it.seg.ID == s.ID {
				return
			}
		}
		items = append(items, reportItem{s, title, reason})
	}
	most := func(v func(reportSegment) int64) reportSegment {
		best := segs[0]
		for _, s := range segs[1:] {
			if v(s) > v(best) {
				best = s
			}
		}
		return best
	}
	if s := most(func(s reportSegment) int64 { return s.Fails }); s.Fails > 0 {
		add(s, fmt.Sprintf("%s %s 구간의 도구 실패 %d회", s.ProjectName, taskTypeName(s.TaskType), s.Fails),
			fmt.Sprintf("도구 호출 %d회 동안 실패한 명령을 고치고 다시 돌리는 일이 이어졌다. 같은 실패가 다음 주에도 나오는지 볼 만하다.", s.Tools))
	}
	if s := most(func(s reportSegment) int64 { return s.Typed }); s.Typed > 1 {
		add(s, fmt.Sprintf("%s %s, 요청 %d번", s.ProjectName, taskTypeName(s.TaskType), s.Typed),
			"요청을 여러 번 고쳐 다시 보냈다. 처음 요청에 완료 기준을 함께 적으면 왕복이 줄 수 있다.")
	}
	for _, s := range segs {
		if s.TaskType == "documentation" || s.TaskType == "planning" || s.TaskType == "review" {
			add(s, fmt.Sprintf("%s %s", s.ProjectName, taskTypeName(s.TaskType)),
				"코드가 남지 않는 작업이라 다른 지표에는 잘 드러나지 않는다. 결과가 팀에 공유됐는지 확인할 만하다.")
			break
		}
	}
	for _, s := range segs {
		if s.Agent == "codex" {
			add(s, fmt.Sprintf("Codex로 진행한 %s %s", s.ProjectName, taskTypeName(s.TaskType)),
				fmt.Sprintf("도구 호출 %d회로 끝났다. 같은 주의 Claude Code 작업과 견줘 어느 쪽이 이 일에 맞았는지 볼 수 있다.", s.Tools))
			break
		}
	}
	if len(items) == 0 {
		s := segs[0]
		add(s, fmt.Sprintf("%s %s", s.ProjectName, taskTypeName(s.TaskType)), "이번 주의 대표 구간이다.")
	}
	return items
}

// top returns the most frequent key and its count, the name breaking ties.
func top(counts map[string]int) (string, int) {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) == 0 {
		return "", 0
	}
	return keys[0], counts[keys[0]]
}

func reportSummary(segs []reportSegment, items []reportItem) string {
	projects, taskTypes := map[string]int{}, map[string]int{}
	var fails int64
	for _, s := range segs {
		projects[s.ProjectName]++
		taskTypes[taskTypeName(s.TaskType)]++
		fails += s.Fails
	}
	project, n := top(projects)
	first, a := top(taskTypes)
	var b strings.Builder
	fmt.Fprintf(&b, "이번 주 작업 구간은 %d개다. 프로젝트로는 %s %d개가 가장 많았고, 작업 유형으로는 %s %d개가 가장 많았다. ", len(segs), project, n, first, a)
	if fails == 0 {
		b.WriteString("도구 실패는 없었다. ")
	} else {
		worst := items[0].seg
		for _, s := range segs {
			if s.Fails > worst.Fails {
				worst = s
			}
		}
		fmt.Fprintf(&b, "도구 실패 %d회 가운데 %d회가 %s의 %s 구간에서 나왔다. ", fails, worst.Fails, worst.ProjectName, taskTypeName(worst.TaskType))
	}
	fmt.Fprintf(&b, "아래 %d개 구간을 다시 볼 만하다.", len(items))
	return b.String()
}

// writtenAIReport rewrites the server's /api/ai-reports answer for one week so
// the screen shows a finished report: the runtime on, a completed run started at
// the weekly firing (Monday 06:00 in Seoul) after the week, and the report. A
// week still running, one with no work, or one whose firing is still ahead
// keeps no report.
func writtenAIReport(server []byte, segs []reportSegment, now time.Time) []byte {
	var resp map[string]any
	if err := json.Unmarshal(server, &resp); err != nil {
		return server
	}
	resp["runtime"] = map[string]any{"enabled": true, "key": reportRuntime, "configured": true, "available": true, "reason": nil}
	next := time.Date(now.In(kst).Year(), now.In(kst).Month(), now.In(kst).Day(), 6, 0, 0, 0, kst)
	for next.Weekday() != time.Monday || !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	resp["schedule"] = map[string]any{"enabled": true, "enabled_source": "admin", "weekday": 1, "hour": 6, "minute": 0,
		"when_source": "admin", "tz": "Asia/Seoul", "next_run": next}

	open, _ := resp["in_progress"].(bool)
	count, _ := resp["segment_count"].(float64)
	until, err := time.Parse(time.RFC3339, fmt.Sprint(resp["until"]))
	write := func() []byte {
		out, _ := json.Marshal(resp)
		return out
	}
	if open || count == 0 || len(segs) == 0 || err != nil {
		return write()
	}
	items := pickItems(segs)
	started := until.Add(6 * time.Hour)
	ms := func(d int) *int { return &d }
	calls := []map[string]any{
		{"tool": "compare_week", "args": json.RawMessage(`{}`), "result_rows": ms(1), "duration_ms": ms(140)},
		{"tool": "query_segments", "args": json.RawMessage(`{"order_by":"tool_fail_count","limit":10}`), "result_rows": ms(min(10, len(segs))), "duration_ms": ms(210)},
		{"tool": "query_segments", "args": json.RawMessage(`{"order_by":"typed_turn_count","limit":10}`), "result_rows": ms(min(10, len(segs))), "duration_ms": ms(190)},
	}
	for i, it := range items {
		args, _ := json.Marshal(map[string]string{"segment_id": strconv.FormatInt(it.seg.ID, 10)})
		calls = append(calls, map[string]any{"tool": "read_segment", "args": json.RawMessage(args),
			"result_rows": ms(int(12 + it.seg.Tools)), "duration_ms": ms(260 + 45*i)})
	}
	for i, c := range calls {
		c["seq"], c["status"] = i+1, "ok"
	}
	took := time.Duration(24+13*len(items)) * time.Second
	finished := started.Add(took)
	if finished.After(now) {
		return write()
	}
	runID := started.Unix() / 3600 // stable for the week, and distinct between weeks
	in := int64(21000 + 6500*len(items))
	cached, out := in*62/100, int64(1800+320*len(items))
	var reportItems []map[string]any
	for _, it := range items {
		s := it.seg
		reportItems = append(reportItems, map[string]any{
			"segment_id": strconv.FormatInt(s.ID, 10), "title": it.title, "reason": it.reason,
			"meta": map[string]any{"session_id": s.SessionID, "start_ts": s.StartTs, "project_name": s.ProjectName, "agent": s.Agent,
				"typed_turn_count": s.Typed, "tool_call_count": s.Tools, "tool_fail_count": s.Fails},
		})
	}
	resp["run"] = map[string]any{"id": runID, "status": "completed", "started_at": started, "finished_at": finished, "error": nil, "tool_calls": calls}
	resp["report"] = map[string]any{
		"run_id": runID, "generated_at": finished, "tz": "Asia/Seoul", "runtime": reportRuntime, "model": reportModel,
		"usage":       map[string]any{"reported": true, "input_tokens": in, "cached_input_tokens": cached, "output_tokens": out},
		"duration_ms": took.Milliseconds(), "summary": reportSummary(segs, items), "items": reportItems,
		"process": map[string]any{"tool_calls": calls, "segments_read": len(items)},
	}
	return write()
}
