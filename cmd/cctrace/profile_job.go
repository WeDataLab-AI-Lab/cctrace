package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"cctrace/internal/claudeauth"
	"cctrace/internal/codexlog"
	"cctrace/internal/codexsyncer"
	"cctrace/internal/gjclog"
	"cctrace/internal/gjcsyncer"
	"cctrace/internal/localdaemon"
	"cctrace/internal/omolog"
	"cctrace/internal/omosyncer"
	"cctrace/internal/processio"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
	"cctrace/internal/usage"
)

type daemonOverrides struct {
	ProfileID, ClaudeDir, Email, Endpoint, CollectFile string
	Local                                              bool
	Interval                                           time.Duration
}

// Integration supplies the existing signed-policy startup barrier and optional
// local analysis runtime. Both use explicit profile identity and lease scope.
var prepareManagedProfile = func(context.Context, profile.Entry) (func(), error) { return func() {}, nil }
var startManagedProfileRuntime = func(_ context.Context, entry profile.Entry, _ string) (func(), error) {
	if entry.Profile.Options.LocalAnalysisEnabled {
		return nil, errors.New("local analysis runtime adapter unavailable")
	}
	return func() {}, nil
}

type profileJob struct {
	entry                            profile.Entry
	client                           *syncer.Client
	states                           []*syncer.State
	passes                           []func(context.Context) (int, error)
	claudeDir, endpoint, collectFile string
	claudeDirs                       []string
	codexDirs, gjcDirs, omoDirs      []string
	codexWarnings                    string
	codex                            *codexsyncer.CodexSyncer
	claude                           *syncer.Syncer
	// observations is the Syncer whose flush sends the job's observations: the
	// Claude syncer, or, when another profile holds the Claude folder, one that
	// leaves that folder to it and sends the heartbeat and the Codex snapshots.
	observations *syncer.Syncer
	extraClaude  []*syncer.Syncer
	// claudeExcluded: the profile collects Claude sessions but another profile
	// holds this one's Claude folder. The job then has no Claude pass and does not
	// ask for the folder's quota either.
	claudeExcluded bool
	// tookOver are the hand-overs of the allocation the last lease took the
	// previous holder's cursor for (Spec.TookOver).
	tookOver []localdaemon.Handoff
	// mu keeps a pass and a flush apart: both read and write the syncers' state.
	// firstPass is closed when a lease's first pass is done, which the flush loop
	// waits for before its first flush.
	mu               sync.Mutex
	firstPass        chan struct{}
	lastQuota        time.Time
	quotaUnsupported bool
	overrides        daemonOverrides
}

