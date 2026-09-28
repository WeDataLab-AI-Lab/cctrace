package api

import (
	"net/http"
	"testing"

	"cctrace/internal/store"
)

// The weekly screen prints when the report runs by itself and when the next
// firing is, so the week's facts carry both.
func TestGetAIReportsCarriesTheSchedule(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	a.consent(t, aiCaller.ID)

	body := decodeBody(t, a.do(http.MethodGet, "/api/ai-reports?week=2026-W37&tz=Asia%2FSeoul", "", false))
	sched, _ := body["schedule"].(map[string]any)
	if sched == nil {
		t.Fatalf("no schedule in %v", body)
	}
	// Nothing saved anywhere: the built-in default, marked as such, and off.
	if sched["enabled"] != false || sched["enabled_source"] != "default" || sched["when_source"] != "default" {
		t.Fatalf("default = %v", sched)
	}
	if sched["weekday"] != float64(1) || sched["hour"] != float64(6) {
		t.Fatalf("built-in when = %v, want Monday 06:00", sched)
	}
	// Empty, not the browser's zone from the query string. The service decides
	// the zone -- saved, then the last report's, then UTC -- and the scheduler
	// reads the same order, so what the screen prints is what will fire. This
	// caller has neither, so the answer is UTC and the screen says so.
	//
	// It used to echo the query parameter here, which made a Seoul user with no
	// report read "매주 월요일 06:00" for a run that went at 06:00 UTC.
	if sched["tz"] != "" {
		t.Fatalf("tz = %v, want empty (UTC) for a caller with nothing recorded", sched["tz"])
	}
	// Off has no next firing to name.
	if sched["next_run"] != nil {
		t.Fatalf("next_run = %v while off", sched["next_run"])
	}
}

// Once it is on, the screen has a firing to print.
func TestGetAIReportsNamesTheNextRun(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	a.consent(t, aiCaller.ID)
	on := true
	a.st.AutoSchedule = &store.AIAutoSchedule{Enabled: &on}

	body := decodeBody(t, a.do(http.MethodGet, "/api/ai-reports?week=2026-W37&tz=Asia%2FSeoul", "", false))
	sched, _ := body["schedule"].(map[string]any)
	if sched["enabled"] != true || sched["enabled_source"] != "admin" {
		t.Fatalf("enabled = %v", sched)
	}
	if next, _ := sched["next_run"].(string); next == "" {
		t.Fatalf("next_run = %v, want a time", sched["next_run"])
	}
}

// A user changes their own time; the answer is what now applies, not just what
// they sent, so the screen never has to combine the layers itself.
func TestSetAIUserSchedule(t *testing.T) {
	const path = "/api/ai/schedule"
	a := newAITest(t, aiRuntime(), aiCaller)

	if rec := a.do(http.MethodPut, path, `{"hour":21}`, false); rec.Code != http.StatusForbidden {
		t.Fatalf("without CSRF status %d", rec.Code)
	}
	for _, body := range []string{`{"weekday":7}`, `{"hour":24}`, `{"minute":60}`, `{"tz":"Not/AZone"}`} {
		rec := a.do(http.MethodPut, path, body, true)
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["error"] != "invalid_schedule" {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(a.st.UserSchedules) != 0 {
		t.Fatalf("a refused request stored %+v", a.st.UserSchedules)
	}

	rec := a.do(http.MethodPut, path, `{"weekday":5,"hour":18,"minute":30,"tz":"Asia/Seoul"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	sched, _ := decodeBody(t, rec)["schedule"].(map[string]any)
	if sched["weekday"] != float64(5) || sched["hour"] != float64(18) || sched["minute"] != float64(30) {
		t.Fatalf("saved = %v", sched)
	}
	if sched["when_source"] != "user" || sched["tz"] != "Asia/Seoul" {
		t.Fatalf("sources = %v", sched)
	}
	if a.st.UserSchedules[aiCaller.ID] == nil {
		t.Fatalf("stored = %+v", a.st.UserSchedules)
	}
}
