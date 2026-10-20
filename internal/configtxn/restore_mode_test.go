package configtxn

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRestoreModeWritableAndReadonly(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]os.FileMode{{0600, 0600}, {0600, 0400}, {0400, 0600}, {0400, 0400}, {0400, 0440}} {
		t.Run(fmt.Sprintf("%o_to_%o", pair[0], pair[1]), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(p, []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(p, 0600) })
			if err := os.Chmod(p, pair[0]); err != nil {
				t.Fatal(err)
			}
			if err := restoreMode(p, pair[1]); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			mask := os.FileMode(0777)
			if runtime.GOOS == "windows" {
				mask = 0200
			}
			if info.Mode().Perm()&mask != pair[1]&mask {
				t.Fatalf("mode=%o want=%o", info.Mode().Perm(), pair[1])
			}
			data, err := os.ReadFile(p)
			if err != nil || string(data) != "retained" {
				t.Fatalf("content=%s err=%v", data, err)
			}
		})
	}
}

func TestRestoreModeReportsMissingFile(t *testing.T) {
	t.Parallel()
	if err := restoreMode(filepath.Join(t.TempDir(), "missing"), 0600); !os.IsNotExist(err) {
		t.Fatalf("missing file error=%v", err)
	}
}
