package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"cctrace/internal/sessionlog"
)

// ErrSessionObservationsUnsupported marks a server with no
// /api/session-observations route: one older than this client. Record sync does
// not depend on it, so observations are parked and sync goes on.
var ErrSessionObservationsUnsupported = errors.New("server does not support session observations")

// ErrSessionObservationsRejected marks a 400: the server refused the request as
// malformed or over a limit, and the error carries what it said. The same bytes
// can never succeed, so nothing retries them as they are.
var ErrSessionObservationsRejected = errors.New("server rejected the session observations")

// SessionObservationsRequest is the body of POST /api/session-observations, as
// far as this client fills it: the collector heartbeat, session snapshots, live
// states and session ends. The names mirror the server's
// (internal/api/handlers_session_observations.go), which this package does not
// import.
type SessionObservationsRequest struct {
	ProfileEmail string `json:"profile_email"`
	UserID       string `json:"user_id"`
	// LoginEmail stays empty for Claude, as on /api/sync.
	LoginEmail string `json:"login_email"`
	// HeartbeatAt says the collector is running. Only its presence counts: the
	// server dates the heartbeat by its own clock.
	HeartbeatAt *time.Time                    `json:"heartbeat_at,omitempty"`
	Snapshots   []SessionSnapshotItem         `json:"snapshots,omitempty"`
	LiveStates  []SessionLiveStateItem        `json:"live_states,omitempty"`
	Events      []SessionObservationEventItem `json:"events,omitempty"`
}

// SessionSnapshotItem is one session's snapshot and whose it is.
type SessionSnapshotItem struct {
	SessionID       string `json:"session_id"`
	Agent           string `json:"agent"`
	ProjectHash     string `json:"project_hash"`
	LoginEmail      string `json:"login_email"`
	BillingProvider string `json:"billing_provider"`
	AccountID       string `json:"account_id"`
	// ObservedAt is when the snapshot last changed, sent as RFC3339Nano. The
	// server keeps the newer of two.
	ObservedAt     time.Time       `json:"observed_at"`
	CollectVersion int             `json:"collect_version"`
	Snapshot       SessionSnapshot `json:"snapshot"`
	// titlesReadRedacted is SessionSnapshotState.TitlesReadRedacted: the titles
	// the sender hides whatever the policy is now.
	titlesReadRedacted TitleFlags
}

// SessionLiveStateItem is one agent process's run of a session. StatusSource
// records whether its state came from a native process file, process liveness,
// or an explicit unknown process-I/O observation.
type SessionLiveStateItem struct {
	SessionID       string `json:"session_id"`
	Agent           string `json:"agent"`
	ProjectHash     string `json:"project_hash"`
	LoginEmail      string `json:"login_email"`
	BillingProvider string `json:"billing_provider"`
	AccountID       string `json:"account_id"`
	// PID and ProcStart, as the file gives them, tell this process from another
	// run of the same session, and from a later process given the same pid.
	PID          int64  `json:"pid"`
	ProcStart    string `json:"proc_start"`
	Status       string `json:"status"`
	StatusSource string `json:"status_source,omitempty"`
	WaitingKind  string `json:"waiting_kind"`
	Name         string `json:"name"`
	NameSource   string `json:"name_source"`
	// StatusChangedAt is when the file says the status last changed; for gone,
	// when this client found the process gone, like HeartbeatAt.
	StatusChangedAt *time.Time `json:"status_changed_at,omitempty"`
	HeartbeatAt     time.Time  `json:"heartbeat_at"`
	IOEpoch         string     `json:"io_epoch,omitempty"`
	IOAsOf          *time.Time `json:"io_as_of,omitempty"`
	IOSource        string     `json:"io_source,omitempty"`
	IOMode          string     `json:"io_mode,omitempty"`
	IOPartial       bool       `json:"io_partial,omitempty"`
	IOFailureCode   string     `json:"io_failure_code,omitempty"`
	InputBytes      *int64     `json:"input_bytes,omitempty"`
	InputLines      *int64     `json:"input_lines,omitempty"`
	OutputBytes     *int64     `json:"output_bytes,omitempty"`
	OutputLines     *int64     `json:"output_lines,omitempty"`
	StdoutBytes     *int64     `json:"stdout_bytes,omitempty"`
	StdoutLines     *int64     `json:"stdout_lines,omitempty"`
	StderrBytes     *int64     `json:"stderr_bytes,omitempty"`
	StderrLines     *int64     `json:"stderr_lines,omitempty"`
}

