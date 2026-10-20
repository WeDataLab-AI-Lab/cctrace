package syncer

import "slices"

// Adoption says what AdoptSource takes from the previous holder's state for one
// source, and how it is named in the new one.
type Adoption struct {
	// Path maps a file key of the previous state to its key in the new one, and
	// says false for a file that is not part of the source. The two states spell
	// the source's folder differently, or hold the source under folders that only
	// partly overlap; the caller knows how.
	Path func(prev string) (string, bool)
	// Home maps the home of an account observation of the previous state to the
	// new state's name for the same home, false for another source's.
	Home func(prev string) (string, bool)
	// DeclineRebuild: the previous holder collected under a repository allowlist,
	// or may have. What it consumed unsent left no mark, so the part of a Claude
	// transcript the cursor passed cannot vouch for a snapshot rebuilt from it.
	DeclineRebuild bool
}

// AdoptSource continues the previous holder's reading of a source in s: it takes
// the cursor of each file of the source from prev, so reading goes on from where
// the previous holder stopped, with the same ledger of record keys, the same
// account history and the same files kept back. It returns how many files it took.
//
// An entry of prev replaces one of s for the same file: it is the newer. What is
// about the previous holder's own sending -- its failures, the server's answers to
// it, its caches of repositories -- stays behind. Only the entries are taken, as
// copies; prev is not changed and is not to be saved. The caller saves s.
func (s *State) AdoptSource(prev *State, a Adoption) int {
	adopted := 0
	for key, fs := range prev.Files {
		to, ok := a.Path(key)
		if !ok || fs == nil {
			continue
		}
		c := *fs
		if fs.ConflictTail != nil {
			tail := *fs.ConflictTail
			c.ConflictTail = &tail
		}
		if a.DeclineRebuild {
			c.RebuildDeclined = true
		}
		if s.Files == nil {
			s.Files = make(map[string]*FileState)
		}
		s.Files[to] = &c
		adopted++
	}
	for key, taints := range prev.Taints {
		if to, ok := a.Path(key); ok {
			if s.Taints == nil {
				s.Taints = make(map[string][]Taint)
			}
			s.Taints[to] = slices.Clone(taints)
		}
	}
	if a.Home != nil {
		s.mergeObservations(prev, a.Home)
	}
	return adopted
}

// mergeObservations adds the previous state's account observations of the source's
// home, under the new name for it, and keeps the log in time order -- the lookups
// read it as one.
func (s *State) mergeObservations(prev *State, home func(string) (string, bool)) {
	for _, o := range prev.ClaudeAccountObservations {
		if h, ok := home(o.Home); ok {
			o.Home = h
			if !slices.ContainsFunc(s.ClaudeAccountObservations, func(x ClaudeAccountObservation) bool {
				return x.Home == o.Home && x.AccountUUID == o.AccountUUID && x.ObservedAt.Equal(o.ObservedAt)
			}) {
				s.ClaudeAccountObservations = append(s.ClaudeAccountObservations, o)
			}
		}
	}
	for _, o := range prev.CodexAccountObservations {
		if h, ok := home(o.Home); ok {
			o.Home = h
			if !slices.ContainsFunc(s.CodexAccountObservations, func(x CodexAccountObservation) bool {
				return x.Home == o.Home && x.AccountID == o.AccountID && x.ObservedAt.Equal(o.ObservedAt)
			}) {
				s.CodexAccountObservations = append(s.CodexAccountObservations, o)
			}
		}
	}
	slices.SortStableFunc(s.ClaudeAccountObservations, func(a, b ClaudeAccountObservation) int { return a.ObservedAt.Compare(b.ObservedAt) })
	slices.SortStableFunc(s.CodexAccountObservations, func(a, b CodexAccountObservation) int { return a.ObservedAt.Compare(b.ObservedAt) })
}
