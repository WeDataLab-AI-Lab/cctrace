//go:build !darwin && !linux

package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateTrustUnsupportedOpenFailsClosed(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "public-ca.pem")
	if err := os.WriteFile(path, []byte("synthetic material"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := openUpdateTrustCA(path)
	if f != nil || err == nil {
		if f != nil {
			f.Close()
		}
		t.Fatalf("unsafe platform open: file=%v err=%v", f, err)
	}
	data, err := readUpdateTrustCA(path, updateTrustMaxBytes)
	if data != nil || err == nil {
		t.Fatalf("unsafe CA read: data=%q err=%v", data, err)
	}
}