// SessionObservationEventItem is one entry of session history. This client
// sends only session_end; the server derives the others from live states.
type SessionObservationEventItem struct {
	SessionID       string            `json:"session_id"`
	Agent           string            `json:"agent"`
	ProjectHash     string            `json:"project_hash"`
	LoginEmail      string            `json:"login_email"`
	BillingProvider string            `json:"billing_provider"`
	AccountID       string            `json:"account_id"`
	EventID         string            `json:"event_id"`
	Kind            string            `json:"kind"`
	ObservedAt      time.Time         `json:"observed_at"`
	Payload         SessionEndPayload `json:"payload"`
}

// SessionEndPayload is what a session_end event says: the reason the SessionEnd
// hook gave.
type SessionEndPayload struct {
	Reason string `json:"reason"`
}

// SessionObservationsResponse counts the request's items: Accepted is all of
// them, Stored those that changed a row, Skipped those ingest refused (blocked,
// deleted, excluded, dated too far ahead). The heartbeat is not an item.
type SessionObservationsResponse struct {
	Accepted int `json:"accepted"`
	Stored   int `json:"stored"`
	Skipped  int `json:"skipped"`
}

// SendSessionObservations posts one request to /api/session-observations.
//
// Status: 200 is decoded; 429 is a RetryableError carrying Retry-After; 413 is
// ErrBodyTooLarge; 404 is ErrSessionObservationsUnsupported; 400 is
// ErrSessionObservationsRejected with the server's reason; anything else is an
// error of its own.
func (c *Client) SendSessionObservations(ctx context.Context, req SessionObservationsRequest) (SessionObservationsResponse, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		if ctxErr := rateLimitContextErr(ctx); ctxErr != nil {
			return SessionObservationsResponse{}, ctxErr
		}
		return SessionObservationsResponse{}, fmt.Errorf("rate limiter: %w", err)
	}
	// Redacted here, on a copy, for the reason the policy lives on the client:
	// every request passes through this one place on its way out.
	if len(req.Snapshots) > 0 {
		items := make([]SessionSnapshotItem, len(req.Snapshots))
		for i, it := range req.Snapshots {
			it.Snapshot = redactSnapshot(it.Snapshot, c.redact, it.titlesReadRedacted)
			items[i] = it
		}
		req.Snapshots = items
	}
	// A session's name is typed by the person or derived from what they asked,
	// so the prompts policy takes it. Unlike a snapshot title it carries no flag
	// for the policy it was read under: names are read from the process files
	// afresh on every flush, so the policy in force when one goes out is the
	// person's current choice. A gone state keeps a name this same process read.
	if len(req.LiveStates) > 0 {
		items := make([]SessionLiveStateItem, len(req.LiveStates))
		for i, it := range req.LiveStates {
			if c.redact.UserPrompts && it.Name != "" {
				it.Name = redactedMarker
			}
			items[i] = it
		}
		req.LiveStates = items
	}
	body, err := encodeRequestJSON(req)
	if err != nil {
		return SessionObservationsResponse{}, fmt.Errorf("marshal: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, transferDeadline(int64(len(body))))
	defer cancel()
	httpReq, err := c.newJSONPostRequest(ctx, "/api/session-observations", body)
	if err != nil {
		return SessionObservationsResponse{}, err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return SessionObservationsResponse{}, fmt.Errorf("%w: %d bytes did not transfer within %s", ErrTransferTimeout, len(body), transferDeadline(int64(len(body))))
		}
		return SessionObservationsResponse{}, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		// Integer seconds with a 5s default, as sync reads it, bounded the way
		// the rule and exclusion queries bound it.
		retryAfter := 5 * time.Second
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			retryAfter = min(time.Duration(min(secs, maxRetryAfterSeconds))*time.Second, maxRetryAfterDelay)
		}
		return SessionObservationsResponse{}, &RetryableError{Err: fmt.Errorf("server returned 429"), RetryAfter: retryAfter}
	case http.StatusRequestEntityTooLarge:
		return SessionObservationsResponse{}, ErrBodyTooLarge
	case http.StatusNotFound:
		return SessionObservationsResponse{}, ErrSessionObservationsUnsupported
	case http.StatusBadRequest:
		return SessionObservationsResponse{}, fmt.Errorf("%w: %s", ErrSessionObservationsRejected, serverErrorText(resp.Body))
	default:
		return SessionObservationsResponse{}, fmt.Errorf("server returned %d", resp.StatusCode)
	}
	var out SessionObservationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SessionObservationsResponse{}, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}

