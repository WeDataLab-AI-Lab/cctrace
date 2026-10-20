package processio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"cctrace/internal/atomicfile"
	"cctrace/internal/filesec"
)

const (
	maxQueuedProcessIOReports = 128
	maxProcessIOQueueBytes    = 256 << 10
	processIOQueueRetention   = 7 * 24 * time.Hour
)

var ErrProcessIOQueueFull = errors.New("process I/O retry queue is full")

type QueuedReport struct {
	SourceHome      string     `json:"source_home,omitempty"`
	SourceRevision  string     `json:"source_revision,omitempty"`
	ProfileEmail    string     `json:"profile_email"`
	UserID          string     `json:"user_id"`
	SessionID       string     `json:"session_id"`
	Agent           string     `json:"agent"`
	ProjectHash     string     `json:"project_hash"`
	BillingProvider string     `json:"billing_provider"`
	AccountID       string     `json:"account_id"`
	PID             int        `json:"pid"`
	ProcStart       string     `json:"proc_start"`
	Status          string     `json:"status"`
	StatusSource    string     `json:"status_source,omitempty"`
	StatusChangedAt *time.Time `json:"status_changed_at,omitempty"`
	IOEpoch         string     `json:"io_epoch"`
	Report          Report     `json:"report"`
}

type processIOQueueFile struct {
	Version int            `json:"version"`
	Reports []QueuedReport `json:"reports"`
}

type Queue struct{ Path string }

func (q Queue) Upsert(report QueuedReport) error {
	if err := validateQueuedReport(report); err != nil {
		return err
	}
	return q.withLock(func() error {
		reports, err := q.readLocked()
		if err != nil {
			return err
		}
		reports = removeExpired(reports, time.Now().UTC().Add(-processIOQueueRetention))
		key := queuedReportKey(report)
		updated := false
		for i := range reports {
			if queuedReportKey(reports[i]) == key {
				reports[i] = report
				updated = true
				break
			}
		}
		if !updated && len(reports) >= maxQueuedProcessIOReports {
			return ErrProcessIOQueueFull
		}
		if !updated {
			reports = append(reports, report)
		}
		sort.Slice(reports, func(i, j int) bool {
			if reports[i].Report.AsOf.Equal(reports[j].Report.AsOf) {
				return queuedReportKey(reports[i]) < queuedReportKey(reports[j])
			}
			return reports[i].Report.AsOf.Before(reports[j].Report.AsOf)
		})
		return q.writeLocked(reports)
	})
}

func (q Queue) List() ([]QueuedReport, error) {
	var reports []QueuedReport
	err := q.withLock(func() error {
		loaded, err := q.readLocked()
		if err != nil {
			return err
		}
		cutoff := time.Now().UTC().Add(-processIOQueueRetention)
		reports = removeExpired(loaded, cutoff)
		if len(reports) != len(loaded) {
			return q.writeLocked(reports)
		}
		return nil
	})
	return reports, err
}

// Ack removes only the snapshot that was actually uploaded. A newer report
// written while the upload was in flight stays queued.
func (q Queue) Ack(uploaded QueuedReport) error {
	return q.withLock(func() error {
		reports, err := q.readLocked()
		if err != nil {
			return err
		}
		key := queuedReportKey(uploaded)
		kept := reports[:0]
		for _, item := range reports {
			if queuedReportKey(item) == key && item.Report.AsOf.Equal(uploaded.Report.AsOf) {
				continue
			}
			kept = append(kept, item)
		}
		if len(kept) == len(reports) {
			return nil
		}
		return q.writeLocked(kept)
	})
}

func (q Queue) withLock(run func() error) error {
	if strings.TrimSpace(q.Path) == "" {
		return errors.New("process I/O queue path is empty")
	}
	dir := filepath.Dir(q.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create process I/O queue directory: %w", err)
	}
	if info, err := os.Stat(dir); err != nil || !privateQueueDirectory(dir, info) {
		return errors.New("process I/O queue directory must be private")
	}
	lock := flock.New(q.Path + ".lock")
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("lock process I/O queue: %w", err)
	}
	defer lock.Unlock() //nolint:errcheck // release on every queue path
	return run()
}

// privateQueueDirectory is the queue's rule for where it may live. On Unix the
// directory must be closed to group and other. Windows reports every directory as
// 0777, so its mode bits say nothing: there the directory must not be writable by
// other users, so that none can plant entries the client would upload. Whether
// other users can read it is not checked on Windows.
func privateQueueDirectory(path string, info os.FileInfo) bool {
	if os.PathSeparator == '\\' {
		writable, err := filesec.WritableByOthers(path, info)
		return err == nil && !writable
	}
	return info.Mode().Perm()&0077 == 0
}

