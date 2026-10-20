package profile

import "testing"

func TestHomeDecisionsRejectImplicitAndAmbiguousConsent(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`null`, `[{"provider":"claude","home":"/synthetic","transmission":"allow"}]`, `[{"provider":"claude","home":"/synthetic","transmission":"allow","quota_enabled":null}]`, `[{"provider":"claude","home":"/synthetic","transmission":"allow","quota_enabled":true,"quota_enabled":false}]`, `[{"provider":"claude","home":"/synthetic","transmission":"allow","quota_enabled":true,"future":true}]`} {
		if _, err := DecodeHomeDecisions([]byte(raw)); err == nil {
			t.Fatalf("implicit/ambiguous consent accepted: %s", raw)
		}
	}
	if _, err := DecodeHomeDecisions([]byte(`[{"provider":"claude","home":"/synthetic","transmission":"allow","quota_enabled":false}]`)); err != nil {
		t.Fatal(err)
	}
}
