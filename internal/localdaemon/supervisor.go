package localdaemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

type Runner func(context.Context, func(Progress)) error

// maxReplyHolders caps the holder names a refused control request carries.
const maxReplyHolders = 8

type Spec struct {
	ID, Name, RuntimeDir, Revision, Destination string
	Enabled                                     bool
	Sources                                     []string
	// SourceLabels says, per Sources entry, where it came from (home, default,
	// env, configured, explicit); it only labels warnings.
	SourceLabels map[string]string
	// Filters are the settings that decide what the profile sends. A source moves
	// to another profile on its own only if that profile's filters send no more.
	Filters Filters
	// LocalAnalysis: the profile analyses sessions locally, so it keeps running
	// when every upload source is held by others.
	LocalAnalysis bool
	Run           Runner
	Acquire       func(context.Context, Allocation) (func(), error)
	// TookOver, when set, says which hand-overs of its allocation the last
	// Acquire took the previous holder's cursor for. One it did not take -- its
	// folder could not be read just then -- stays on the route and comes with
	// the next lease. Unset, a lease takes every hand-over it is given.
	TookOver      func() []Handoff
	BlockedReason string
	Quiesce       func(context.Context) error
}

// Allocation is what one generation of a job may read: the Spec.Sources entries
// it is allowed, and the lines among them that have just been handed to it by
// another profile (Handoff).
type Allocation struct {
	Allowed []string
	// Routes are the ids of the routes behind Allowed. A generation holds their
	// line locks (LockLines) for as long as it reads, so that no other run reads
	// the same lines, and confirms them (ConfirmRoute) once it has them.
	Routes []string
	// LineRoutes are the routes behind each of Allowed, by line.
	LineRoutes map[string][]string
	// PeerPending are the runtime directories of the profiles left out of this
	// profile's Claude folders, whose queues of session ends this profile sends.
	PeerPending []string
	Handoffs    []Handoff
	// AfterBlock: the profile was held back by an overlap in the snapshot this
	// daemon took over from. On the lease where it gets its state made anew, the
	// sessions of the blocked time are not collected.
	AfterBlock bool
}

type Progress struct {
	State        string
	Sent         int
	BlockedFiles int64
	ErrorCode    string
	RetryAfter   time.Duration
}

type Config struct {
	Root, Version     string
	Interval, Refresh time.Duration
	Load              func() ([]Spec, error)
	// Logf, when set, receives the daemon's one-line diagnostics.
	Logf func(format string, args ...any)
}

type job struct {
	spec     Spec
	status   JobStatus
	cancel   context.CancelFunc
	active   bool
	token    string
	next     time.Time
	failures int
	pending  []pendingControl
	release  func()
	reset    bool
	quiesced bool
	// gone: the profile has left the registry while its job was still running.
	gone bool
	// alloc is what the latest plan lets the next generation read; nil until a
	// plan has been made and kept. plan is the job's share of that plan.
	alloc *Allocation
	plan  JobPlan
	// afterBlock: the daemon found this profile held back in the snapshot it took
	// over from, and its next lease has not been taken yet.
	afterBlock bool
	// held is what the job's lease, or its run if it has no lease, was made for.
	// It is the answer to "does this job still read that line?" -- not whether a
	// pass is running, since a job between passes keeps its lease.
	held *Allocation
}

type pendingControl struct{ path, action string }
type event struct {
	id, token string
	progress  *Progress
	exit      bool
	err       error
	release   func()
	quiesced  bool
	// leased: the job took its lease for a generation made of handoffs. The
	// profile has the previous owner's cursor, so the hand-over is complete.
	leased   bool
	handoffs []Handoff
	// busy: the job could not take its lease because a line is in use.
	busy bool
}

type Supervisor struct {
	config   Config
	runtime  Runtime
	jobs     map[string]*job
	stops    map[string]bool
	events   chan event
	complete bool
	previous map[string]JobStatus
	routes   routing
}

func New(c Config) *Supervisor {
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	if c.Refresh <= 0 {
		c.Refresh = time.Second
	}
	return &Supervisor{config: c, jobs: map[string]*job{}, stops: map[string]bool{}, events: make(chan event, 1024), complete: true, routes: routing{said: map[string]bool{}, names: map[string]string{}, back: map[string]string{}}}
}