func profileJobSpec(entry profile.Entry, overrides daemonOverrides) localdaemon.Spec {
	p := *entry.Profile
	job := &profileJob{entry: entry}
	job.overrides = overrides
	job.entry.Profile = &p
	job.claudeDir = resolveClaudeDir(&p, "")
	job.endpoint = p.Server.SyncEndpoint
	if job.endpoint == "" {
		job.endpoint = p.Server.Endpoint
	}
	if entry.ID == overrides.ProfileID {
		if overrides.ClaudeDir != "" {
			job.claudeDir = overrides.ClaudeDir
		}
		if overrides.Email != "" {
			p.User.Email = overrides.Email
		}
		if overrides.Local {
			applyLocalDevEndpoints(&p, &overrides.Endpoint)
		}
		if overrides.Endpoint != "" {
			job.endpoint = overrides.Endpoint
		}
		job.collectFile = overrides.CollectFile
	}
	var sources []string
	labels := map[string]string{}
	if p.CollectionEnabled("claude") {
		job.claudeDirs = []string{job.claudeDir}
		if p.HomesVersion != 0 || p.Homes != nil {
			job.claudeDirs = p.TransmissionHomes("claude")
			job.claudeDir = ""
			if len(job.claudeDirs) > 0 {
				job.claudeDir = job.claudeDirs[0]
			}
		}
		for _, dir := range job.claudeDirs {
			sources = append(sources, "claude:"+dir)
			labels["claude:"+dir] = "home"
		}
	}
	if p.CollectionEnabled("codex") {
		// The same checked resolution as one-shot collection. The spec is rebuilt
		// on every refresh, so what it skips is said once, when the lease is taken.
		var skipped bytes.Buffer
		job.codexDirs = resolveCodexScanDirs(&skipped, &p)
		job.codexWarnings = skipped.String()
		for _, dir := range job.codexDirs {
			sources = append(sources, "codex:"+dir)
			labels["codex:"+dir] = codexSourceLabel(dir, &p)
		}
	}
	if p.Options.GjcSyncEnabled {
		job.gjcDirs = resolveGjcScanDirs(io.Discard, &p)
		for _, dir := range job.gjcDirs {
			sources = append(sources, "gjc:"+dir)
			labels["gjc:"+dir] = defaultOrConfiguredLabel(dir, gjclog.DefaultGjcDir())
		}
	}
	if p.Options.OmoSyncEnabled {
		job.omoDirs = resolveOmoScanDirs(io.Discard, &p)
		for _, dir := range job.omoDirs {
			sources = append(sources, "omo:"+dir)
			labels["omo:"+dir] = defaultOrConfiguredLabel(dir, omolog.DefaultOmoDir())
		}
	}
	destination := sha256.Sum256([]byte(strings.TrimRight(job.endpoint, "/") + "\x00" + p.User.ID + "\x00" + p.User.Email))
	return localdaemon.Spec{ID: entry.ID, Name: entry.Name, RuntimeDir: entry.RuntimeDir, Revision: entry.Revision, Enabled: entry.CollectionState == "enabled", Destination: hex.EncodeToString(destination[:]), Sources: sources, SourceLabels: labels, Filters: profileFilters(&p), LocalAnalysis: p.Options.LocalAnalysisEnabled, Acquire: job.acquire, TookOver: func() []localdaemon.Handoff { return job.tookOver }, Run: job.run, Quiesce: func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		release, err := localdaemon.AcquireProfile(ctx, entry.RuntimeDir)
		if err == nil {
			release()
		}
		return err
	}}
}

// codexSourceLabel says where a Codex folder in the profile's sources came from:
// the explicit home, the default folder, CODEX_HOME, or the profile's list. The
// explicit home comes first because it is the one whose path is never shown.
func codexSourceLabel(dir string, p *profile.Profile) string {
	same := func(a, b string) bool {
		if strings.TrimSpace(b) == "" {
			return false
		}
		abs, err := filepath.Abs(codexlog.ExpandHome(strings.TrimSpace(b)))
		return err == nil && filepath.Clean(a) == abs
	}
	switch {
	case same(dir, p.CodexHome):
		return "explicit"
	case same(dir, codexlog.DefaultCodexDir()):
		return "default"
	case same(dir, os.Getenv("CODEX_HOME")):
		return "env"
	}
	return "configured"
}

func defaultOrConfiguredLabel(dir, def string) string {
	if abs, err := filepath.Abs(def); err == nil && filepath.Clean(dir) == abs {
		return "default"
	}
	return "configured"
}

// profileFilters are the settings that decide what a profile sends. A source the
// profile held can move to another profile on its own only if that one's
// filters send no more than these.
func profileFilters(p *profile.Profile) localdaemon.Filters {
	return localdaemon.Filters{
		RepoPrefixes:    slices.Clone(p.Options.CollectRepositoryPrefixes),
		RedactPrompts:   p.Options.RedactUserPrompts,
		RedactTools:     p.Options.RedactToolDetails,
		ExcludeAccounts: slices.Clone(p.Options.ExcludeAccounts),
	}
}

// logf is diagf for what a profile's job says: a daemon's one log is shared by
// every profile it collects.
func (j *profileJob) logf(format string, args ...any) {
	diagf("profile=%s %s", j.entry.Name, fmt.Sprintf(format, args...))
}

