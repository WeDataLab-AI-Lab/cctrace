package syncer

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"cctrace/internal/atomicfile"
)

// TransferEvent contains only bounded transport metadata, never request identities.
type TransferEvent struct {
	Sequence             uint64    `json:"sequence"`
	At                   time.Time `json:"at"`
	Provider             string    `json:"provider"`
	ClientVersion        string    `json:"client_version"`
	RequestBytes         int       `json:"request_bytes"`
	RecordCount          int       `json:"record_count"`
	HTTPStatus           int       `json:"http_status,omitempty"`
	Failure              string    `json:"failure,omitempty"`
	DurationMS           int64     `json:"duration_ms"`
	DeadlineMS           int64     `json:"deadline_ms"`
	RetryAfterMS         int64     `json:"retry_after_ms,omitempty"`
	SplitDepth           int       `json:"split_depth"`
	Outcome              string    `json:"outcome"`
	ServerBodyLimitKnown bool      `json:"server_body_limit_known"`
}
type TransferDiagnostics struct {
	SchemaVersion int             `json:"schema_version"`
	LastFailure   *TransferEvent  `json:"last_failure,omitempty"`
	Events        []TransferEvent `json:"events"`
}
type transferRecorder struct {
	mu   sync.Mutex
	path string
	data TransferDiagnostics
}

var safeTransferVersion = regexp.MustCompile(`^(dev|unknown|v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]{1,48})?)$`)
var errTransferHistory = errors.New("invalid transfer diagnostics history")

func safeTransferPath(path string) bool {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err == nil && (st.Mode()&os.ModeSymlink != 0 || (p == path && !st.Mode().IsRegular())) {
			return false
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false
		}
		if p == filepath.Dir(p) {
			return true
		}
	}
}
func uniqueJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	keys := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return errTransferHistory
			}
			keys[name] = true
		}
		if err := uniqueJSON(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}
func ReadTransferDiagnostics(path string) (TransferDiagnostics, error) {
	var d TransferDiagnostics
	if !safeTransferPath(path) {
		return d, errTransferHistory
	}
	f, err := os.Open(path)
	if err != nil {
		return d, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil {
		return d, err
	}
	if len(body) > 64*1024 {
		return d, errTransferHistory
	}
	if err = uniqueJSON(json.NewDecoder(bytes.NewReader(body))); err != nil {
		return d, errTransferHistory
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&d); err != nil {
		return TransferDiagnostics{}, errTransferHistory
	}
	if decoder.Decode(new(any)) != io.EOF || d.SchemaVersion != 1 || d.Events == nil || len(d.Events) > 32 {
		return TransferDiagnostics{}, errTransferHistory
	}
	var sequence uint64
	validateEvents := append([]TransferEvent{}, d.Events...)
	if d.LastFailure != nil {
		if d.LastFailure.Outcome == "accepted" || (len(d.Events) > 0 && d.LastFailure.Sequence > d.Events[len(d.Events)-1].Sequence) {
			return TransferDiagnostics{}, errTransferHistory
		}
		validateEvents = append(validateEvents, *d.LastFailure)
	}
	for i, e := range validateEvents {
		if i == len(d.Events) {
			sequence = 0
		}
		if e.Sequence <= sequence || e.At.IsZero() || e.At.After(time.Now().Add(time.Minute)) || transferProvider(e.Provider) != e.Provider || (len(e.ClientVersion) > 64 || !safeTransferVersion.MatchString(e.ClientVersion)) || e.RequestBytes < 1 || e.RecordCount < 0 || e.DurationMS < 0 || e.DurationMS > 86400000 || e.DeadlineMS < 0 || e.DeadlineMS > int64(maxTransferDeadline/time.Millisecond) || e.RetryAfterMS < 0 || e.RetryAfterMS > 86400000 || e.SplitDepth < 0 || e.SplitDepth > 64 || e.ServerBodyLimitKnown || (e.HTTPStatus != 0 && (e.HTTPStatus < 100 || e.HTTPStatus > 599)) {
			return TransferDiagnostics{}, errTransferHistory
		}
		switch e.Failure {
		case "", "timeout", "canceled", "dns", "tls", "connect", "network", "request", "response", "encode":
		default:
			return TransferDiagnostics{}, errTransferHistory
		}
		switch e.Outcome {
		case "accepted", "body_too_large", "rate_limited", "auth_error", "http_error", "network_error", "response_error":
		default:
			return TransferDiagnostics{}, errTransferHistory
		}
		sequence = e.Sequence
	}
	return d, nil
}
func (c *Client) SetTransferDiagnostics(path string) error {
	if !safeTransferPath(path) {
		c.diagnostics = nil
		return nil
	}
	d, err := ReadTransferDiagnostics(path)
	if errors.Is(err, os.ErrNotExist) {
		d = TransferDiagnostics{SchemaVersion: 1, Events: []TransferEvent{}}
	} else if err != nil {
		d = TransferDiagnostics{SchemaVersion: 1, Events: []TransferEvent{}}
	}
	c.diagnostics = &transferRecorder{path: path, data: d}
	return nil
}
func (r *transferRecorder) record(e TransferEvent) {
	if r == nil {
		return
	}
	if !safeTransferPath(r.path) {
		return
	}
	if len(e.ClientVersion) > 64 || !safeTransferVersion.MatchString(e.ClientVersion) {
		e.ClientVersion = "unknown"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.data.Events); n > 0 {
		e.Sequence = r.data.Events[n-1].Sequence + 1
	} else {
		e.Sequence = 1
	}
	if e.Outcome != "accepted" {
		copyEvent := e
		r.data.LastFailure = &copyEvent
	}
	r.data.Events = append(r.data.Events, e)
	if len(r.data.Events) > 32 {
		r.data.Events = r.data.Events[len(r.data.Events)-32:]
	}
	body, err := json.Marshal(r.data)
	if err != nil {
		return
	}
	if err = os.MkdirAll(filepath.Dir(r.path), 0700); err != nil {
		return
	}
	_ = atomicfile.Write(r.path, body, 0600)
}
func transferProvider(agent string) string {
	switch agent {
	case "claude", "codex", "gjc", "omo":
		return agent
	default:
		return "unknown"
	}
}

type splitDepthKey struct{}
