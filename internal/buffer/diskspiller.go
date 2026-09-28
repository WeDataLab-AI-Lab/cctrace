package buffer

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DiskSpiller writes data to WAL files on disk when the primary queue is unavailable.
// Files are NDJSON (newline-delimited JSON), one record per line.
type DiskSpiller struct {
	dir     string
	maxSize int64 // max bytes per WAL file before rotation
	mu      sync.Mutex
	current *os.File
	written int64
}

// NewDiskSpiller creates a new DiskSpiller that writes WAL files to dir.
// maxSize is the maximum size in bytes of each WAL file before rotating.
func NewDiskSpiller(dir string, maxSize int64) *DiskSpiller {
	return &DiskSpiller{
		dir:     dir,
		maxSize: maxSize,
	}
}

// Spill appends data as a single line to the current WAL file.
func (d *DiskSpiller) Spill(data []byte) error {
	return d.SpillBatch([][]byte{data})
}

// SpillBatch appends records and syncs once at the batch boundary. Shutdown
// drains can contain thousands of records, so syncing every record would exceed
// the container grace period precisely while the primary queue is unavailable.
func (d *DiskSpiller) SpillBatch(records [][]byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, data := range records {
		if err := d.ensureFile(); err != nil {
			return fmt.Errorf("diskspiller: ensure file: %w", err)
		}

		record := make([]byte, len(data)+1)
		copy(record, data)
		record[len(data)] = '\n'
		n, err := d.current.Write(record)
		if err != nil {
			return fmt.Errorf("diskspiller: write: %w", err)
		}
		if n != len(record) {
			return fmt.Errorf("diskspiller: write: %w", io.ErrShortWrite)
		}
		d.written += int64(n)

		if d.written >= d.maxSize {
			if err := d.current.Sync(); err != nil {
				return fmt.Errorf("diskspiller: sync rotated file: %w", err)
			}
			if err := d.current.Close(); err != nil {
				return fmt.Errorf("diskspiller: close rotated file: %w", err)
			}
			d.current = nil
			d.written = 0
		}
	}

	if d.current != nil {
		if err := d.current.Sync(); err != nil {
			return fmt.Errorf("diskspiller: sync: %w", err)
		}
	}
	return nil
}

// Recover returns an iterator over all records in existing WAL files.
// Records are returned in file order (oldest first).
func (d *DiskSpiller) Recover() iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		d.mu.Lock()
		defer d.mu.Unlock()

		files, err := d.walFiles()
		if err != nil {
			return
		}
		for _, path := range files {
			f, err := os.Open(path)
			if err != nil {
				return
			}
			reader := bufio.NewReader(f)
			for {
				line, readErr := reader.ReadBytes('\n')
				if readErr != nil {
					if readErr == io.EOF && len(line) == 0 {
						f.Close()
						break
					}
					f.Close()
					return
				}
				line = bytes.TrimSuffix(line, []byte{'\n'})
				line = bytes.TrimSuffix(line, []byte{'\r'})
				if !yield(line) {
					f.Close()
					return
				}
			}
		}
	}
}

// Replay sends complete WAL records in order and removes only records accepted
// by send. A failed record, all later records, and an incomplete crash tail stay
// on disk for the next recovery attempt.
func (d *DiskSpiller) Replay(send func([]byte) error) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.current != nil {
		if err := d.current.Sync(); err != nil {
			return 0, fmt.Errorf("diskspiller: sync before replay: %w", err)
		}
		if err := d.current.Close(); err != nil {
			return 0, fmt.Errorf("diskspiller: close before replay: %w", err)
		}
		d.current = nil
		d.written = 0
	}

	files, err := d.walFiles()
	if err != nil {
		return 0, err
	}

	replayed := 0
	for _, path := range files {
		acknowledged, replayErr := d.replayFile(path, send)
		replayed += acknowledged
		if replayErr != nil {
			return replayed, replayErr
		}
	}
	return replayed, nil
}

