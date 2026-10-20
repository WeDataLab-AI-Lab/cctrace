//go:build !windows

package filesec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWritableByOthersFollowsGroupAndWorldWriteBits(t *testing.T) {
	for _, tc := range []struct {
		mode os.FileMode
		want bool
	}{
		{0700, false},
		{0750, false},
		{0755, false},
		{0644, false},
		{0600, false},
		{0720, true},
		{0702, true},
		{0770, true},
		{0777, true},
		{0662, true},
	} {
		dir := filepath.Join(t.TempDir(), "d")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{dir, file} {
			// chmod, not the create mode: the umask would clear the bits under test.
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := WritableByOthers(path, info)
			if err != nil || got != tc.want {
				t.Errorf("mode %04o: WritableByOthers = %v, %v; want %v", tc.mode, got, err, tc.want)
			}
		}
	}
}