// serverErrorText is the reason a 400 gives ({"error": ...}), bounded because it
// goes into a log line.
func serverErrorText(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, 1<<10))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(raw))
}

// redactSnapshot applies the policy to a snapshot on its way out, by field
// rather than by Redact's key walk. The titles and the agent name are what the
// person or the model wrote about the conversation, so the prompts policy takes
// them -- and keeps taking a title read while it was on (readRedacted), after
// it has been turned off. Nothing else in a snapshot is redacted: PR links, cost
// and line totals, model ids and continued-in session ids are outcome metadata,
// like the branch name sync already sends under either policy, and a Codex
// turn's times, ending, model and effort, and the git counts, hold no text.
//
// s is a copy, and the titles are replaced with new pointers, never written
// through, so the state the snapshot came from keeps them.
func redactSnapshot(s SessionSnapshot, policy RedactPolicy, readRedacted TitleFlags) SessionSnapshot {
	if policy.Enabled() && s.Commits != nil {
		s.Commits = s.Commits.clone()
		for i := range s.Commits.Items {
			if s.Commits.Items[i].Subject != nil {
				marker := redactedMarker
				s.Commits.Items[i].Subject = &marker
			}
		}
	}
	for _, t := range []struct {
		title **string
		read  bool
	}{
		{&s.AITitle, readRedacted.AITitle},
		{&s.CustomTitle, readRedacted.CustomTitle},
		{&s.AgentName, readRedacted.AgentName},
	} {
		if (policy.UserPrompts || t.read) && *t.title != nil && **t.title != "" {
			marker := redactedMarker
			*t.title = &marker
		}
	}
	return s
}

// Bounds on one flush request.
const (
	// Items of every kind together. The server takes at most 200 snapshots, 200
	// live states and 500 events in one request (maxSessionSnapshotsPerRequest
	// and its neighbours), so no kind can pass its own cap.
	maxObservationChunkItems = 200
	// Half the server's 8 MiB body cap, so a chunk fits without asking; a 413 still splits.
	maxObservationChunkBytes = 4 << 20
	// The server's cap on profile_email, user_id and login_email (maxSessionObservationKeyBytes).
	maxObservationEnvelopeBytes = 256
	// The server's caps on an item's keys -- session_id, project_hash,
	// proc_start -- and on its tags, name_source among them
	// (maxSessionObservationKeyBytes, maxSessionObservationTagBytes). An item past
	// one is refused whole, so it is not sent.
	maxObservationKeyBytes = 256
	maxObservationTagBytes = 64
)

// observationsUnsupportedPark is how long a 404 stops the flush, for the reason
// exclusionsUnsupportedPark is not a latch: during a rolling deploy one old
// instance can answer 404 while the rest have the route.
const observationsUnsupportedPark = time.Hour

// observationsEnvelopePark is how long the flush stops after the server refused
// a heartbeat alone. The envelope is this machine's identity, which does not
// change until someone edits the profile; retrying sooner repeats the refusal.
const observationsEnvelopePark = time.Hour

// errEnvelopeRejected marks a 400 the server also gave a request carrying no
// item at all: what it refuses is the envelope -- the identity every request
// carries -- and no item is at fault.
var errEnvelopeRejected = errors.New("the server refuses the request envelope")

