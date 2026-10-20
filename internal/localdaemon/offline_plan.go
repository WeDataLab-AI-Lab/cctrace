package localdaemon

import (
	"errors"
	"os"
	"slices"
	"strings"
	"time"
)

// PlanOffline answers who reads what while no daemon runs: the plan the daemon
// would make from the routes file as it stands. It changes nothing, so a line
// nobody has reserved goes to the first profile by ID in memory only. A routes
// file that cannot be used closes every line that overlaps another profile's, as
// the daemon does.
func PlanOffline(root string, specs []Spec) PlanResult {
	file, err := ReadRoutes(root)
	in := PlanInput{Routes: file, Valid: err == nil, Now: time.Now().UTC()}
	if errors.Is(err, os.ErrNotExist) {
		in.Routes, in.Valid = RouteFile{Version: routesVersion}, true
	}
	for _, sp := range specs {
		in.Profiles = append(in.Profiles, planProfileOf(sp, false))
	}
	return Plan(in)
}

// Job is the plan of the profile with this registry ID.
func (r PlanResult) Job(profileID string) (JobPlan, bool) {
	job, ok := r.Jobs[routeIdentity(profileID)]
	return job, ok
}

// SourceShape says how two source lines ("kind:folder") overlap, as Plan judges
// it: "" when their kinds differ or the folders their collectors read do not
// overlap, ShareSame when those folders are the same, ShareNested otherwise. A
// run outside the daemon uses it to find a folder among the lines of a decision
// however the folder is spelled: a link, a trailing "/.", another case.
func SourceShape(a, b string) string {
	ka, fa, _ := strings.Cut(a, ":")
	kb, fb, _ := strings.Cut(b, ":")
	if ka != kb {
		return ""
	}
	v := newFSView()
	x, y := v.roots(ka, fa), v.roots(kb, fb)
	if x == nil || y == nil {
		return ""
	}
	return v.shape(x, y)
}

// SourceLockID is the line lock a run outside the daemon takes for a line no
// route covers yet ("kind:folder", sent to destination): the id the daemon gives
// the route it makes for that source, so that the job that reads the line once it
// is reserved waits for the run, and two such runs do not read it at once. It is
// "" for a line no route can be made for.
func SourceLockID(line, destination string) string {
	kind, folder, _ := strings.Cut(line, ":")
	roots, destination := ScanRoots(kind, folder), routeDestination(destination)
	if roots == nil || !isHex(destination, 64) {
		return ""
	}
	return sourceID(kind, destination, roots)
}

// ConfirmUnreserved is ConfirmRoute for a line a run locked by its SourceLockID.
// The line is the profile's to read while no route goes by that id -- a routes
// file that is missing or cannot be used reserves nothing -- or while the route
// that does is the profile's, as ConfirmRoute says.
func ConfirmUnreserved(root, id, owner string) error {
	f, err := ReadRoutes(root)
	if err != nil || !slices.ContainsFunc(f.Routes, func(r Route) bool { return r.ID == id }) {
		return nil
	}
	_, err = ConfirmRoute(root, id, owner)
	return err
}