// acquire takes the profile's lease for one generation, which reads the sources
// the allocation allows and no others. Those are locked line by line for as long
// as the lease lasts, so that no other run reads them at the same time; a line
// that is in use is reported as localdaemon.ErrLineBusy, which the supervisor
// retries without calling it a failure.
func (j *profileJob) acquire(ctx context.Context, alloc localdaemon.Allocation) (func(), error) {
	barrier, err := prepareManagedProfile(ctx, j.entry)
	if err != nil {
		return nil, err
	}
	defer barrier()
	leaseCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release, err := localdaemon.AcquireProfile(leaseCtx, j.entry.RuntimeDir)
	if err != nil {
		return nil, err
	}
	if len(alloc.Routes) > 0 {
		releaseLines, err := localdaemon.LockLines(ctx, profile.DefaultDir(), alloc.Routes, 0)
		if err != nil {
			release()
			return nil, fmt.Errorf("take the sources' locks: %w", err)
		}
		releaseProfile := release
		release = func() { releaseLines(); releaseProfile() }
	}
	if err := j.prepare(ctx, alloc); err != nil {
		release()
		return nil, err
	}
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	closeRuntime, err := startManagedProfileRuntime(runtimeCtx, j.entry, j.claudeDir)
	if err != nil {
		runtimeCancel()
		release()
		return nil, err
	}
	releaseLock := release
	// The flush loop lives as long as the lease, not as long as a pass: what the
	// record sync does -- fail, back off, be told to wait -- must not decide when
	// the collector's heartbeat goes.
	j.firstPass = make(chan struct{})
	flushDone := make(chan struct{})
	go func(firstPass <-chan struct{}, interval time.Duration) {
		defer close(flushDone)
		j.flushLoop(runtimeCtx, firstPass, interval)
	}(j.firstPass, observationFlushInterval)
	release = func() { runtimeCancel(); <-flushDone; closeRuntime(); releaseLock() }
	interval := j.overrides.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	rt := syncRuntime{PID: os.Getpid(), InstanceID: newSyncInstanceID(), StartedAt: time.Now().UTC(), Version: version, Interval: interval, ClaudeDir: j.claudeDir, ProfileEmail: j.entry.Profile.User.Email, SupervisorRoot: profile.DefaultDir()}
	if j.entry.ID == j.overrides.ProfileID {
		rt.Local = j.overrides.Local
		rt.EndpointOverride = j.overrides.Endpoint
	}
	if err := writeJSONFile(filepath.Join(j.entry.RuntimeDir, "sync-runtime.json"), rt); err != nil {
		release()
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(j.entry.RuntimeDir, "sync.pid"), []byte(strconv.Itoa(rt.PID)), 0600); err != nil {
		release()
		return nil, err
	}
	return func() {
		var current syncRuntime
		if ok, err := readJSONFile(filepath.Join(j.entry.RuntimeDir, "sync-runtime.json"), &current); err == nil && ok && current.InstanceID == rt.InstanceID {
			_ = writeJSONFile(filepath.Join(j.entry.RuntimeDir, "sync-exit.json"), syncExit{InstanceID: rt.InstanceID, ExitedAt: time.Now().UTC()})
			_ = os.Remove(filepath.Join(j.entry.RuntimeDir, "sync-runtime.json"))
			_ = os.Remove(filepath.Join(j.entry.RuntimeDir, "sync.pid"))
		}
		release()
		j.client = nil
		j.states = nil
		j.passes = nil
		j.codex = nil
		j.claude = nil
		j.observations = nil
		j.extraClaude = nil
		j.firstPass = nil
	}, nil
}