// maxLoneRefusals is how many items one flush lets the server refuse one by
// one. Past it the server is enforcing a rule this client does not know -- a
// newer server's stricter item check -- rather than refusing a stray item, and
// halving on would cost two requests per item and settle every one unsent.
const maxLoneRefusals = 3

// observationsRefusingPark is how long the flush stops once the server has
// refused more than maxLoneRefusals items in one flush: the rest stay pending
// for a client that knows the rule, or for a server that relaxes it.
const observationsRefusingPark = time.Hour

// errRefusingItems stops a flush at the refusal past maxLoneRefusals.
var errRefusingItems = fmt.Errorf("the server refused more than %d session observations one by one; the rest are left unsent", maxLoneRefusals)

// observationFlushState is what FlushObservations keeps between calls.
type observationFlushState struct {
	// parkedUntil holds the flush back after a 404, a 429, a refused envelope or
	// too many refused items.
	parkedUntil time.Time
	// envelopeLogged and skippedLogged keep a standing condition to one line.
	envelopeLogged bool
	skippedLogged  bool
	// live is what finding a process gone takes from one flush to the next.
	live liveStateMemory
	// logged keeps each kind of problem with what a flush reads -- a process
	// file, a pending session end -- to one line per process: the flush reads
	// them every minute.
	logged map[string]bool
}

// logOnce logs a problem of the given kind the first time this process meets one.
func (s *Syncer) logOnce(kind, format string, args ...any) {
	if s.obs.logged[kind] {
		return
	}
	if s.obs.logged == nil {
		s.obs.logged = map[string]bool{}
	}
	s.obs.logged[kind] = true
	log.Printf(format, args...)
}

// pendingObservation is one item of a flush on its way out: a dirty snapshot, a
// live state or a pending session end -- exactly one of the three.
type pendingObservation struct {
	snapshot  *SessionSnapshotItem
	liveState *SessionLiveStateItem
	event     *SessionObservationEventItem
	size      int
	// settle is the item's last word in this flush. A 200, or an exclusion that
	// consumes the item unsent, calls it with false; the server's refusal of the
	// item alone calls it with true, since resending the same bytes cannot
	// succeed. A snapshot is marked sent at the revision it carries, so a change
	// made while the request was in flight stays dirty; a gone state is not sent
	// again; a session end's pending file is deleted. A live state that is not
	// gone has nothing to settle unless refused: then this process does not send
	// that process's state again.
	settle func(refused bool)
}

// account is the billing account the item is attributed to, if it names one.
func (p pendingObservation) account() (AccountRef, bool) {
	var provider, id string
	switch {
	case p.snapshot != nil:
		provider, id = p.snapshot.BillingProvider, p.snapshot.AccountID
	case p.liveState != nil:
		provider, id = p.liveState.BillingProvider, p.liveState.AccountID
	case p.event != nil:
		provider, id = p.event.BillingProvider, p.event.AccountID
	}
	return AccountRef{BillingProvider: provider, AccountID: id}, id != ""
}

// refusal says, for the log line, what the server refused alone and what
// becomes of it.
func (p pendingObservation) refusal() string {
	switch {
	case p.snapshot != nil:
		return fmt.Sprintf("the snapshot of session %s and it is not retried until it changes", p.snapshot.SessionID)
	case p.liveState != nil:
		return fmt.Sprintf("the live state of session %s (pid %d) and this collector does not send it again", p.liveState.SessionID, p.liveState.PID)
	default:
		return fmt.Sprintf("the session end of session %s and its pending file is deleted", p.event.SessionID)
	}
}

// encodedSize is about what an item adds to a request body, for chunking.
func encodedSize(item any) int {
	b, err := json.Marshal(item)
	if err != nil {
		return 0
	}
	return len(b)
}