func (s *Supervisor) Run(ctx context.Context, ready func(Runtime)) error {
	if s.config.Load == nil || !filepath.IsAbs(s.config.Root) {
		return errors.New("invalid daemon configuration")
	}
	if err := os.MkdirAll(s.config.Root, 0700); err != nil {
		return err
	}
	if err := safeDirectory(s.config.Root); err != nil {
		return err
	}
	lockPath := filepath.Join(s.config.Root, "daemon.lock")
	if i, err := os.Lstat(lockPath); err == nil && (!i.Mode().IsRegular() || i.Mode()&os.ModeSymlink != 0) {
		return errors.New("unsafe daemon lock")
	}
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return ErrAlreadyRunning
	}
	defer lock.Unlock()
	// The runtime file goes with the daemon, whether or not begin got as far as
	// writing it.
	defer os.Remove(filepath.Join(s.config.Root, "daemon-runtime.json"))
	if err := s.begin(); err != nil {
		return err
	}
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := s.publish(); err != nil {
		return err
	}
	if ready != nil {
		ready(s.runtime)
	}
	ticker := time.NewTicker(s.config.Refresh)
	defer ticker.Stop()
	var shutdown []string
	stopping := false
	ctxDone := ctx.Done()
	for {
		if stopping && s.activeCount() == 0 {
			for _, path := range shutdown {
				_ = writeJSON(path+".ack", commandReply{})
			}
			return s.publish()
		}
		select {
		case <-ctxDone:
			ctxDone = nil
			if !stopping {
				stopping = true
				cancel()
				s.stopAll()
			}
		case e := <-s.events:
			s.handleEvent(e, stopping)
		case <-ticker.C:
			if !stopping {
				if found := s.tick(jobCtx); len(found) > 0 {
					shutdown = append(shutdown, found...)
					stopping = true
					cancel()
					s.stopAll()
				}
			}
		}
		if err := s.publish(); err != nil {
			cancel()
			s.stopAll()
			for s.activeCount() > 0 {
				s.handleEvent(<-s.events, true)
			}
			return err
		}
	}
}

// begin prepares the daemon's state under the root it owns: the control
// directory, the stops and the previous snapshot it takes over from, its runtime
// file, and the first view of the profiles.
func (s *Supervisor) begin() error {
	dir := filepath.Join(s.config.Root, "daemon-control")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := safeDirectory(dir); err != nil {
		return err
	}
	if err := ReadJSON(filepath.Join(s.config.Root, "daemon-stops.json"), &s.stops); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	previous, err := ReadSnapshot(s.config.Root)
	unreadable := err != nil && !errors.Is(err, os.ErrNotExist)
	s.previous = map[string]JobStatus{}
	for _, job := range previous.Jobs {
		s.previous[job.ID] = job
	}
	s.runtime = Runtime{PID: os.Getpid(), InstanceID: randomID(), StartedAt: time.Now().UTC(), Version: s.config.Version}
	if err := writeJSON(filepath.Join(s.config.Root, "daemon-runtime.json"), s.runtime); err != nil {
		return err
	}
	s.reconcile()
	if unreadable {
		s.say("snapshot-unreadable")
	}
	s.initRoutes()
	s.route()
	for _, old := range previous.Jobs {
		if j := s.jobs[old.ID]; j != nil && j.status.ErrorCode != "duplicate_route" && old.ErrorCode == "rate_limited" && old.NextRetryAt.After(time.Now()) {
			j.next = old.NextRetryAt
			j.status.NextRetryAt = old.NextRetryAt
			j.status.ErrorCode = old.ErrorCode
			j.status.State = "retrying"
		}
	}
	return nil
}

// tick is one refresh: it re-reads the profiles, answers the control requests,
// and starts the jobs that may start. It returns the shutdown requests it found,
// in which case nothing is started.
func (s *Supervisor) tick(jobCtx context.Context) (shutdown []string) {
	s.reconcile()
	s.route()
	shutdown = s.commands()
	if len(shutdown) == 0 && s.complete {
		// A request can change who reads what; decide again before anything starts.
		s.route()
		for _, j := range s.jobs {
			if s.mayStart(j) {
				s.start(jobCtx, j)
			}
		}
	}
	return shutdown
}