// prepare builds what one generation reads from: a pass for each kind of source,
// over the folders the allocation allows. A kind whose folders are all held by
// others has no pass and no state; a profile with nothing to upload -- local
// analysis only -- has no pass at all and still runs.
func (j *profileJob) prepare(ctx context.Context, alloc localdaemon.Allocation) error {
	j.states = nil
	j.passes = nil
	j.codex = nil
	j.claude = nil
	j.observations = nil
	j.extraClaude = nil
	j.claudeExcluded = false
	j.tookOver = nil
	p := j.entry.Profile
	client, err := newSyncClient(p, j.endpoint, version, strings.TrimPrefix(j.entry.ID, "named:"))
	if err != nil {
		return err
	}
	if err := client.SetTransferDiagnostics(filepath.Join(j.entry.RuntimeDir, "transfer-diagnostics.json")); err != nil {
		return err
	}
	j.client = client
	for _, line := range strings.Split(strings.TrimSpace(j.codexWarnings), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			j.logf("%s", line)
		}
	}
	load := func(name string) (*syncer.State, error) {
		state, err := syncer.LoadState(filepath.Join(j.entry.RuntimeDir, name))
		if err == nil {
			j.states = append(j.states, state)
		}
		return state, err
	}
	if p.CollectionEnabled("claude") && j.claudeDir != "" {
		if allowedDirs("claude", []string{j.claudeDir}, alloc) == nil || p.HomesVersion != 0 && profile.CheckCodexHome(j.claudeDir) != nil {
			j.claudeExcluded = true
			state, err := syncer.LoadState(filepath.Join(j.entry.RuntimeDir, claudeStateFile(p, j.claudeDir)))
			if err != nil {
				return err
			}
			j.observations = syncer.New(j.claudeDir, p.User.Email, p.User.ID, state, client, p.Options.CollectRepositoryPrefixes)
			j.observations.LeaveClaudeHome()
		} else {
			stateFile := claudeStateFile(p, j.claudeDir)
			state, err := load(stateFile)
			if err != nil {
				return err
			}
			seedCollectFile(state, j.collectFile)
			j.claude = syncer.New(j.claudeDir, p.User.Email, p.User.ID, state, client, p.Options.CollectRepositoryPrefixes)
			j.claude.SetPeerPending(peerSessionEndQueues(alloc.PeerPending))
			claude := j.claude
			if err := j.arrangeCursors(ctx, alloc, cursorKind{kind: "claude", stateFile: stateFile, state: state, dirs: []string{j.claudeDir}, transcripts: true,
				join: func(ctx context.Context, _ string, t0 time.Time) (int, error) { return claude.JoinSource(ctx, t0) }}); err != nil {
				return err
			}
			j.passes = append(j.passes, j.claude.SyncOnce)
			j.observations = j.claude
		}
	}
	if len(j.claudeDirs) > 1 {
		for _, dir := range j.claudeDirs[1:] {
			if allowedDirs("claude", []string{dir}, alloc) == nil || profile.CheckCodexHome(dir) != nil {
				continue
			}
			stateFile := claudeStateFile(p, dir)
			state, e := load(stateFile)
			if e != nil {
				return e
			}
			collector := syncer.New(dir, p.User.Email, p.User.ID, state, client, p.Options.CollectRepositoryPrefixes)
			collector.SetPeerPending(peerSessionEndQueues(alloc.PeerPending))
			j.extraClaude = append(j.extraClaude, collector)
			if e = j.arrangeCursors(ctx, alloc, cursorKind{kind: "claude", stateFile: stateFile, state: state, dirs: []string{dir}, transcripts: true, join: func(ctx context.Context, _ string, t0 time.Time) (int, error) { return collector.JoinSource(ctx, t0) }}); e != nil {
				return e
			}
			j.passes = append(j.passes, collector.SyncOnce)
		}
	}
	if dirs := j.sourceDirs("codex", j.codexDirs, alloc); p.CollectionEnabled("codex") && dirs != nil {
		state, err := load("codex-sync-state.json")
		if err != nil {
			return err
		}
		// The spec's check can be older than this lease. A home that has turned
		// into a symlink since, or lost its owner or mode, is left out of what
		// this lease scans; the spec's own list is untouched, so a home that
		// recovers is scanned again by the next lease.
		codexDirs := dirs
		if home := filepath.Clean(p.CodexHome); p.CodexHome != "" && slices.Contains(codexDirs, home) && profile.CheckCodexHome(p.CodexHome) != nil {
			codexDirs = slices.DeleteFunc(slices.Clone(codexDirs), func(dir string) bool { return dir == home })
			j.logf("[codex-sync] explicit codex_home is unavailable; it was not scanned")
		}
		j.codex = codexsyncer.New(codexDirs, p.User.Email, p.User.ID, state, client, p.Options.CollectRepositoryPrefixes)
		j.codex.SetRedactedHomes(p.CodexHome)
		if err := j.arrangeCursors(ctx, alloc, cursorKind{kind: "codex", stateFile: "codex-sync-state.json", state: state, dirs: codexDirs, join: j.codex.JoinSource}); err != nil {
			return err
		}
		// The Codex session snapshots go in the one flush, under the accounts of
		// the Codex homes this job reads. A profile that does not collect Claude
		// sessions at all has no syncer to carry them.
		if j.observations != nil {
			j.observations.ServeCodexSnapshots(state, codexDirs)
		}
		j.passes = append(j.passes, j.codex.SyncOnce)
	}
	if dirs := j.sourceDirs("gjc", j.gjcDirs, alloc); p.Options.GjcSyncEnabled && dirs != nil {
		state, err := load("gjc-sync-state.json")
		if err != nil {
			return err
		}
		gjc := gjcsyncer.New(dirs, p.User.Email, p.User.ID, state, client, p.Options.CollectRepositoryPrefixes)
		if err := j.arrangeCursors(ctx, alloc, cursorKind{kind: "gjc", stateFile: "gjc-sync-state.json", state: state, dirs: dirs, join: gjc.JoinSource}); err != nil {
			return err
		}
		j.passes = append(j.passes, gjc.SyncOnce)
	}
	if dirs := j.sourceDirs("omo", j.omoDirs, alloc); p.Options.OmoSyncEnabled && dirs != nil {
		state, err := load("omo-sync-state.json")
		if err != nil {
			return err
		}
		omo := omosyncer.New(dirs, p.User.Email, p.User.ID, state, client, p.Options.CollectRepositoryPrefixes)
		if err := j.arrangeCursors(ctx, alloc, cursorKind{kind: "omo", stateFile: "omo-sync-state.json", state: state, dirs: dirs, join: omo.JoinSource}); err != nil {
			return err
		}
		j.passes = append(j.passes, omo.SyncOnce)
	}
	return nil
}

