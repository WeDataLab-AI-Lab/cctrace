package api

import (
	"net/http"
	"testing"

	"cctrace/internal/aireport"
)

// The schedule is an AI setting, so it takes the same gates as the others: a
// browser session, an admin, and CSRF on the write.
func TestAdminAISetScheduleGates(t *testing.T) {
	const path = "/api/admin/ai/schedule"
	const body = `{"enabled":true,"weekday":3,"hour":15,"minute":30}`

	a := newAITest(t, aiRuntime(), aiAdmin)
	if rec := a.do(http.MethodPut, path, body, false); rec.Code != http.StatusForbidden {
		t.Fatalf("without CSRF status %d", rec.Code)
	}
	if rec := newAITest(t, aiRuntime(), aiCaller).do(http.MethodPut, path, body, true); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status %d", rec.Code)
	}
	if a.st.AutoSchedule != nil {
		t.Fatalf("a refused request stored %+v", a.st.AutoSchedule)
	}
}

// A weekday or time outside its range is refused before it is stored. The
// scheduler would otherwise read a row it cannot turn into a firing.
func TestAdminAISetScheduleRejectsOutOfRange(t *testing.T) {
	const path = "/api/admin/ai/schedule"
	a := newAITest(t, aiRuntime(), aiAdmin)

	for _, body := range []string{
		`{"enabled":true,"weekday":7}`,
		`{"enabled":true,"weekday":-1}`,
		`{"enabled":true,"hour":24}`,
		`{"enabled":true,"minute":60}`,
	} {
		rec := a.do(http.MethodPut, path, body, true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", body, rec.Code)
		}
		if got := decodeBody(t, rec)["error"]; got != "invalid_schedule" {
			t.Fatalf("%s: error = %v", body, got)
		}
	}
	if a.st.AutoSchedule != nil {
		t.Fatalf("a refused request stored %+v", a.st.AutoSchedule)
	}
}

// Saving answers with the resolved schedule, and the admin view reports the
// same thing, so the screen never has to guess what took effect.
func TestAdminAISetScheduleSavesAndShows(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiAdmin)

	rec := a.do(http.MethodPut, "/api/admin/ai/schedule", `{"enabled":true,"weekday":3,"hour":15,"minute":30}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	saved, _ := decodeBody(t, rec)["schedule"].(map[string]any)
	if saved["enabled"] != true || saved["weekday"] != float64(3) || saved["hour"] != float64(15) || saved["minute"] != float64(30) {
		t.Fatalf("saved = %v", saved)
	}
	if saved["enabled_source"] != "admin" || saved["when_source"] != "admin" {
		t.Fatalf("sources = %v", saved)
	}
	if a.st.AutoSchedule == nil || a.st.AutoSchedule.UpdatedBy != aiAdmin.Email {
		t.Fatalf("stored = %+v", a.st.AutoSchedule)
	}

	view, _ := decodeBody(t, a.do(http.MethodGet, "/api/admin/ai", "", false))["schedule"].(map[string]any)
	if view["weekday"] != float64(3) || view["hour"] != float64(15) || view["enabled"] != true {
		t.Fatalf("admin view = %v", view)
	}
}

// Nothing saved still shows a schedule: the built-in default, marked as such,
// so the screen shows what would happen if an admin turned it on.
func TestAdminAIShowsBuiltInScheduleByDefault(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiAdmin)

	view, _ := decodeBody(t, a.do(http.MethodGet, "/api/admin/ai", "", false))["schedule"].(map[string]any)
	if view == nil {
		t.Fatal("the admin view carries no schedule")
	}
	if view["enabled"] != false || view["enabled_source"] != "default" || view["when_source"] != "default" {
		t.Fatalf("default = %v", view)
	}
	if view["weekday"] != float64(1) || view["hour"] != float64(6) || view["minute"] != float64(0) {
		t.Fatalf("built-in when = %v, want Monday 06:00", view)
	}
}

// The admin's weekday and time are read in each user's zone, and for everyone
// who has none that is the deployment's default -- so the admin screen names
// it, and the weekly screen of such a user shows the same zone.
func TestAIScheduleShowsTheDefaultZone(t *testing.T) {
	cfg := aireport.Config{EnvRuntime: aireport.DefaultRuntimeKey, DefaultTZ: "Asia/Seoul"}

	admin := newAIRegistryTest(t, aiAdmin, aireport.NewMemStore(), cfg, aiRuntime())
	view, _ := decodeBody(t, admin.do(http.MethodGet, "/api/admin/ai", "", false))["schedule"].(map[string]any)
	if view["tz"] != "Asia/Seoul" {
		t.Fatalf("admin schedule tz = %v, want the deployment's default", view["tz"])
	}

	user := newAIRegistryTest(t, aiCaller, aireport.NewMemStore(), cfg, aiRuntime())
	user.consent(t, aiCaller.ID)
	sched, _ := decodeBody(t, user.do(http.MethodGet, "/api/ai-reports?week=2026-W37&tz=UTC", "", false))["schedule"].(map[string]any)
	if sched["tz"] != "Asia/Seoul" {
		t.Fatalf("user schedule tz = %v, want the deployment's default", sched["tz"])
	}
}
