package store

import (
	"strings"
	"testing"
)

func TestMigrationsAddSessionRecordProjectScopeIndex(t *testing.T) {
	ddl := strings.Join(migrations, "\n")
	want := "CREATE INDEX IF NOT EXISTS idx_srec_project_scope ON session_records (project_hash, agent, session_id)"
	if !strings.Contains(ddl, want) {
		t.Fatalf("migrations do not add the latest-activity project scope index %q", want)
	}
}
