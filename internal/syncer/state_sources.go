package syncer

import (
	"encoding/json"
	"os"
	"slices"
	"time"
)

// How a source folder came to be read through a state.
const (
	// SourceInitial: the folder was in the profile's sources when the state first
	// saw it, or the state was made for it. Nothing about it needed arranging.
	SourceInitial = "initial"
	// SourceJoining and SourceJoined: the folder was handed to this profile by
	// another one whose cursor is not available. What was on disk at Since is
	// skipped, what appeared after is read; joining is the step in between, kept so
	// a restart finishes it with the same Since.
	SourceJoining = "joining"
	SourceJoined  = "joined"
	// SourceAdopted: the folder came with the previous holder's cursor.
	SourceAdopted = "adopted"
)

// SourceMark is what a state remembers of one source folder's arrival.
type SourceMark struct {
	Since time.Time `json:"since"`
	Via   string    `json:"via"`
	// From is the profile whose cursor was taken over, and Handoff names the
	// hand-over; a mark that already carries the hand-over of the moment means the
	// work was done and is not repeated.
	From    string `json:"from,omitempty"`
	Handoff string `json:"handoff,omitempty"`
	// AfterBlock: the profile had been held back from this folder by another
	// profile, and its state was made only when that ended. The sessions of the
	// blocked time are not collected, and `cctrace status` says so.
	AfterBlock bool `json:"after_block,omitempty"`
}

// StateOrigin says what a state file was.
type StateOrigin int

const (
	// StateMissing: there was no file.
	StateMissing StateOrigin = iota
	// StateCorrupt: there was a file and it could not be read as a state.
	StateCorrupt
	// StateValid: it was a state, however few entries it held.
	StateValid
)

// LoadStateStrict reads a state as LoadState does, and says what it found. A
// missing and a damaged file both give an empty new state, but only a missing one
// is nothing to worry about: a damaged one may have held the cursor of a source
// being handed over, and the hand-over must not pretend it adopted it. A file
// that cannot be read at all is an error.
func LoadStateStrict(path string) (*State, StateOrigin, error) {
	s := &State{Files: make(map[string]*FileState), path: path}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		s.isNew = true
		return s, StateMissing, nil
	}
	if err != nil {
		return nil, StateMissing, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		// As LoadState: treated as new, so historical sessions are not re-sent.
		s = &State{Files: make(map[string]*FileState), path: path, isNew: true}
		return s, StateCorrupt, nil
	}
	s.path = path
	if s.Files == nil {
		s.Files = make(map[string]*FileState)
	}
	return s, StateValid, nil
}

// SourceMark returns the mark of a source folder.
func (s *State) SourceMark(dir string) (SourceMark, bool) {
	m, ok := s.Sources[dir]
	return m, ok
}

// MarkSource sets the mark of a source folder. It does not save.
func (s *State) MarkSource(dir string, m SourceMark) {
	if s.Sources == nil {
		s.Sources = make(map[string]SourceMark)
	}
	s.Sources[dir] = m
}

// ForgetSourcesExcept drops the marks of the folders not in keep: the state no
// longer reads them. Their cursors stay, so that the folder coming back reads on
// from where it was.
func (s *State) ForgetSourcesExcept(keep []string) {
	for dir := range s.Sources {
		if !slices.Contains(keep, dir) {
			delete(s.Sources, dir)
		}
	}
	if len(s.Sources) == 0 {
		s.Sources = nil
	}
}
