//go:build !windows

package buffer

import "os"

// syncDir makes the new WAL file's directory entry durable. Creating and
// fsyncing a file is not enough on POSIX: the entry that names it lives in the
// directory, and a crash between the two leaves a synced file nothing points at.
func (d *DiskSpiller) syncDir() error {
	dir, err := os.Open(d.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
