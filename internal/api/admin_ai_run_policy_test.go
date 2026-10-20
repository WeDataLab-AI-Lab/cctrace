package api

import (
	"net/http"
	"testing"
)

// The run policy is an AI setting, so it takes the same gates as the others: a
// browser session, an admin, and CSRF on the write.
func TestAdminAISetRunPolicyGates(t *testing.T) {
	const path = "/api/admin/ai/run-policy"
	const body = `{"consent_required":true}`

	a := newAITest(t, aiRuntime(), aiAdmin)
	if rec := a.do(http.MethodPut, path, body, false); rec.Code != http.StatusForbidden {
		t.Fatalf("without CSRF status %d", rec.Code)
	}
	if rec := newAITest(t, aiRuntime(), aiCaller).do(http.MethodPut, path, body, true); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status %d", rec.Code)
	}
	if a.st.RunPolicy != nil {
		t.Fatalf("a refused request stored %+v", a.st.RunPolicy)
	}
}

// Nothing saved reads as consent not required, marked as the default, so the
// screen shows the switch in the state every user is actually in.
func TestAdminAIShowsRunPolicyDefault(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiAdmin)

	view, _ := decodeBody(t, a.do(http.MethodGet, "/api/admin/ai", "", false))["run_policy"].(map[string]any)
	if view == nil {
		t.Fatal("the admin view carries no run policy")
	}
	if view["consent_required"] != false || view["consent_required_source"] != "default" {
		t.Fatalf("default = %v", view)
	}
	if view["max_concurrent"] != 2.0 || view["max_concurrent_source"] != "default" ||
		view["max_concurrent_default"] != 2.0 || view["max_concurrent_max"] != 10.0 {
		t.Fatalf("default limit = %v", view)
	}
}

// The limit is saved and shown like the switch, and one outside the range the
// screen offers is refused before it is stored.
func TestAdminAISetRunPolicyLimit(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiAdmin)

	for _, body := range []string{`{"max_concurrent":0}`, `{"max_concurrent":11}`} {
		rec := a.do(http.MethodPut, "/api/admin/ai/run-policy", body, true)
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["error"] != "invalid_run_policy" {
			t.Fatalf("%s: status %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if a.st.RunPolicy != nil {
		t.Fatalf("a refused limit was stored: %+v", a.st.RunPolicy)
	}

	rec := a.do(http.MethodPut, "/api/admin/ai/run-policy", `{"consent_required":null,"max_concurrent":4}`, true)
	saved, _ := decodeBody(t, rec)["run_policy"].(map[string]any)
	if rec.Code != http.StatusOK || saved["max_concurrent"] != 4.0 || saved["max_concurrent_source"] != "admin" || saved["consent_required"] != false {
		t.Fatalf("status %d saved %v", rec.Code, saved)
	}
}

// Saving answers with the resolved policy, the weekly screen of a user who has
// not consented then asks for it, and null hands the switch back to the default.
func TestAdminAISetRunPolicySavesAndShows(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiAdmin)

	rec := a.do(http.MethodPut, "/api/admin/ai/run-policy", `{"consent_required":true}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	saved, _ := decodeBody(t, rec)["run_policy"].(map[string]any)
	if saved["consent_required"] != true || saved["consent_required_source"] != "admin" {
		t.Fatalf("saved = %v", saved)
	}
	if a.st.RunPolicy == nil || a.st.RunPolicy.UpdatedBy != aiAdmin.Email {
		t.Fatalf("stored = %+v", a.st.RunPolicy)
	}
	consent, _ := decodeBody(t, a.do(http.MethodGet, "/api/ai-reports?week=2026-W37&tz=Asia/Seoul", "", false))["consent"].(map[string]any)
	if consent["required"] != true || consent["granted"] != false {
		t.Fatalf("weekly consent = %v", consent)
	}

	rec = a.do(http.MethodPut, "/api/admin/ai/run-policy", `{"consent_required":null}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status %d: %s", rec.Code, rec.Body.String())
	}
	if a.st.RunPolicy != nil {
		t.Fatalf("cleared = %+v", a.st.RunPolicy)
	}
}

func TestAdminAISetRunPolicyRejectsBadBody(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiAdmin)
	for _, body := range []string{`{`, `{"consent_required":"yes"}`} {
		rec := a.do(http.MethodPut, "/api/admin/ai/run-policy", body, true)
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["error"] != "invalid_request" {
			t.Fatalf("%s: status %d", body, rec.Code)
		}
	}
	if a.st.RunPolicy != nil {
		t.Fatalf("a refused request stored %+v", a.st.RunPolicy)
	}
}
