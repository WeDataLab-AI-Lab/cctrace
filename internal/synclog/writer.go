// Package synclog bounds the size of the sync daemon's log file.
//
// The daemon used to write straight to an O_APPEND file descriptor with no size
// handling at all, so the log only ever grew: a local log reached 54MB in 50
// days, and one reported from a Windows client had reached 305MB. The log has no
// programmatic consumer — it exists so a person can run `tail` on it during
// troubleshooting — which means old content is expendable but the most recent
// days must survive.
package synclog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	// DefaultMaxSize caps a single log file. Measured growth is tens of KB/day
	// when nothing is failing, ~1MB/day with a retry loop running, and ~4.6MB/day
	// in the worst case observed. 10MB therefore holds months of healthy
	// operation and about two days of a pathological loop.
	DefaultMaxSize = 10 << 20

	// DefaultMaxBackups keeps enough history that a failure loop cannot bury its
	// own cause before anyone looks: at the worst observed growth rate, 5 backups
	// preserve roughly ten days rather than two.
	DefaultMaxBackups = 5
)

// Writer is an io.Writer that rotates its file once it reaches maxSize.
//
// Rotation renames sync.log to sync.log.1, shifting existing backups up by one
// and discarding whatever falls past maxBackups. Writes are serialized, so a
// single Writer is safe to share; a second process writing the same path is not
// supported and is why the daemon's raw stdout/stderr go to a separate file.
type Writer struct {
	path       string
	maxSize    int64
	maxBackups int

	mu      sync.Mutex
	current *os.File
	written int64
}

// New returns a Writer for path. The file is opened on the first write, so
// constructing one is cheap and cannot fail.
//
// A maxSize of zero or less disables rotation entirely, which is only useful in
// tests; maxBackups of zero truncates in place instead of keeping history.
func New(path string, maxSize int64, maxBackups int) *Writer {
	return &Writer{path: path, maxSize: maxSize, maxBackups: maxBackups}
}

// NewDefault returns a Writer with the package's size and retention defaults.
func NewDefault(path string) *Writer {
	return New(path, DefaultMaxSize, DefaultMaxBackups)
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureFile(); err != nil {
		return 0, err
	}
	n, err := w.current.Write(p)
	w.written += int64(n)
	if err != nil {
		return n, err
	}
	if w.maxSize > 0 && w.written >= w.maxSize {
		if err := w.rotate(); err != nil {
			return n, err
		}
	}
	return n, nil
}

// Close releases the underlying file. The Writer stays usable: a later write
// reopens the file, which keeps a mistimed Close from silently dropping logs.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closeCurrent()
}

func (w *Writer) closeCurrent() error {
	if w.current == nil {
		return nil
	}
	err := w.current.Close()
	w.current = nil
	return err
}

// ensureFile opens the log, seeding written from the existing file size.
//
// Carrying the current size forward is what makes the cap hold across restarts.
// Starting the counter at zero would grant every daemon restart another maxSize
// of growth, and the daemon is restarted often — a session hook spawns it, and
// self-update replaces the watch child.
func (w *Writer) ensureFile() error {
	if w.current != nil {
		return nil
	}
	if dir := filepath.Dir(w.path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("synclog: create log dir: %w", err)
		}
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("synclog: open log: %w", err)
	}
	size := int64(0)
	if st, serr := f.Stat(); serr == nil {
		size = st.Size()
	}
	w.current = f
	w.written = size
	return nil
}

// rotate closes the live file, shifts the backups up by one and starts fresh.
//
// The oldest backup is removed first so the renames below never collide, and
// each rename ignores a missing source: backups only exist once enough
// rotations have happened.
func (w *Writer) rotate() error {
	if err := w.closeCurrent(); err != nil {
		return fmt.Errorf("synclog: close before rotate: %w", err)
	}
	w.written = 0

	if w.maxBackups <= 0 {
		if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("synclog: discard log: %w", err)
		}
		return w.ensureFile()
	}

	if err := os.Remove(pathFor(w.path, w.maxBackups)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("synclog: drop oldest backup: %w", err)
	}
	for i := w.maxBackups - 1; i >= 1; i-- {
		err := os.Rename(pathFor(w.path, i), pathFor(w.path, i+1))
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("synclog: shift backup %d: %w", i, err)
		}
	}
	if err := os.Rename(w.path, pathFor(w.path, 1)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("synclog: rotate log: %w", err)
	}
	// Recreate immediately rather than waiting for the next write. The daemon
	// prints this path on startup and the docs tell users to tail it, so the file
	// disappearing between rotations would break `tail -f` for no reason.
	return w.ensureFile()
}

// pathFor names the nth backup of path, e.g. sync.log.2.
func pathFor(path string, n int) string {
	return fmt.Sprintf("%s.%d", path, n)
}

var _ io.WriteCloser = (*Writer)(nil)
