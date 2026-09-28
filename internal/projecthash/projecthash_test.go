package projecthash

import "testing"

// Claude Code names the directory under its projects folder itself, and that name IS
// the project_hash for claude sessions. We do not get to choose the convention; we
// have to reproduce it, or the same directory lands under two different keys
// depending on which agent recorded the session (#303).
//
// The shapes below were taken from real directory names -- a leading separator, a dot
// directory, an underscore, a drive letter in either case, multi-byte text, a space --
// with generic names substituted for the paths they came from. Lowercased, because we
// deliberately fold case that Claude Code preserves (see FromPath).
func TestFromPathDerivesTheProjectHash(t *testing.T) {
	cases := []struct{ name, cwd, want string }{
		{
			name: "posix keeps the leading separator as a dash",
			cwd:  "/Users/alice/myapp",
			want: "-users-alice-myapp",
		},
		{
			name: "a dot directory is not special",
			cwd:  "/Users/alice/.cctrace",
			want: "-users-alice--cctrace",
		},
		{
			name: "underscore becomes a dash",
			cwd:  `C:\work\app_v2`,
			want: "c--work-app-v2",
		},
		{
			name: "windows drive letter is folded with everything else",
			cwd:  `F:\PROJECT\08_DEMO`,
			want: "f--project-08-demo",
		},
		{
			name: "either drive case reaches the same hash",
			cwd:  `c:\project\example-app`,
			want: "c--project-example-app",
		},
		{
			name: "windows dot directory",
			cwd:  `C:\work\app\.agents`,
			want: "c--work-app--agents",
		},
		{
			// \uc0b0\ub9bc\uc218 is three runes and nine UTF-8 bytes: one dash each,
			// not one per byte. Written as escapes so this file stays ASCII.
			name: "one dash per rune, not per byte",
			cwd:  "C:\\work\\p_\uc0b0\ub9bc\uc218",
			want: "c--work-p----",
		},
		{
			name: "spaces are replaced like any other character",
			cwd:  `C:\work\2026 Annual Report`,
			want: "c--work-2026-annual-report",
		},
		{
			name: "empty stays empty so callers can tell 'no project' apart",
			cwd:  "",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FromPath(c.cwd); got != c.want {
				t.Errorf("FromPath(%q)\n got %q\nwant %q", c.cwd, got, c.want)
			}
		})
	}
}

// Backfilling existing rows means running this over values that are already hashes.
// If it were not idempotent, a migration that ran twice would corrupt them.
func TestFromPathIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"-users-alice-myapp",
		"f--project-08-demo",
		"c--work-p----",
		"",
	} {
		if got := FromPath(in); got != in {
			t.Errorf("FromPath(%q) = %q, want unchanged", in, got)
		}
	}
}

// Repair is what runs against values already in the database and against what old
// clients keep sending, so its judgement calls need to be pinned.
func TestRepairRestoresWhatTheOldClientsDropped(t *testing.T) {
	cases := []struct{ name, stored, want string }{
		{"old posix hash regains its leading separator", "users-alice-myapp", "-users-alice-myapp"},
		{"canonical posix hash is left alone", "-users-alice-myapp", "-users-alice-myapp"},
		{"windows form is already rooted", "c--work-app", "c--work-app"},
		{"raw windows path is converted, not prefixed", `C:\work\app`, "c--work-app"},
		{"codex fallback key is not a path", "codex-9f8e7d6c", "codex-9f8e7d6c"},
		{"old posix hash with an underscore is converted and prefixed", "users-alice-my_app", "-users-alice-my-app"},
		{"empty stays empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Repair(c.stored); got != c.want {
				t.Errorf("Repair(%q) = %q, want %q", c.stored, got, c.want)
			}
		})
	}
}

// A backfill that ran twice must not keep adding dashes.
func TestRepairIsIdempotent(t *testing.T) {
	for _, in := range []string{"users-alice-myapp", `C:\work\app`, "codex-9f8e", ""} {
		once := Repair(in)
		if twice := Repair(once); twice != once {
			t.Errorf("Repair(Repair(%q)) = %q, want %q", in, twice, once)
		}
	}
}

// Case folding is the one place this deliberately differs from Claude Code, so the
// merges it is there to produce are worth stating outright.
func TestFromPathFoldsCaseSoOneDirectoryIsOneProject(t *testing.T) {
	pairs := [][2]string{
		// A case-insensitive filesystem reports the same directory both ways; a
		// segment spelled two ways was eleven separate projects in real data.
		// Written without a root because folding has nothing to do with the root.
		{"Documents/Code", "Documents/code"},
		// A Windows drive letter arrives in either case.
		{`C:\work\app`, `c:\work\app`},
	}
	for _, p := range pairs {
		if a, b := FromPath(p[0]), FromPath(p[1]); a != b {
			t.Errorf("FromPath(%q) = %q but FromPath(%q) = %q; want one project", p[0], a, p[1], b)
		}
	}
}

// The project name is what a person reads in a list, and on Windows it was the whole
// path: splitting on '/' alone leaves a backslash path intact (#303).
func TestNameFromPathTakesTheLastSegmentOnEitherPlatform(t *testing.T) {
	cases := []struct{ cwd, want string }{
		{"/Users/alice/myapp", "myapp"},
		{"/Users/alice/myapp/", "myapp"},
		{`C:\work\app\.agents`, ".agents"},
		{`c:\project\example-app`, "example-app"},
		{`C:\work\app_v2\`, "app_v2"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NameFromPath(c.cwd); got != c.want {
			t.Errorf("NameFromPath(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
}
