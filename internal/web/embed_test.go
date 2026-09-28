package web

import (
	"testing"
	"testing/fstest"
)

// The placeholder that the test/lint make targets drop into internal/web/dist
// satisfies go:embed but contains no dashboard. hasDashboard is what lets the
// server refuse to run in that state instead of serving an empty page, so it
// must distinguish the two shapes exactly.
func TestHasDashboard(t *testing.T) {
	cases := []struct {
		name string
		fsys fstest.MapFS
		want bool
	}{
		{
			name: "real web build",
			fsys: fstest.MapFS{
				"dist/index.html":   {Data: []byte(`<!doctype html><script src="/_next/static/app.js"></script>`)},
				"dist/_next/app.js": {Data: []byte("//")},
			},
			want: true,
		},
		{
			name: "test placeholder only",
			fsys: fstest.MapFS{
				"dist/.gitkeep": {Data: []byte{}},
			},
			want: false,
		},
		{
			// A hand-written or leftover index.html is not a dashboard. Accepting
			// it would let the exact artifact this guard exists to stop —
			// a server with no working UI — through both the build and the
			// startup check.
			name: "index.html without built assets",
			fsys: fstest.MapFS{
				"dist/index.html": {Data: []byte("<html>hi</html>")},
			},
			want: false,
		},
		{
			// An asset tree left over from an earlier build does not make a
			// truncated or emptied entry document servable.
			name: "empty index.html beside a real asset tree",
			fsys: fstest.MapFS{
				"dist/index.html":          {Data: []byte{}},
				"dist/_next/static/app.js": {Data: []byte("//")},
			},
			want: false,
		},
		{
			name: "entry document that never loads the assets",
			fsys: fstest.MapFS{
				"dist/index.html":          {Data: []byte("<html>placeholder page</html>")},
				"dist/_next/static/app.js": {Data: []byte("//")},
			},
			want: false,
		},
		{
			name: "empty embed",
			fsys: fstest.MapFS{},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasDashboard(c.fsys); got != c.want {
				t.Errorf("hasDashboard() = %v, want %v", got, c.want)
			}
		})
	}
}