func (s *Supervisor) reconcile() {
	specs, err := s.config.Load()
	if err != nil {
		s.complete = false
		s.stopAll()
		return
	}
	s.complete = true
	sort.SliceStable(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	seen := map[string]bool{}
	for _, spec := range specs {
		if spec.ID == "" || seen[spec.ID] || spec.Run == nil || !filepath.IsAbs(spec.RuntimeDir) {
			s.complete = false
			continue
		}
		seen[spec.ID] = true
		j := s.jobs[spec.ID]
		if j == nil {
			j = &job{spec: spec, status: JobStatus{ID: spec.ID, Name: spec.Name, Enabled: spec.Enabled, State: "unknown"}}
			j.status.RouteSignature = routeSignature(spec)
			s.jobs[spec.ID] = j
		}
		j.gone = false
		changed := j.spec.Revision != spec.Revision || j.spec.Enabled != spec.Enabled || j.spec.BlockedReason != spec.BlockedReason
		if changed && !j.active {
			s.release(j)
			if j.status.ErrorCode != "rate_limited" || j.spec.Destination != spec.Destination {
				j.next = time.Time{}
				j.status.NextRetryAt = time.Time{}
				j.status.ErrorCode = ""
				j.status.State = "unknown"
			}
		}
		if signature := routeSignature(spec); signature != j.status.RouteSignature {
			j.status.RouteSignature = signature
			j.status.ErrorCode = ""
		}
		if changed && j.active {
			j.reset = true
		}
		if changed {
			j.spec = spec
		}
		j.status.Name = spec.Name
		j.status.Enabled = spec.Enabled
		if changed && j.active {
			j.cancel()
			j.status.State = "stopping"
		}
		if !spec.Enabled {
			if j.active {
				j.cancel()
			} else {
				s.release(j)
				j.status.State = "disabled"
			}
		}
		if spec.BlockedReason != "" {
			if j.active {
				j.cancel()
			} else {
				s.release(j)
			}
			j.status.State = "error"
			j.status.ErrorCode = spec.BlockedReason
		}
	}
	for id, j := range s.jobs {
		if !seen[id] {
			j.gone = true
			if j.active {
				j.spec.Enabled = false
				j.cancel()
				j.status.State = "stopping"
			} else {
				s.release(j)
				delete(s.jobs, id)
			}
		}
	}
	for id, j := range s.jobs {
		if s.stops[id] && !j.active {
			s.release(j)
			j.status.State = "stopped"
		}
	}
}

func routesOverlap(a, b Spec) bool {
	return len(OverlappingSources(a, b)) > 0
}

// OverlappingSources returns the entries of a that overlap an entry of b: the
// same destination, the same kind, and one folder inside the other.
func OverlappingSources(a, b Spec) []string {
	if a.Destination == "" || a.Destination != b.Destination {
		return nil
	}
	var overlapping []string
	for _, x := range a.Sources {
		for _, y := range b.Sources {
			xp, xpath, ok := strings.Cut(x, ":")
			yp, ypath, yok := strings.Cut(y, ":")
			xpath, ypath = canonicalSource(xpath), canonicalSource(ypath)
			if ok && yok && xp == yp && (within(xpath, ypath) || within(ypath, xpath)) {
				overlapping = append(overlapping, x)
				break
			}
		}
	}
	return overlapping
}
func canonicalSource(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		return abs
	}
	return filepath.Clean(path)
}

