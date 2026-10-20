package localdaemon

import (
	"bytes"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
)

const allocationsFileName = "daemon-allocations.json"

// AllocationsFile is what the daemon says each of its jobs reads and does not.
// Other processes that read sources -- a one-shot sync, a re-enrichment -- follow
// it while the daemon runs, so that they apply the daemon's decision exactly as
// its jobs do, including any setting the daemon was started with that they do
// not have. It is the daemon's own file: a reader uses it only while the daemon
// of InstanceID is running.
type AllocationsFile struct {
	InstanceID string                   `json:"instance_id"`
	RoutesRev  int                      `json:"routes_rev"`
	Jobs       map[string]JobAllocation `json:"jobs"`
}

// JobAllocation is one profile's share, by profile ID.
type JobAllocation struct {
	Revision string         `json:"revision"`
	Allowed  []string       `json:"allowed"`
	Excluded []ExcludedLine `json:"excluded,omitempty"`
	// Routes are the routes behind each allowed line, by line: what a run outside
	// the daemon locks for the lines it reads.
	Routes map[string][]string `json:"routes,omitempty"`
}

// ExcludedLine is a line the profile does not read, and the route that holds it.
// Route is empty when no route is known: the routes file could not be used, or
// the route exists only in a plan that saves nothing.
type ExcludedLine struct {
	Line  string `json:"line"`
	Route string `json:"route"`
}

// ReadAllocations reads the daemon's allocations file. A file that does not exist
// reads as os.ErrNotExist.
func ReadAllocations(root string) (AllocationsFile, error) {
	var f AllocationsFile
	err := ReadJSON(filepath.Join(root, allocationsFileName), &f)
	return f, err
}

// writeAllocations records the jobs' allocations when they differ from what was
// last recorded.
func (s *Supervisor) writeAllocations() {
	file := AllocationsFile{InstanceID: s.runtime.InstanceID, RoutesRev: s.routes.file.Rev, Jobs: map[string]JobAllocation{}}
	for _, id := range s.ids() {
		j := s.jobs[id]
		if j.gone || j.alloc == nil {
			continue
		}
		ja := JobAllocation{Revision: j.spec.Revision, Allowed: slices.Clone(j.alloc.Allowed), Excluded: slices.Clone(j.plan.ExcludedLines), Routes: maps.Clone(j.alloc.LineRoutes)}
		if ja.Allowed == nil {
			ja.Allowed = []string{}
		}
		file.Jobs[id] = ja
	}
	data, err := json.Marshal(file)
	if err != nil || bytes.Equal(data, s.routes.allocations) {
		return
	}
	if err := writeJSON(filepath.Join(s.config.Root, allocationsFileName), file); err == nil {
		s.routes.allocations = data
	}
}
