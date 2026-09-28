package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/store"
)

// Clients older than this change keep sending the POSIX-trimmed spelling, and a
// Windows client sent a raw path with backslashes. If the server stored what it was
// handed, one directory would keep landing under several keys for as long as any old
// client exists -- so the normalisation has to happen here, where every agent and
// every client version arrives (#303).
func TestSyncCanonicalisesProjectHashFromAnyClient(t *testing.T) {
	cases := []struct {
		name, sent, want string
	}{
		{"old posix client omits the leading dash", "users-alice-myapp", "-users-alice-myapp"},
		{"windows client sends a raw path", `C:\work\app\.agents`, "c--work-app--agents"},
		{"already canonical is left alone", "-users-alice-myapp", "-users-alice-myapp"},
		{"codex fallback key is untouched", "codex-9f8e7d", "codex-9f8e7d"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var inserted []*store.SessionRecord
			var upsertedHash string
			m := &mockStore{
				insertSessionRecordsFn: func(_ context.Context, records []*store.SessionRecord) error {
					inserted = records
					return nil
				},
				upsertProjectFn: func(_ context.Context, _, projectHash, _, _, _, _ string) error {
					upsertedHash = projectHash
					return nil
				},
			}
			srv := newTestServer(m, nil)
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			body := `{"agent":"codex","project_hash":` + quote(c.sent) +
				`,"records":[{"record_type":"assistant"}]}`
			resp, err := http.Post(ts.URL+"/api/sync", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if len(inserted) != 1 {
				t.Fatalf("inserted %d records, want 1", len(inserted))
			}
			if inserted[0].ProjectHash != c.want {
				t.Errorf("record project_hash = %q, want %q", inserted[0].ProjectHash, c.want)
			}
			if upsertedHash != c.want {
				t.Errorf("upserted project hash = %q, want %q", upsertedHash, c.want)
			}
		})
	}
}

// A per-record hash has to be normalised too: records carry their own when a sync
// run spans more than one project.
func TestSyncCanonicalisesPerRecordProjectHash(t *testing.T) {
	var inserted []*store.SessionRecord
	m := &mockStore{
		insertSessionRecordsFn: func(_ context.Context, records []*store.SessionRecord) error {
			inserted = records
			return nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"agent":"codex","project_hash":"-users-alice-a","records":[` +
		`{"record_type":"assistant","project_hash":"users-alice-b"}]}`
	resp, err := http.Post(ts.URL+"/api/sync", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if len(inserted) != 1 {
		t.Fatalf("inserted %d records, want 1", len(inserted))
	}
	if inserted[0].ProjectHash != "-users-alice-b" {
		t.Errorf("per-record hash = %q, want %q", inserted[0].ProjectHash, "-users-alice-b")
	}
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
