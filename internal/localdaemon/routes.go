package localdaemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"cctrace/internal/atomicfile"
	"cctrace/internal/profile"
)

const (
	routesFileName = "daemon-routes.json"
	routesVersion  = 1
	maxRoutes      = 4096
	maxAliases     = 4
	// maxSinceAhead is how far in the future a route may be dated: no further than
	// clocks that disagree a little can explain.
	maxSinceAhead = 24 * time.Hour
)

// States of a route. Active: the owner reads the line. Releasing: the owner is
// letting go of it and nobody reads it until the next owner takes it. Unowned:
// nobody reads it (a tombstone, or a file that could not name the owner).
const (
	RouteActive    = "active"
	RouteReleasing = "releasing"
	RouteUnowned   = "unowned"
)

// Route records which profile reads one source: the line folder of a kind, for
// one destination. Another profile's line that overlaps it is left out until the
// owner changes.
type Route struct {
	ID   string `json:"id"`
	Rev  int    `json:"rev"`
	Kind string `json:"kind"`
	// Path is the canonical folder of the line that made the route. It is kept
	// in this file only; snapshots and logs name the route by ID.
	Path string `json:"path"`
	// Destination is the owner's current destination hash. Aliases are the ones
	// it had before a policy moved it: a profile still on one is held back.
	Destination string   `json:"destination"`
	Aliases     []string `json:"aliases,omitempty"`
	// Owner is a profile ID; "" is unowned.
	Owner string `json:"owner"`
	State string `json:"state"`
	// To is the next owner of a releasing route; "" leaves it unowned after.
	To string `json:"to,omitempty"`
	// From and Handoff are set while the owner has changed and the new owner has
	// not yet taken over the old owner's cursor.
	From    string    `json:"from,omitempty"`
	Handoff string    `json:"handoff,omitempty"`
	Since   time.Time `json:"since"`
}

// RouteFile is the content of daemon-routes.json.
type RouteFile struct {
	Version int `json:"version"`
	// Rev counts writes of the file, so a writer that is told its write failed
	// can find out whether it happened.
	Rev    int     `json:"rev"`
	Routes []Route `json:"routes"`
}

// InvalidRoutesError says why daemon-routes.json cannot be used. Reason is a
// short stable word fit for a log line; it never carries a path.
type InvalidRoutesError struct {
	Reason string
	// Row is the index of the offending route, -1 when the file as a whole is.
	Row int
	// Err is what failed to read or parse the file, if that is the reason.
	Err error
}

func (e *InvalidRoutesError) Error() string {
	if e.Row >= 0 {
		return "daemon routes invalid: " + e.Reason + " (route " + strconv.Itoa(e.Row) + ")"
	}
	return "daemon routes invalid: " + e.Reason
}

func (e *InvalidRoutesError) Unwrap() error { return e.Err }

func invalidRoutes(reason string, row int) error {
	return &InvalidRoutesError{Reason: reason, Row: row}
}

// ErrRoutesWriteUncertain is returned when a write failed and the file that is
// on disk afterwards could not tell whether it took effect. Nothing may be
// decided from it until the file has been read again.
var ErrRoutesWriteUncertain = errors.New("daemon routes write outcome uncertain")

// atomicWriteRoutes is a seam: atomicfile.Write can fail after it has replaced
// the file, which tests need to produce.
var atomicWriteRoutes = atomicfile.Write

var routeKinds = []string{"claude", "codex", "gjc", "omo"}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// validIdentity accepts "", "default" and "named:" plus a profile name.
func validIdentity(id string) bool {
	if id == "" || id == "default" {
		return true
	}
	name, ok := strings.CutPrefix(id, "named:")
	return ok && profile.ValidateName(name) == nil
}

// classes returns the destinations a route holds against: its own and its aliases.
func (r Route) classes() []string {
	all := append([]string{r.Destination}, r.Aliases...)
	slices.Sort(all)
	return slices.Compact(all)
}

func (r Route) invalidReason(now time.Time) string {
	switch {
	case r.ID == "" || r.Kind == "" || r.Path == "" || r.Destination == "" || r.State == "" || r.Since.IsZero():
		return "missing_field"
	case !isHex(r.ID, 8):
		return "id"
	case !slices.Contains(routeKinds, r.Kind):
		return "kind"
	case !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path:
		return "path"
	case !isHex(r.Destination, 64):
		return "destination"
	case len(r.Aliases) > maxAliases || slices.ContainsFunc(r.Aliases, func(a string) bool { return !isHex(a, 64) }):
		return "aliases"
	case !validIdentity(r.Owner) || !validIdentity(r.To) || !validIdentity(r.From):
		return "identity"
	case r.State != RouteActive && r.State != RouteReleasing && r.State != RouteUnowned:
		return "state"
	case r.State == RouteReleasing && r.Owner == "", r.State == RouteUnowned && r.Owner != "", r.To != "" && r.State != RouteReleasing:
		return "state"
	case r.Handoff != "" && (!isHex(r.Handoff, 32) || r.State != RouteActive || r.Owner == ""):
		return "handoff"
	case r.Since.After(now.Add(maxSinceAhead)):
		return "since"
	}
	return ""
}