// peerSessionEndQueues are the queues of session ends of the profiles whose
// runtime directories the allocation names: profiles left out of this Claude
// folder, whose hooks record into their own queues and have no collector to send.
func peerSessionEndQueues(runtimeDirs []string) []string {
	queues := make([]string, 0, len(runtimeDirs))
	for _, dir := range runtimeDirs {
		queues = append(queues, syncer.SessionEndDir(filepath.Join(dir, "sync-state.json")))
	}
	return queues
}

// allowedDirs are the folders of kind among dirs that the allocation allows, in
// their order; nil if there are folders and none is allowed.
func allowedDirs(kind string, dirs []string, alloc localdaemon.Allocation) []string {
	var allowed []string
	for _, dir := range dirs {
		if slices.Contains(alloc.Allowed, kind+":"+dir) {
			allowed = append(allowed, dir)
		}
	}
	if len(allowed) == 0 && len(dirs) > 0 {
		return nil
	}
	return allowed
}

// sourceDirs is allowedDirs for a kind that may have no folder at all, in which
// case its pass exists and has nothing to scan, as it always has: the result is
// then an empty, non-nil list.
func (j *profileJob) sourceDirs(kind string, dirs []string, alloc localdaemon.Allocation) []string {
	if allowed := allowedDirs(kind, dirs, alloc); allowed != nil || len(dirs) == 0 {
		if allowed == nil {
			return []string{}
		}
		return allowed
	}
	return nil
}