func (q Queue) readLocked() ([]QueuedReport, error) {
	data, err := os.ReadFile(q.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > maxProcessIOQueueBytes {
		return nil, errors.New("process I/O queue exceeds its size limit")
	}
	var file processIOQueueFile
	if err := json.Unmarshal(data, &file); err != nil || file.Version != 1 || len(file.Reports) > maxQueuedProcessIOReports {
		return nil, errors.New("process I/O queue is invalid")
	}
	for _, report := range file.Reports {
		if err := validateQueuedReport(report); err != nil {
			return nil, errors.New("process I/O queue contains an invalid report")
		}
	}
	return file.Reports, nil
}

func (q Queue) writeLocked(reports []QueuedReport) error {
	data, err := json.Marshal(processIOQueueFile{Version: 1, Reports: reports})
	if err != nil {
		return err
	}
	if len(data) > maxProcessIOQueueBytes {
		return errors.New("process I/O queue exceeds its size limit")
	}
	return atomicfile.Write(q.Path, data, 0600)
}

func validateQueuedReport(report QueuedReport) error {
	if !validSessionID(report.SessionID) || report.PID <= 0 || report.ProcStart == "" ||
		report.Agent != "claude" && report.Agent != "codex" || report.ProjectHash == "" ||
		!validSessionID(report.IOEpoch) || report.Report.AsOf.IsZero() || len(report.ProcStart) > 256 ||
		len(report.ProjectHash) > 256 || len(report.BillingProvider) > 64 || len(report.AccountID) > 256 ||
		len(report.ProfileEmail) > 256 || len(report.UserID) > 256 {
		return errors.New("process I/O queue report is incomplete")
	}
	if report.Report.Partial != (report.Report.FailureCode != "") {
		return errors.New("process I/O queue partial status and failure code disagree")
	}
	if report.Report.FailureCode != "" && !processIOFailureCodeAllowed(report.Report.FailureCode) {
		return errors.New("process I/O queue failure code is not allowed")
	}
	if report.Status != "busy" && report.Status != "idle" && report.Status != "waiting" && report.Status != "shell" && report.Status != "unknown" && report.Status != "gone" {
		return errors.New("process I/O queue report has an invalid process status")
	}
	if report.StatusSource != "" && !processIOStatusSourceAllowed(report.StatusSource) {
		return errors.New("process I/O queue report has an invalid status source")
	}
	if report.StatusSource == "process_io_lifecycle" && report.Status != "gone" {
		return errors.New("process I/O lifecycle source must describe a gone process")
	}
	if report.Status == "unknown" && report.StatusSource != "process_io_unknown" || report.StatusSource == "process_io_unknown" && report.Status != "unknown" {
		return errors.New("unknown process status and source disagree")
	}
	if report.StatusSource == "process_io_unknown" && report.Report.Final ||
		report.StatusSource == "process_io_lifecycle" && !report.Report.Final {
		return errors.New("process I/O lifecycle status source disagrees with final report")
	}
	c := report.Report.Counts
	if c.InputBytes < 0 || c.InputLines < 0 || c.OutputBytes < 0 || c.OutputLines < 0 {
		return errors.New("process I/O queue report has a negative counter")
	}
	switch report.Report.Mode {
	case ModePTY:
		if c.StdoutBytes != nil || c.StdoutLines != nil || c.StderrBytes != nil || c.StderrLines != nil {
			return errors.New("PTY queue report cannot claim separate stdout/stderr counters")
		}
	case ModePipes:
		if c.StdoutBytes == nil || c.StdoutLines == nil || c.StderrBytes == nil || c.StderrLines == nil ||
			*c.StdoutBytes < 0 || *c.StdoutLines < 0 || *c.StderrBytes < 0 || *c.StderrLines < 0 {
			return errors.New("pipe queue report needs non-negative stdout/stderr counters")
		}
	default:
		return errors.New("process I/O queue report has an invalid mode")
	}
	return nil
}

func processIOStatusSourceAllowed(source string) bool {
	switch source {
	case "legacy", "claude_process_file", "process_liveness", "process_io_unknown", "process_io_lifecycle":
		return true
	default:
		return false
	}
}

func processIOFailureCodeAllowed(code string) bool {
	switch code {
	case FailureInputRelay, FailureOutputRelay, FailureUpload, FailureResize, FailureMapping, "terminal_restore_failed":
		return true
	default:
		return false
	}
}

func queuedReportKey(report QueuedReport) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s\x00%s", report.ProfileEmail, report.UserID, report.SessionID, report.PID, report.ProcStart, report.IOEpoch)
}

func removeExpired(reports []QueuedReport, cutoff time.Time) []QueuedReport {
	kept := reports[:0]
	for _, report := range reports {
		if !report.Report.AsOf.Before(cutoff) {
			kept = append(kept, report)
		}
	}
	return kept
}