// FlushObservations sends this Claude home's live states, its pending session
// ends and its dirty session snapshots, and the dirty snapshots of the Codex
// homes this process serves (ServeCodexSnapshots), with the collector's
// heartbeat -- the heartbeat alone when there is nothing else, because the
// collector is running whether or not a session has anything new to say. A home
// another profile reads (LeaveClaudeHome) is that profile's to report. It is
// called on its own interval, apart from the record sync and its backoff. It
// first counts the uncommitted edited files of the sessions that are due
// (countSessionEdits), so new counts go in the same flush.
//
// An excluded account's item is consumed without being sent, as Drop consumes
// records; while the exclusion query fails, the items that need its answer are
// held. A 200 settles what it carried. A 404 parks the flush for
// observationsUnsupportedPark, a refused envelope for observationsEnvelopePark,
// more than maxLoneRefusals refused items for observationsRefusingPark and a 429
// until its Retry-After; any other failure leaves the rest pending for the next
// flush. Nothing here touches record sync.
func (s *Syncer) FlushObservations(ctx context.Context) error {
	now := nowFn()
	if now.Before(s.obs.parkedUntil) {
		return nil
	}
	// Checked before anything is sent: over the server's cap, every request is
	// refused whole, the heartbeat alone included, until the profile changes.
	for _, f := range []struct {
		name, value string
	}{{"profile_email", s.profileEmail}, {"user_id", s.userID}} {
		if len(f.value) > maxObservationEnvelopeBytes {
			if !s.obs.envelopeLogged {
				s.obs.envelopeLogged = true
				log.Printf("[syncer] session observations: %s is %d bytes, over the server's %d; nothing is sent until it is changed", f.name, len(f.value), maxObservationEnvelopeBytes)
			}
			return nil
		}
	}
	// Asked afresh, as a sync pass asks: a query that failed earlier is not an
	// answer.
	s.accounts.BeginPass()
	flush := &observationFlush{s: s, heartbeat: now.UTC()}
	flush.codexMarked = s.pruneCodexSnapshots(now)
	var claude []pendingObservation
	if !s.claudeLeft {
		claude = s.claudeObservations(ctx, now, flush)
	}
	pending := slices.Concat(claude, s.dirtyCodexSnapshots(flush))
	var accounts []AccountRef
	for _, p := range pending {
		if a, ok := p.account(); ok {
			accounts = append(accounts, a)
		}
	}
	resolveErr := s.accounts.resolve(ctx, accounts)
	if err := ctx.Err(); err != nil {
		return err
	}

	send := make([]pendingObservation, 0, len(pending))
	for _, p := range pending {
		if a, ok := p.account(); ok {
			switch {
			case s.client.excludedLocally(a):
				p.settle(false)
				continue
			case resolveErr != nil:
				continue
			case s.accounts.excluded(a):
				p.settle(false)
				continue
			}
		}
		send = append(send, p)
	}

	chunks := chunkObservations(send)
	if len(chunks) == 0 {
		chunks = [][]pendingObservation{nil}
	}
	var sendErr error
	for _, chunk := range chunks {
		if err := flush.send(ctx, chunk); err != nil {
			sendErr = s.parkObservations(err)
			break
		}
	}
	if flush.marked {
		if err := s.state.Save(); err != nil {
			log.Printf("[syncer] failed to save state: %v", err)
		}
	}
	if flush.codexMarked {
		if err := s.codexState.Save(); err != nil {
			log.Printf("[syncer] failed to save the Codex state: %v", err)
		}
	}
	var refusedErr, heldErr error
	if flush.refused > 0 {
		refusedErr = fmt.Errorf("the server refused %d session observation(s); each is logged with its reason and is not sent again as it was", flush.refused)
	}
	if resolveErr != nil {
		heldErr = fmt.Errorf("session observations held: %w", resolveErr)
	}
	return errors.Join(sendErr, refusedErr, heldErr)
}

// claudeObservations are this Claude home's items of a flush: its live states,
// its pending session ends and its dirty snapshots, after counting the
// uncommitted edited files and the commits of the sessions that are due.
func (s *Syncer) claudeObservations(ctx context.Context, now time.Time, flush *observationFlush) []pendingObservation {
	gitCtx, cancelGit := context.WithTimeout(ctx, sessionGitCountBudget)
	commitCtx, cancelCommits := context.WithTimeout(gitCtx, sessionGitCountBudget/2)
	commitsChanged := s.collectSessionCommits(commitCtx, now)
	cancelCommits()
	editsChanged := s.countSessionEdits(gitCtx, now)
	cancelGit()
	if commitsChanged || editsChanged {
		if err := s.state.Save(); err != nil {
			log.Printf("[syncer] failed to save state: %v", err)
		}
	}
	ends := s.sessionEnds(now)
	// A session whose end goes now is not reported alive beside it: its process
	// file can outlast the hook by a moment.
	ending := map[string]bool{}
	for _, p := range ends {
		ending[p.event.SessionID] = true
	}
	return slices.Concat(s.liveStates(now, ending), ends, s.dirtySnapshots(flush))
}

