package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The server's answer for a week with work and no AI runtime: the screen reads
// it as "off". The written report has to turn it into a finished one built on
// that week's own segments, and leave the server's facts alone.
func TestWrittenAIReportCompletesAWeekFromItsSegments(t *testing.T) {
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, kst)
	server := []byte(`{"week":"2026-W42","tz":"Asia/Seoul","since":"2026-10-12T00:00:00+09:00","until":"2026-10-19T00:00:00+09:00",
		"in_progress":false,"runtime":{"enabled":false,"key":"codex-app-server","configured":false,"available":false,"reason":null},
		"consent":{"required":false,"granted":false,"runtime_key":"codex-app-server:none","disclosure_version":"2026-09-15"},
		"segment_count":3,"schedule":{"enabled":false,"enabled_source":"default","weekday":1,"hour":6,"minute":0,"when_source":"default","tz":"Asia/Seoul","next_run":null},
		"run":null,"report":null}`)
	segs := []reportSegment{
		{ID: 11, SessionID: "s-1", StartTs: start.Add(10 * time.Hour), ProjectName: "nebula-ingest", Agent: "claude", TaskType: "debugging", Typed: 3, Tools: 30, Fails: 4},
		{ID: 12, SessionID: "s-2", StartTs: start.Add(30 * time.Hour), ProjectName: "nebula-cli", Agent: "codex", TaskType: "implementation", Typed: 9, Tools: 22},
		{ID: 13, SessionID: "s-3", StartTs: start.Add(50 * time.Hour), ProjectName: "nebula-ingest", Agent: "claude", TaskType: "testing", Typed: 2, Tools: 8},
	}
	now := start.AddDate(0, 0, 10)
	var got struct {
		Week         string `json:"week"`
		SegmentCount int    `json:"segment_count"`
		Runtime      struct {
			Enabled, Configured, Available bool
		} `json:"runtime"`
		Run struct {
			Status     string    `json:"status"`
			StartedAt  time.Time `json:"started_at"`
			FinishedAt time.Time `json:"finished_at"`
		} `json:"run"`
		Report *struct {
			Summary string `json:"summary"`
			Items   []struct {
				SegmentID string `json:"segment_id"`
				Title     string `json:"title"`
				Meta      struct {
					SessionID string `json:"session_id"`
					Agent     string `json:"agent"`
				} `json:"meta"`
			} `json:"items"`
			Process struct {
				ToolCalls []struct {
					Tool string          `json:"tool"`
					Args json.RawMessage `json:"args"`
				} `json:"tool_calls"`
				SegmentsRead int `json:"segments_read"`
			} `json:"process"`
		} `json:"report"`
	}
	if err := json.Unmarshal(writtenAIReport(server, segs, now), &got); err != nil {
		t.Fatal(err)
	}
	if got.Week != "2026-W42" || got.SegmentCount != 3 {
		t.Fatalf("server facts changed: week %s, %d segments", got.Week, got.SegmentCount)
	}
	if !got.Runtime.Enabled || !got.Runtime.Configured || !got.Runtime.Available || got.Run.Status != "completed" {
		t.Fatalf("runtime %+v, run %s: the screen would not show a finished report", got.Runtime, got.Run.Status)
	}
	if !got.Run.StartedAt.After(start.AddDate(0, 0, 7)) || !got.Run.FinishedAt.After(got.Run.StartedAt) || got.Run.FinishedAt.After(now) {
		t.Fatalf("run %s..%s is not after the week and before now", got.Run.StartedAt, got.Run.FinishedAt)
	}
	if got.Report == nil || len(got.Report.Items) == 0 || !strings.Contains(got.Report.Summary, "nebula-ingest") {
		t.Fatalf("report %+v", got.Report)
	}
	reads := 0
	for _, c := range got.Report.Process.ToolCalls {
		if c.Tool == "read_segment" {
			reads++
		}
	}
	if reads != got.Report.Process.SegmentsRead || reads < len(got.Report.Items) {
		t.Fatalf("%d read_segment calls, segments_read %d, %d items", reads, got.Report.Process.SegmentsRead, len(got.Report.Items))
	}
	bySeg := map[string]reportSegment{"11": segs[0], "12": segs[1], "13": segs[2]}
	for _, it := range got.Report.Items {
		s, ok := bySeg[it.SegmentID]
		if !ok || it.Meta.SessionID != s.SessionID || it.Meta.Agent != s.Agent || it.Title == "" {
			t.Fatalf("item %+v does not point at one of the week's segments", it)
		}
	}
}

// A week still running, or one with no work, has nothing to report yet.
func TestWrittenAIReportLeavesOpenAndEmptyWeeksUnwritten(t *testing.T) {
	start := time.Date(2026, 10, 19, 0, 0, 0, 0, kst)
	open := []byte(`{"week":"2026-W43","in_progress":true,"segment_count":2,"runtime":{},"schedule":{},"run":null,"report":null}`)
	segs := []reportSegment{{ID: 1, SessionID: "s", StartTs: start.Add(time.Hour), ProjectName: "nebula-cli", Agent: "claude", TaskType: "testing"}}
	empty := []byte(`{"week":"2026-W38","in_progress":false,"segment_count":0,"runtime":{},"schedule":{},"run":null,"report":null}`)
	for name, body := range map[string][]byte{"open": open, "empty": empty} {
		var got struct {
			Runtime struct{ Enabled bool } `json:"runtime"`
			Report  any                    `json:"report"`
		}
		if err := json.Unmarshal(writtenAIReport(body, segs, start.AddDate(0, 0, 3)), &got); err != nil {
			t.Fatal(err)
		}
		if !got.Runtime.Enabled || got.Report != nil {
			t.Fatalf("%s week: runtime enabled %v, report %v", name, got.Runtime.Enabled, got.Report)
		}
	}
}