func routeSignature(spec Spec) string {
	sources := make([]string, 0, len(spec.Sources))
	for _, source := range spec.Sources {
		kind, path, ok := strings.Cut(source, ":")
		if ok {
			source = kind + ":" + canonicalSource(path)
		}
		sources = append(sources, source)
	}
	sort.Strings(sources)
	sum := sha256.Sum256([]byte(spec.Destination + "\x00" + strings.Join(sources, "\x00")))
	return hex.EncodeToString(sum[:])
}
func within(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	// macOS may retain different case spellings for the same directory after
	// EvalSymlinks. Compare physical ancestors as well as lexical containment.
	anchor, err := os.Stat(a)
	if err != nil {
		return false
	}
	for current := b; ; current = filepath.Dir(current) {
		if info, err := os.Stat(current); err == nil && os.SameFile(anchor, info) {
			return true
		}
		if filepath.Dir(current) == current {
			return false
		}
	}
}
func (s *Supervisor) ids() []string {
	ids := make([]string, 0, len(s.jobs))
	for id := range s.jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func (s *Supervisor) activeCount() int {
	n := 0
	for _, j := range s.jobs {
		if j.active {
			n++
		}
	}
	return n
}
func (s *Supervisor) canStart(j *job) bool {
	if !j.active && !j.quiesced && j.spec.Quiesce != nil && !time.Now().Before(j.next) {
		return true
	}
	return j.spec.Enabled && j.spec.BlockedReason == "" && !s.stops[j.spec.ID] && !j.active && j.status.ErrorCode != "duplicate_route" && !time.Now().Before(j.next)
}

// mayStart is canStart for a job whose sources have been decided. Until a plan
// has been made and kept the job collects nothing: only its legacy quiesce may run.
func (s *Supervisor) mayStart(j *job) bool {
	if !s.canStart(j) {
		return false
	}
	return j.alloc != nil || (!j.quiesced && j.spec.Quiesce != nil)
}

func (s *Supervisor) start(parent context.Context, j *job) {
	ctx, cancel := context.WithCancel(parent)
	j.cancel = cancel
	j.active = true
	j.token = randomID()
	j.status.Generation++
	j.status.State = "collecting"
	spec, token := j.spec, j.token
	hasLease := j.release != nil
	needQuiesce := !j.quiesced && spec.Quiesce != nil
	canCollect := spec.Enabled && spec.BlockedReason == "" && !s.stops[spec.ID] && j.status.ErrorCode != "duplicate_route" && j.alloc != nil
	// A lease already held keeps the lines it was made for; a new one is made for
	// what the latest plan allows.
	var alloc Allocation
	switch {
	case hasLease && j.held != nil:
		alloc = *j.held
	case j.alloc != nil:
		alloc = *j.alloc
		j.held = &alloc
	}
	go func() {
		report := func(p Progress) {
			select {
			case s.events <- event{id: spec.ID, token: token, progress: &p}:
			case <-ctx.Done():
			}
		}
		if needQuiesce {
			if err := spec.Quiesce(ctx); err != nil {
				report(Progress{State: "error", ErrorCode: "migration_pending"})
				s.events <- event{id: spec.ID, token: token, exit: true, err: err}
				return
			}
			if !canCollect {
				s.events <- event{id: spec.ID, token: token, exit: true, quiesced: true}
				return
			}
		}
		var release func()
		if spec.Acquire != nil && !hasLease {
			var err error
			release, err = spec.Acquire(ctx, alloc)
			if errors.Is(err, ErrLineBusy) {
				// Another run still reads a line. That is no failure: the job tries
				// again at the next refresh.
				s.events <- event{id: spec.ID, token: token, exit: true, busy: true, err: err}
				return
			}
			if err != nil {
				code := "job_setup_failed"
				switch {
				case errors.Is(err, ErrMigrationPending) || errors.Is(err, context.DeadlineExceeded):
					code = "migration_pending"
				case errors.Is(err, ErrPolicyLockBusy):
					code = "policy_lock_busy"
				}
				report(Progress{State: "error", ErrorCode: code})
				s.events <- event{id: spec.ID, token: token, exit: true, err: err}
				return
			}
		}
		if !hasLease {
			took := alloc.Handoffs
			if spec.TookOver != nil {
				took = spec.TookOver()
			}
			if len(took) > 0 || alloc.AfterBlock {
				s.events <- event{id: spec.ID, token: token, leased: true, handoffs: took}
			}
		}
		err := spec.Run(ctx, report)
		s.events <- event{id: spec.ID, token: token, exit: true, err: err, release: release, quiesced: needQuiesce}
	}()
}
func (s *Supervisor) handleEvent(e event, stopping bool) {
	j := s.jobs[e.id]
	if j == nil || j.token != e.token {
		return
	}
	if e.leased {
		j.afterBlock = false
		s.adopt(j, e.handoffs)
		return
	}
	if p := e.progress; p != nil && !j.reset && !stopping {
		j.status.State = p.State
		j.status.Sent += int64(p.Sent)
		j.status.BlockedFiles = p.BlockedFiles
		j.status.ErrorCode = p.ErrorCode
		if p.State == "retrying" || p.State == "error" {
			j.failures++
			delay := time.Duration(j.failures*j.failures) * 5 * time.Second
			if delay > 5*time.Minute {
				delay = 5 * time.Minute
			}
			if p.RetryAfter > delay {
				delay = p.RetryAfter
			}
			j.next = time.Now().Add(delay)
			j.status.NextRetryAt = j.next
		} else {
			j.failures = 0
			j.status.NextRetryAt = time.Time{}
			j.next = time.Now().Add(s.config.Interval)
		}
		j.status.LastPassAt = time.Now().UTC()
	}
	if e.exit {
		if e.quiesced {
			j.quiesced = true
		}
		if e.release != nil {
			j.release = e.release
		}
		j.active = false
		j.cancel()
		if e.err != nil && !e.busy && !errors.Is(e.err, context.Canceled) {
			j.status.State = "error"
			if j.status.ErrorCode == "" {
				j.status.ErrorCode = "job_failed"
			}
			floor := time.Now().Add(5 * time.Second)
			if floor.After(j.next) {
				j.next = floor
			}
			j.status.NextRetryAt = j.next
		}
		if j.reset {
			s.release(j)
			j.reset = false
			if j.status.ErrorCode == "rate_limited" && time.Now().Before(j.next) {
				j.status.State = "retrying"
			} else {
				j.next = time.Time{}
				j.status.NextRetryAt = time.Time{}
				j.status.State = "unknown"
			}
		}
		if stopping || s.stops[e.id] || !j.spec.Enabled {
			s.release(j)
			j.status.State = "stopped"
		} else if j.status.State == "stopping" || j.status.State == "collecting" {
			j.status.State = "unknown"
			j.next = time.Time{}
		}
		if j.spec.BlockedReason != "" {
			s.release(j)
			j.status.State = "error"
			j.status.ErrorCode = j.spec.BlockedReason
		}
		if j.status.ErrorCode == "duplicate_route" {
			s.release(j)
			j.status.State = "blocked"
		}
		for _, pending := range j.pending {
			if pending.action == "restart" || pending.action == "start" {
				s.release(j)
				j.next = time.Time{}
				j.status.State = "unknown"
				j.status.ErrorCode = ""
			}
			_ = writeJSON(pending.path+".ack", commandReply{})
		}
		j.pending = nil
		if j.release == nil {
			j.held = nil
		}
	}
}
func (s *Supervisor) stopAll() {
	for _, j := range s.jobs {
		if j.active {
			j.cancel()
			j.status.State = "stopping"
		} else {
			s.release(j)
			j.status.State = "stopped"
		}
	}
}
func (s *Supervisor) release(j *job) {
	if j.release != nil {
		j.release()
		j.release = nil
	}
	if !j.active {
		j.held = nil
	}
}
func (s *Supervisor) publish() error {
	snap := Snapshot{Runtime: s.runtime, ObservedAt: time.Now().UTC(), Complete: s.complete, Jobs: []JobStatus{}}
	for _, id := range s.ids() {
		status := s.jobs[id].status
		status.SharedSources = s.sharedSources(s.jobs[id])
		snap.Jobs = append(snap.Jobs, status)
	}
	return writeJSON(filepath.Join(s.config.Root, "daemon-status.json"), snap)
}
func (s *Supervisor) commands() []string {
	dir := filepath.Join(s.config.Root, "daemon-control")
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.complete = false
		return nil
	}
	var shutdown []string
	for i, entry := range entries {
		if i >= 256 {
			s.complete = false
			break
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		var req commandRequest
		err := ReadJSON(path, &req)
		if err != nil {
			_ = writeJSON(path+".ack", commandReply{Error: "invalid_request"})
			_ = os.Remove(path)
			continue
		}
		if req.InstanceID != s.runtime.InstanceID {
			_ = writeJSON(path+".ack", commandReply{Error: "daemon_instance_changed"})
			_ = os.Remove(path)
			continue
		}
		if req.Action == "shutdown" {
			shutdown = append(shutdown, path)
			_ = os.Remove(path)
			continue
		}
		if req.Action == "claim" {
			_ = writeJSON(path+".ack", s.claim(req))
			_ = os.Remove(path)
			continue
		}
		j := s.jobs[req.ProfileID]
		code := ""
		switch {
		case j == nil:
			code = "profile_unavailable"
		case req.Action != "stop" && req.Action != "restart" && req.Action != "start" && req.Action != "collect":
			code = "invalid_action"
		case req.Action != "stop" && (!j.spec.Enabled || j.spec.BlockedReason != "" || j.status.ErrorCode == "duplicate_route"):
			code = "profile_unavailable"
		case req.Action != "stop" && j.status.ErrorCode == "rate_limited" && time.Now().Before(j.next):
			code = "rate_limited"
		}
		if code != "" {
			reply := commandReply{Error: code}
			if code == "profile_unavailable" && j != nil {
				reply.Reason, reply.Holders, reply.More = s.unavailable(j)
			}
			_ = writeJSON(path+".ack", reply)
			_ = os.Remove(path)
			continue
		}
		old := s.stops[req.ProfileID]
		s.stops[req.ProfileID] = req.Action == "stop"
		if err := writeJSON(filepath.Join(s.config.Root, "daemon-stops.json"), s.stops); err != nil {
			s.stops[req.ProfileID] = old
			_ = writeJSON(path+".ack", commandReply{Error: "state_write_failed"})
			_ = os.Remove(path)
			continue
		}
		if j.active && req.Action != "collect" {
			j.pending = append(j.pending, pendingControl{path: path, action: req.Action})
			j.cancel()
			j.status.State = "stopping"
		} else {
			if req.Action == "stop" {
				s.release(j)
				j.status.State = "stopped"
			} else {
				if req.Action == "restart" {
					s.release(j)
				}
				j.next = time.Time{}
				j.status.State = "unknown"
				j.status.ErrorCode = ""
			}
			_ = writeJSON(path+".ack", commandReply{})
		}
		_ = os.Remove(path)
	}
	return shutdown
}