// dirtySnapshots lists, in path order, the dirty snapshots of transcripts under
// this Syncer's Claude home. Several homes can share one state file -- separate
// profiles run separate Syncers over it -- and a snapshot is attributed to its
// own home's account; another home's is left for that home's Syncer, the way
// its records are.
func (s *Syncer) dirtySnapshots(f *observationFlush) []pendingObservation {
	home := normalizeHome(s.claudeDir)
	root := filepath.Join(home, "projects") + string(filepath.Separator)
	var paths []string
	for path, fs := range s.state.Files {
		if fs != nil && fs.SessionSnapshot != nil && fs.SessionSnapshot.dirty() && strings.HasPrefix(path, root) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	pending := make([]pendingObservation, 0, len(paths))
	for _, path := range paths {
		st := s.state.Files[path].SessionSnapshot
		item := SessionSnapshotItem{
			// Every metadata line's sessionId is its transcript's name.
			SessionID:          strings.TrimSuffix(filepath.Base(path), ".jsonl"),
			Agent:              "claude",
			ProjectHash:        st.ProjectHash,
			BillingProvider:    "anthropic",
			AccountID:          s.state.ClaudeAccountAt(home, st.attributedAt()),
			ObservedAt:         st.ObservedAt,
			CollectVersion:     sessionlog.CollectVersion,
			Snapshot:           st.Snapshot,
			titlesReadRedacted: st.TitlesReadRedacted,
		}
		pending = append(pending, snapshotObservation(st, item, &f.marked))
	}
	return pending
}

// attributedAt is the time a snapshot's account is read at: its newest record.
// A file that has shown no timestamped record since its snapshot began is
// attributed at the time the snapshot changed, when its lines were read. Read at
// the zero time it would come out blank, and an excluded account's snapshot
// would go out unattributed.
func (st *SessionSnapshotState) attributedAt() time.Time {
	if st.NewestRecordAt.IsZero() {
		return st.ObservedAt
	}
	return st.NewestRecordAt
}

// snapshotObservation is a dirty snapshot's item on its way out. Settled, the
// snapshot is marked sent at the revision the item carries, so a change made
// while the request was in flight stays dirty, and *marked says the state the
// snapshot lives in needs saving.
func snapshotObservation(st *SessionSnapshotState, item SessionSnapshotItem, marked *bool) pendingObservation {
	revision := st.Revision
	return pendingObservation{snapshot: &item, size: encodedSize(item), settle: func(bool) {
		st.SentRevision = max(st.SentRevision, revision)
		*marked = true
	}}
}

// chunkObservations groups items into requests of at most
// maxObservationChunkItems items and about maxObservationChunkBytes of encoded
// items. An item bigger than the budget goes alone.
func chunkObservations(pending []pendingObservation) [][]pendingObservation {
	var chunks [][]pendingObservation
	var cur []pendingObservation
	size := 0
	for _, p := range pending {
		if len(cur) > 0 && (len(cur) == maxObservationChunkItems || size+p.size > maxObservationChunkBytes) {
			chunks = append(chunks, cur)
			cur, size = nil, 0
		}
		cur = append(cur, p)
		size += p.size
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// observationFlush is one FlushObservations call's sending.
type observationFlush struct {
	s         *Syncer
	heartbeat time.Time
	// envelopeOK is set once a request of this flush was accepted: a 400 after
	// that is about the items it carried, not the envelope.
	envelopeOK bool
	// marked says a snapshot's sent revision moved, so the state needs saving,
	// and codexMarked the same of the Codex state; refused counts the items the
	// server refused alone.
	marked      bool
	codexMarked bool
	refused     int
}

// send sends one chunk with the heartbeat. A 400 or 413 on several items splits
// the chunk in half, as sendBatch splits on a 413, until the item the server
// refuses is alone. That one is logged once, with the server's reason, and
// settled as refused: resending the same bytes cannot succeed. Past
// maxLoneRefusals such items it stops, leaving that one and the rest pending
// (errRefusingItems).
//
// Before the first split of a 400, a heartbeat alone asks whether it is the
// envelope the server refuses. If that is refused too, no item is at fault, and
// splitting would only send twice as many requests to settle every item unsent:
// it stops instead, with everything left pending (errEnvelopeRejected).
func (f *observationFlush) send(ctx context.Context, chunk []pendingObservation) error {
	resp, err := f.s.client.SendSessionObservations(ctx, f.request(chunk))
	rejected := errors.Is(err, ErrSessionObservationsRejected)
	if rejected && !f.envelopeOK {
		if len(chunk) > 0 {
			_, probeErr := f.s.client.SendSessionObservations(ctx, f.request(nil))
			switch {
			case probeErr == nil:
				f.envelopeOK = true
			case !errors.Is(probeErr, ErrSessionObservationsRejected):
				return probeErr
			}
		}
		if !f.envelopeOK {
			return fmt.Errorf("%w: %v", errEnvelopeRejected, err)
		}
	}
	refused := rejected || errors.Is(err, ErrBodyTooLarge)
	switch {
	case err == nil:
		f.envelopeOK = true
		if resp.Skipped > 0 && !f.s.obs.skippedLogged {
			f.s.obs.skippedLogged = true
			log.Printf("[syncer] session observations: the server skipped %d of %d item(s): a blocked project, a deleted session or an excluded account, or this machine's clock running more than five minutes ahead of the server's", resp.Skipped, resp.Accepted)
		}
		for _, p := range chunk {
			p.settle(false)
		}
	case refused && len(chunk) > 1:
		mid := len(chunk) / 2
		if err := f.send(ctx, chunk[:mid]); err != nil {
			return err
		}
		return f.send(ctx, chunk[mid:])
	case refused && len(chunk) == 1:
		if f.refused == maxLoneRefusals {
			return fmt.Errorf("%w: %v", errRefusingItems, err)
		}
		f.refused++
		log.Printf("[syncer] session observations: the server refused %s: %v", chunk[0].refusal(), err)
		chunk[0].settle(true)
	default:
		return err
	}
	return nil
}

func (f *observationFlush) request(chunk []pendingObservation) SessionObservationsRequest {
	heartbeat := f.heartbeat
	req := SessionObservationsRequest{ProfileEmail: f.s.profileEmail, UserID: f.s.userID, HeartbeatAt: &heartbeat}
	for _, p := range chunk {
		switch {
		case p.snapshot != nil:
			req.Snapshots = append(req.Snapshots, *p.snapshot)
		case p.liveState != nil:
			req.LiveStates = append(req.LiveStates, *p.liveState)
		case p.event != nil:
			req.Events = append(req.Events, *p.event)
		}
	}
	return req
}

// parkObservations holds the flush back after a 404, a refused envelope, too
// many refused items or a 429, and returns err for the caller to report.
func (s *Syncer) parkObservations(err error) error {
	var retryable *RetryableError
	switch {
	case errors.Is(err, ErrSessionObservationsUnsupported):
		s.obs.parkedUntil = nowFn().Add(observationsUnsupportedPark)
		return fmt.Errorf("%w; parked for %s", err, observationsUnsupportedPark)
	case errors.Is(err, errEnvelopeRejected):
		s.obs.parkedUntil = nowFn().Add(observationsEnvelopePark)
		return fmt.Errorf("%w; nothing is marked sent, parked for %s", err, observationsEnvelopePark)
	case errors.Is(err, errRefusingItems):
		s.obs.parkedUntil = nowFn().Add(observationsRefusingPark)
		return fmt.Errorf("%w; parked for %s", err, observationsRefusingPark)
	case errors.As(err, &retryable):
		s.obs.parkedUntil = nowFn().Add(retryable.RetryAfter)
	}
	return err
}