func (j *profileJob) run(ctx context.Context, report func(localdaemon.Progress)) error {
	j.mu.Lock()
	unlock := sync.OnceFunc(j.mu.Unlock)
	defer unlock()
	j.client.ResetTransferOutcome()
	total := 0
	var failure error
	for _, pass := range j.passes {
		n, err := pass(ctx)
		total += n
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			failure = err
		}
	}
	outcome := j.client.TransferOutcome()
	progress := localdaemon.Progress{State: "healthy", Sent: total}
	if outcome.Failed > 0 {
		failure = outcome.LastError
	}
	if failure != nil {
		progress.State = "retrying"
		progress.ErrorCode = "transport_error"
		progress.RetryAfter = outcome.RetryAfter
		if errors.Is(failure, syncer.ErrAuthentication) {
			progress.ErrorCode = "auth_error"
		}
		var retry *syncer.RetryableError
		if errors.As(failure, &retry) {
			progress.ErrorCode = "rate_limited"
			if retry.RetryAfter > progress.RetryAfter {
				progress.RetryAfter = retry.RetryAfter
			}
		}
	}
	for _, state := range j.states {
		for _, file := range state.Files {
			if file.BlockedByBodyLimit {
				progress.BlockedFiles++
			}
		}
	}
	if progress.BlockedFiles > 0 && failure == nil {
		progress.State = "blocked"
	}
	unlock()
	report(progress)
	if j.firstPass != nil {
		select {
		case <-j.firstPass:
		default:
			close(j.firstPass)
		}
	}
	// Quota has its own cadence and never changes collection health. Missing
	// windows remain absent in the desktop's existing quota protocol.
	if j.lastQuota.IsZero() {
		j.lastQuota = time.Now()
	} else if time.Since(j.lastQuota) >= quotaInterval {
		j.lastQuota = time.Now()
		j.pollQuota(ctx)
	}
	return nil
}

// quotaInterval is how often a job reports quota. Var, not const, so tests can
// poll without waiting.
var quotaInterval = 5 * time.Minute

// errClaudeQuotaSkipped is why a job that does not read a Claude folder has no
// usage to report for it.
var errClaudeQuotaSkipped = errors.New("the Claude folder is held by another profile")

// fetchClaudeUsage is a seam: the quota gate is told apart from a failed fetch by
// whether it was called.
var fetchClaudeUsage = usage.FetchContext

// managedObservationFlushTimeout bounds each send of a flush, as
// oneShotObservationFlushTimeout bounds a run about to exit: a route that hangs
// would otherwise hold the job's pass off for as long as the connection lives.
// Var, not const, so tests can shrink it.
var managedObservationFlushTimeout = 10 * time.Second

// managedFlushDone, when set, is called after every flush of a lease. Tests wait
// on it instead of on the clock.
var managedFlushDone func()

