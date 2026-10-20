package localdaemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The daemon's files are the current user's own, made with the modes this
// package asks for. On Windows those modes are not what Go reports back, so a
// check that read them rejected every directory and file the daemon wrote and
// the daemon never started.
func TestDaemonFilesTheUserCreatedAreSafe(t *testing.T) {
	root := t.TempDir()
	if err := safeDirectory(root); err != nil {
		t.Fatalf("a directory the user created: %v", err)
	}
	want := Runtime{PID: 42, InstanceID: "instance"}
	if err := writeJSON(filepath.Join(root, "daemon-runtime.json"), want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRuntime(root)
	if err != nil {
		t.Fatalf("a file the daemon wrote: %v", err)
	}
	if got.PID != want.PID || got.InstanceID != want.InstanceID {
		t.Fatalf("read back %+v, wrote %+v", got, want)
	}
	if running, err := Running(root); err != nil || running {
		t.Fatalf("Running on an idle root = %v, %v; want false, nil", running, err)
	}
}

// The daemon's callers branch on os.ErrNotExist for a file that is not there yet,
// and a file can vanish between the Lstat that found it and the access-list read
// that judges it. Only that failure keeps its identity; every other way of not
// being able to tell is as unsafe as a write grant.
func TestOthersWriteVerdictKeepsOnlyAMissingFile(t *testing.T) {
	const unsafe = "unsafe daemon metadata"
	for _, tc := range []struct {
		name     string
		writable bool
		err      error
		want     string // "" is no error
		missing  bool
	}{
		{name: "not writable", writable: false},
		{name: "writable", writable: true, want: unsafe},
		{name: "cannot tell", err: errors.New("cannot inspect file security"), want: unsafe},
		{name: "no permission to tell", err: fmt.Errorf("inspect file security: %w", os.ErrPermission), want: unsafe},
		{name: "vanished", err: fmt.Errorf("inspect file security: %w", os.ErrNotExist), want: "inspect file security: file does not exist", missing: true},
		{name: "vanished and writable", writable: true, err: fmt.Errorf("inspect file security: %w", os.ErrNotExist), want: "inspect file security: file does not exist", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := othersWriteVerdict(tc.writable, tc.err, unsafe)
			if (got == nil) != (tc.want == "") || got != nil && got.Error() != tc.want {
				t.Fatalf("verdict = %v, want %q", got, tc.want)
			}
			if errors.Is(got, os.ErrNotExist) != tc.missing {
				t.Fatalf("errors.Is(verdict, os.ErrNotExist) = %v, want %v", !tc.missing, tc.missing)
			}
		})
	}
}