// ValidateRoutes checks every rule the file must keep. A file that breaks one
// is not interpreted at all: guessing which routes are sound would let a damaged
// file decide who reads a source.
func ValidateRoutes(f RouteFile, now time.Time) error {
	if f.Version != routesVersion {
		return invalidRoutes("version", -1)
	}
	if len(f.Routes) > maxRoutes {
		return invalidRoutes("size", -1)
	}
	ids := make(map[string]bool, len(f.Routes))
	for i, r := range f.Routes {
		if reason := r.invalidReason(now); reason != "" {
			return invalidRoutes(reason, i)
		}
		if ids[r.ID] {
			return invalidRoutes("duplicate_id", i)
		}
		ids[r.ID] = true
	}
	if i := conflictingRoute(f.Routes); i >= 0 {
		return invalidRoutes("conflict", i)
	}
	return nil
}

// conflictingRoute returns the index of a route that overlaps an earlier one of
// the same kind under a destination they both hold against, or -1. A releasing
// route may overlap another: nobody reads it, and a route that loses a collision
// stands that way until its owner has stopped reading. Only routes sharing a
// destination are compared, so a large file of distinct destinations costs
// nothing.
func conflictingRoute(routes []Route) int {
	groups := map[string][]int{}
	for i, r := range routes {
		for _, d := range r.classes() {
			groups[d] = append(groups[d], i)
		}
	}
	view := newFSView()
	roots := map[int][]string{}
	rootsOf := func(i int) []string {
		if _, ok := roots[i]; !ok {
			roots[i] = view.roots(routes[i].Kind, routes[i].Path)
		}
		return roots[i]
	}
	found := -1
	for _, d := range slices.Sorted(maps.Keys(groups)) {
		idxs := groups[d]
		for b := 1; b < len(idxs); b++ {
			for a := 0; a < b; a++ {
				later := max(idxs[a], idxs[b])
				if found >= 0 && later >= found {
					continue
				}
				ra, rb := routes[idxs[a]], routes[idxs[b]]
				if ra.Kind == rb.Kind && ra.State != RouteReleasing && rb.State != RouteReleasing && view.shape(rootsOf(idxs[a]), rootsOf(idxs[b])) != "" {
					found = later
				}
			}
		}
	}
	return found
}

// ReadRoutes reads and validates <root>/daemon-routes.json. A file that does not
// exist reads as os.ErrNotExist (the first start). Any other trouble is an
// *InvalidRoutesError, and the caller must then treat every owner as unknown.
func ReadRoutes(root string) (RouteFile, error) {
	path := filepath.Join(root, routesFileName)
	var f RouteFile
	err := ReadJSON(path, &f)
	if errors.Is(err, ErrMetadataChanged) {
		// The daemon replaced the file while it was being read.
		f = RouteFile{}
		err = ReadJSON(path, &f)
	}
	if errors.Is(err, os.ErrNotExist) {
		return RouteFile{}, err
	}
	if err != nil {
		reason := "unreadable"
		var syntax *json.SyntaxError
		var kind *json.UnmarshalTypeError
		if errors.As(err, &syntax) || errors.As(err, &kind) {
			reason = "format"
		}
		return RouteFile{}, &InvalidRoutesError{Reason: reason, Row: -1, Err: err}
	}
	if err := ValidateRoutes(f, time.Now()); err != nil {
		return RouteFile{}, err
	}
	return f, nil
}

// WriteRoutes writes f as the next revision of the file and returns what it
// wrote. It refuses a file that ReadRoutes would refuse, so the daemon cannot
// write itself into unknown-owner mode.
//
// A failed write is not necessarily one that did not happen: the replace can
// succeed and the directory sync after it fail. So after a failure the file is
// read again. If it is the file just written, the write took effect and counts
// as a success. If it is the file before, the write did not happen and the
// caller gets the failure with f unchanged. Anything else is
// ErrRoutesWriteUncertain, and the caller decides nothing until it has read the
// file again.
func WriteRoutes(root string, f RouteFile) (RouteFile, error) {
	next := f
	next.Version = routesVersion
	next.Rev = f.Rev + 1
	if err := ValidateRoutes(next, time.Now()); err != nil {
		return f, err
	}
	data, err := json.Marshal(next)
	if err != nil {
		return f, err
	}
	if len(data) > maxInput {
		// ReadJSON would refuse this file when it is read back.
		return f, invalidRoutes("size", -1)
	}
	err = atomicWriteRoutes(filepath.Join(root, routesFileName), data, 0600)
	if err == nil {
		return next, nil
	}
	got, readErr := ReadRoutes(root)
	switch {
	case readErr == nil && got.Rev == next.Rev:
		if again, e := json.Marshal(got); e == nil && bytes.Equal(again, data) {
			return next, nil
		}
	case readErr == nil && got.Rev == f.Rev, errors.Is(readErr, os.ErrNotExist) && f.Rev == 0:
		return f, err
	}
	return f, fmt.Errorf("%w: %w", ErrRoutesWriteUncertain, err)
}
