package profile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"cctrace/internal/atomicfile"

	"github.com/gofrs/flock"
)

var ErrSelectionConflict = errors.New("profile selection revision conflict")

// Selection is the next profile choice for each supported agent. It contains
// names and a revision only; home paths and credential data stay in profiles.
type Selection struct {
	Version  int    `json:"version"`
	Revision uint64 `json:"revision"`
	Claude   string `json:"claude,omitempty"`
	Codex    string `json:"codex,omitempty"`
}

func SelectionPath() string { return filepath.Join(DefaultDir(), "next-profile.json") }

// ReadSelection reads a selection file. A missing file means the agent has no
// pending selection; callers resolve that state to the explicit default.
func ReadSelection(path string) (*Selection, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Selection{Version: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 8<<10 {
		return nil, errors.New("profile selection must be a small regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var selection Selection
	if err := dec.Decode(&selection); err != nil {
		return nil, fmt.Errorf("profile selection: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("profile selection has trailing data")
	}
	if selection.Version != 1 || !validSelectedName(selection.Claude) || !validSelectedName(selection.Codex) {
		return nil, errors.New("profile selection has an unsupported version or name")
	}
	return &selection, nil
}

// UpdateSelection serializes writers with a file lock, checks an optional
// expected revision, then atomically replaces the selection at mode 0600.
func UpdateSelection(path, agent, name string, expectedRevision *uint64) (*Selection, error) {
	if agent != "claude" && agent != "codex" {
		return nil, fmt.Errorf("unsupported agent %q", agent)
	}
	if !validSelectedName(name) {
		return nil, fmt.Errorf("invalid selected profile name %q", name)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create selection directory: %w", err)
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return nil, fmt.Errorf("lock profile selection: %w", err)
	}
	defer lock.Unlock() //nolint:errcheck

	selection, err := ReadSelection(path)
	if err != nil {
		return nil, err
	}
	if expectedRevision != nil && selection.Revision != *expectedRevision {
		return nil, ErrSelectionConflict
	}
	if selection.Revision == ^uint64(0) {
		return nil, errors.New("profile selection revision exhausted")
	}
	selection.Version = 1
	selection.Revision++
	if agent == "claude" {
		selection.Claude = name
	} else {
		selection.Codex = name
	}
	data, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := atomicfile.Write(path, data, 0600); err != nil {
		return nil, fmt.Errorf("write profile selection: %w", err)
	}
	verified, err := ReadSelection(path)
	if err != nil {
		return nil, fmt.Errorf("verify profile selection: %w", err)
	}
	if verified.Revision != selection.Revision || verified.Claude != selection.Claude || verified.Codex != selection.Codex {
		return nil, errors.New("profile selection changed while verifying write")
	}
	return verified, nil
}

func validSelectedName(name string) bool {
	return name == "" || name == "default" || ValidateName(name) == nil
}