// replayFile returns after deleting a fully replayed file or rewriting it to
// contain only the unacknowledged suffix.
func (d *DiskSpiller) replayFile(path string, send func([]byte) error) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("diskspiller: open %s: %w", path, err)
	}

	reader := bufio.NewReader(f)
	var acknowledgedOffset int64
	acknowledged := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			if closeErr := f.Close(); closeErr != nil {
				return acknowledged, fmt.Errorf("diskspiller: close %s: %w", path, closeErr)
			}
			if err := d.trimAcknowledged(path, acknowledgedOffset); err != nil {
				return acknowledged, err
			}
			if readErr == io.EOF && len(line) == 0 {
				return acknowledged, nil
			}
			return acknowledged, fmt.Errorf("diskspiller: incomplete or unreadable record in %s: %w", path, readErr)
		}

		record := bytes.TrimSuffix(line, []byte{'\n'})
		record = bytes.TrimSuffix(record, []byte{'\r'})
		if err := send(record); err != nil {
			if closeErr := f.Close(); closeErr != nil {
				return acknowledged, fmt.Errorf("diskspiller: close %s: %w", path, closeErr)
			}
			if trimErr := d.trimAcknowledged(path, acknowledgedOffset); trimErr != nil {
				return acknowledged, trimErr
			}
			return acknowledged, fmt.Errorf("diskspiller: replay %s: %w", path, err)
		}
		acknowledged++
		acknowledgedOffset += int64(len(line))
	}
}

func (d *DiskSpiller) trimAcknowledged(path string, offset int64) error {
	if offset == 0 {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("diskspiller: stat %s: %w", path, err)
		}
		if info.Size() != 0 {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("diskspiller: remove %s: %w", path, err)
		}
		return d.syncDir()
	}

	src, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("diskspiller: reopen %s: %w", path, err)
	}
	defer src.Close()
	if _, err := src.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("diskspiller: seek %s: %w", path, err)
	}

	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("diskspiller: stat %s: %w", path, err)
	}
	if offset == info.Size() {
		if err := src.Close(); err != nil {
			return fmt.Errorf("diskspiller: close %s: %w", path, err)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("diskspiller: remove %s: %w", path, err)
		}
		return d.syncDir()
	}

	tmp, err := os.CreateTemp(d.dir, ".wal-replay-*")
	if err != nil {
		return fmt.Errorf("diskspiller: create replay temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return fmt.Errorf("diskspiller: rewrite %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("diskspiller: sync replay temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("diskspiller: close replay temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("diskspiller: replace %s: %w", path, err)
	}
	return d.syncDir()
}

// Cleanup removes all WAL files.
func (d *DiskSpiller) Cleanup() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.current != nil {
		if err := d.current.Sync(); err != nil {
			return fmt.Errorf("diskspiller: sync before cleanup: %w", err)
		}
		if err := d.current.Close(); err != nil {
			return fmt.Errorf("diskspiller: close before cleanup: %w", err)
		}
		d.current = nil
		d.written = 0
	}

	files, err := d.walFiles()
	if err != nil {
		return err
	}
	for _, path := range files {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("diskspiller: remove %s: %w", path, err)
		}
	}
	return d.syncDir()
}

// FileCount returns the number of WAL files.
func (d *DiskSpiller) FileCount() (int, error) {
	files, err := d.walFiles()
	return len(files), err
}

// Close closes the current WAL file if open.
func (d *DiskSpiller) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.current != nil {
		err := d.current.Close()
		d.current = nil
		d.written = 0
		return err
	}
	return nil
}

// ensureFile creates or opens the current WAL file. Must be called with mu held.
func (d *DiskSpiller) ensureFile() error {
	if d.current != nil {
		return nil
	}
	if err := os.MkdirAll(d.dir, 0755); err != nil {
		return err
	}
	name := fmt.Sprintf("wal-%d.ndjson", time.Now().UnixNano())
	path := filepath.Join(d.dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	if err := d.syncDir(); err != nil {
		f.Close()
		return err
	}
	d.current = f
	d.written = 0
	return nil
}

// walFiles returns sorted WAL file paths (oldest first).
func (d *DiskSpiller) walFiles() ([]string, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "wal-") && strings.HasSuffix(e.Name(), ".ndjson") {
			files = append(files, filepath.Join(d.dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}