// flushLoop is watch mode's observation flush for a managed profile: the first
// flush once the lease's first pass is done (or one interval after the lease, if
// that pass is slower), then one per interval. It is the heartbeat, the session
// snapshots, the pending session ends and the process I/O queue. The server
// takes a collector for stale after three minutes without a heartbeat, so these
// ride their own interval, beside the pass and its backoff, as in watch mode
// (runSync); the SessionEnd hook's --once child gives up at once on the lease
// this job holds, so a managed profile has no other process to send them.
func (j *profileJob) flushLoop(ctx context.Context, firstPass <-chan struct{}, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	select {
	case <-ctx.Done():
		return
	case <-firstPass:
	case <-ticker.C:
	}
	for {
		j.flushObservations(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// flushObservations is runSync's flush after a pass: FlushObservations of the
// job's observations Syncer, then the process I/O queue. A failure is reported
// and does not touch the pass's health; what did not go stays queued. The queue
// is the profile's own: a report under another identity is left to the queue's
// retention, not dropped, since a launcher can run under an email the profile
// does not carry (CCTRACE_PROFILE_EMAIL).
func (j *profileJob) flushObservations(lease context.Context) {
	if managedFlushDone != nil {
		defer managedFlushDone()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if lease.Err() != nil {
		return
	}
	p := j.entry.Profile
	if j.observations != nil {
		ctx, cancel := context.WithTimeout(lease, managedObservationFlushTimeout)
		if err := j.observations.FlushObservations(ctx); err != nil && lease.Err() == nil {
			j.logf("[observations] %v", err)
		}
		cancel()
	}
	for _, collector := range j.extraClaude {
		ctx, cancel := context.WithTimeout(lease, managedObservationFlushTimeout)
		if err := collector.FlushObservations(ctx); err != nil && lease.Err() == nil {
			j.logf("[observations] %v", err)
		}
		cancel()
	}
	// Legacy process reports contain no source home. Hold them under explicit
	// membership rather than guess permission from a billing ID or current HOME.
	ctx, cancel := context.WithTimeout(lease, managedObservationFlushTimeout)
	defer cancel()
	queue := processio.Queue{Path: filepath.Join(j.entry.RuntimeDir, processIOQueueFile)}
	if err := flushProcessIOQueueFor(ctx, queue, j.client, p.User.Email, p.User.ID, p.Options.ExcludeAccounts, false, func(r processio.QueuedReport) bool {
		allowed := processIOHomeAllowed(p, r)
		if !allowed {
			j.logf("[process-io] pending observation held: source consent unavailable or changed")
		}
		return allowed
	}); err != nil && lease.Err() == nil {
		j.logf("[process-io] observation upload: %v", err)
	}
}

func (j *profileJob) pollQuota(ctx context.Context) {
	p := j.entry.Profile
	if p.HomesVersion != 0 || p.Homes != nil {
		return
	} // helper owns explicit per-home quota consent
	var u *usage.Response
	err := errClaudeQuotaSkipped
	if !j.claudeExcluded {
		u, err = fetchClaudeUsage(ctx, j.claudeDir)
	}
	if err == nil {
		q := &syncer.QuotaPayload{ProfileEmail: p.User.Email, UserID: p.User.ID}
		if w := u.Window(usage.KindSession); w != nil {
			q.FiveHourPct = w.Utilization
			q.FiveHourResetsAt = w.ResetsAt
		}
		if w := u.Window(usage.KindWeeklyAll); w != nil {
			q.SevenDayPct = w.Utilization
			q.SevenDayResetsAt = w.ResetsAt
		}
		_ = j.client.SendQuota(ctx, q)
		if !j.quotaUnsupported {
			if acct, err := claudeauth.ReadAccount(j.claudeDir); err == nil && acct.AccountUUID != "" && usage.CheckAccountBinding(j.claudeDir, acct.AccountUUID, u) == nil {
				j.quotaUnsupported = errors.Is(j.client.SendQuotaSamples(ctx, u.Samples(acct.AccountUUID, acct.EmailAddress, p.User.Email)), syncer.ErrQuotaSamplesUnsupported)
			}
		}
	}
	if j.codex != nil {
		j.codex.PollAppServerQuota(ctx)
	}
}

// Explicit multi-home states are keyed by source, independent of display order.
func claudeStateFile(p *profile.Profile, home string) string {
	if p.HomesVersion == 0 && p.Homes == nil {
		return "sync-state.json"
	}
	if p.ClaudeConfigDir != "" && filepath.Clean(p.ClaudeConfigDir) == home {
		return "sync-state.json"
	}
	digest := sha256.Sum256([]byte(home))
	return fmt.Sprintf("claude-%x-sync-state.json", digest[:12])
}
