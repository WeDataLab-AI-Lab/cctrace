// Package codexappserver reads a Codex account's live rate-limit buckets by
// running `codex app-server` and asking it over JSON-RPC on stdio.
//
// The session JSONL already carries a rate-limit reading on every turn, and
// internal/codexlog collects it without spawning anything. This package exists
// because that reading is one bucket. The payload carries a single limit_id —
// "codex", whose windows depend on the plan (weekly alone on Pro, five-hour and
// weekly on Plus) — while the account also meters model-scoped buckets under
// other ids. Nothing on disk records them.
//
// The app-server's account/rateLimits/read answers with rateLimitsByLimitId,
// which is the whole set. There is no way to reach it without a process:
// `codex app-server daemon start` would give a reusable control socket, but it
// requires a separate standalone install that cctrace has no business putting
// on a user's machine, so every read starts a child and ends it.
//
// That makes process lifetime the load-bearing property here, not the parsing.
// A quota poller on this repository once collapsed from a five-minute interval
// into a request per second and issued 1,293,935 of them; the same collapse on
// this path would leave processes behind rather than requests. Everything below
// is arranged so that cannot happen: one child per Fetch, killed and reaped on
// every exit path by a single deferred teardown, a hard timeout so no read can
// hold the poll open, a byte budget so no output can be read without bound, and
// a cache that holds failures as well as successes so a broken installation
// costs one process per interval instead of one per call.
package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// cacheTTL matches the usage poller's: a reading older than this is worth
	// taking again, and one younger is not worth a process.
	cacheTTL = 5 * time.Minute

	// failureCacheTTL keeps a failure from being retried immediately. A machine
	// with no codex installed, or one that is logged out, answers the same way
	// every time, and re-asking on every call is how a fixed interval turns
	// into an unbounded rate.
	failureCacheTTL = time.Minute

	// clientName and clientVersion identify this client in the handshake. The
	// server records them; nothing branches on them.
	clientName    = "cctrace"
	clientVersion = "1"

	// maxMessages bounds how many lines are read while waiting for one reply.
	// The exchange is three requests, so anything beyond this is the server
	// talking to itself.
	maxMessages = 512

	// Replies are matched by id because notifications arrive interleaved with
	// responses, and reading by arrival order would take one for the other.
	initializeID   = 1
	rateLimitsID   = 2
	accountID      = 3
	rateLimitsCall = "account/rateLimits/read"
	accountCall    = "account/read"
)

// Seams for tests. Each is a real dependency of the process path — where the
// binary is, how long a read may take, how much it may say, and what time it is
// — and none of them can be substituted from outside the package otherwise.
var (
	lookPathFn       = exec.LookPath
	nowFn            = time.Now
	fetchTimeout     = 20 * time.Second
	maxResponseBytes = int64(8 << 20)
)

// Reading is one rate-limit window of one bucket at one instant.
type Reading struct {
	// LimitID is the bucket this window belongs to ("codex", or a model-scoped
	// id such as "codex_bengalfox"). It is part of the window's identity: two
	// buckets report windows of the same length, so the length alone does not
	// say which meter a reading came off.
	LimitID string
	// LimitName is the server's display name for the bucket, when it gave one.
	LimitName string
	PlanType  string

	UsedPercent float64
	// WindowMinutes is the window's length: 300 for a five-hour window, 10080
	// for a weekly one.
	WindowMinutes int
	ResetsAt      *time.Time
}

// Snapshot is one account/rateLimits/read result, flattened to one Reading per
// reported window.
type Snapshot struct {
	// FetchedAt is when the answer came back, and it is the sampled_at of the
	// history rows built from it. The cache hands the same Snapshot back
	// repeatedly; stamping it here rather than at send time is what lets the
	// primary key drop those repeats instead of storing a flat line.
	FetchedAt time.Time
	// LoginEmail comes from account/read, which exposes account metadata without
	// making cctrace parse or forward the live tokens in auth.json.
	LoginEmail string
	Readings   []Reading
}

// cached holds the last outcome — success or failure — and how long it stands.
type cached struct {
	snap      *Snapshot
	err       error
	fetchedAt time.Time
	ttl       time.Duration
}

func (c *cached) isStale(now time.Time) bool {
	return c == nil || now.Sub(c.fetchedAt) >= c.ttl
}

// cache is keyed on the Codex home. One machine runs several holding different
// accounts, so a shared entry would report one account's meter under another's
// name — a wrong value rather than a missing one.
var (
	cacheMu sync.Mutex
	cache   = map[string]*cached{}
)

func clearCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache = map[string]*cached{}
	modelCacheMu.Lock()
	defer modelCacheMu.Unlock()
	modelCache = map[string]*cachedModels{}
}

// Fetch returns the live rate-limit buckets for one Codex home, from an
// in-memory cache, refreshing it when stale.
//
// The lock is held across the child's whole lifetime on purpose. Concurrent
// callers wait rather than each starting a process, so the number of live
// children is one regardless of how many goroutines ask.
func Fetch(ctx context.Context, codexHome string) (*Snapshot, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	now := nowFn()
	if c := cache[codexHome]; !c.isStale(now) {
		return c.snap, c.err
	}
	snap, err := run(ctx, codexHome, now)
	if err != nil {
		cache[codexHome] = &cached{err: err, fetchedAt: now, ttl: failureCacheTTL}
		return nil, err
	}
	cache[codexHome] = &cached{snap: snap, fetchedAt: now, ttl: cacheTTL}
	return snap, nil
}

// run starts one app-server, reads quota plus account metadata, and ends it.
func run(ctx context.Context, codexHome string, now time.Time) (*Snapshot, error) {
	bin, err := lookPathFn("codex")
	if err != nil {
		return nil, fmt.Errorf("codex CLI not found on PATH: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	cmd := exec.Command(bin, "app-server")
	// CODEX_HOME is the only way codex is told which home to answer for.
	// Reading the meter from the default home while labelling the row with a
	// profile's account does not leave a field blank, it fills it with the
	// wrong account.
	// The same allowlist as a report run: the daemon's secrets do not reach it.
	cmd.Env = childEnv(RuntimeConfig{Home: codexHome}, "")
	// The child's stderr is dropped rather than inherited: it is diagnostic
	// chatter on the daemon's own stream, and buffering it here would be an
	// unbounded allocation for output nothing reads.
	cmd.Stderr = nil
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}

	// One teardown, on every path out of this function — reply, protocol error,
	// timeout, panic. There is no other exit, and it is the only place Wait is
	// called, so the child is reaped exactly once and never left as a zombie.
	//
	// SIGKILL, not SIGTERM: the answer is already in hand by the time this
	// runs, so there is nothing a graceful shutdown could still produce, and an
	// uncatchable signal is what makes the wait below bounded. It goes to the
	// process group so a child of the child cannot outlive its parent, and it
	// is sent before Wait — a group is addressed by the leader's pid, and after
	// the leader is reaped that number can belong to somebody else's process.
	defer func() {
		_ = stdin.Close()
		killGroup(cmd.Process)
		_ = cmd.Wait()
	}()

	type outcome struct {
		snap *Snapshot
		err  error
	}
	// Buffered, so the goroutine can finish and exit even after this function
	// has stopped listening on a timeout.
	done := make(chan outcome, 1)
	go func() {
		snap, err := exchange(stdin, stdout, now)
		done <- outcome{snap, err}
	}()

	select {
	case o := <-done:
		return o.snap, o.err
	case <-ctx.Done():
		// The deferred teardown kills the child, which closes the pipe and
		// releases the reader goroutine. Nothing is left running behind this
		// return.
		return nil, fmt.Errorf("codex app-server did not answer within %s", fetchTimeout)
	}
}

// exchange performs the handshake and reads quota plus optional account metadata.
func exchange(w io.Writer, r io.Reader, now time.Time) (*Snapshot, error) {
	// The budget bounds every read below at once: past it the reader sees EOF
	// and the wait for a reply fails, rather than growing without limit
	// alongside a server that will not stop talking.
	br := bufio.NewReader(io.LimitReader(r, maxResponseBytes))

	if err := writeMessage(w, map[string]any{
		"jsonrpc": "2.0",
		"id":      initializeID,
		"method":  "initialize",
		"params": map[string]any{
			"clientInfo": map[string]any{"name": clientName, "version": clientVersion},
		},
	}); err != nil {
		return nil, err
	}
	if _, err := awaitResult(br, initializeID); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := writeMessage(w, map[string]any{"jsonrpc": "2.0", "method": "initialized"}); err != nil {
		return nil, err
	}
	// Ask for optional account metadata first, then the required quota. Reading
	// until the quota reply lets an older app-server ignore account/read without
	// holding the poll open, while an implementation that answers in request
	// order enriches the sample with the actual ChatGPT login email.
	if err := writeMessage(w, map[string]any{
		"jsonrpc": "2.0", "id": accountID, "method": accountCall, "params": map[string]any{},
	}); err != nil {
		return nil, err
	}
	if err := writeMessage(w, map[string]any{
		"jsonrpc": "2.0", "id": rateLimitsID, "method": rateLimitsCall,
	}); err != nil {
		return nil, err
	}
	raw, accountRaw, err := awaitRateLimits(br)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rateLimitsCall, err)
	}

	var resp rawRateLimitsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode %s: %w", rateLimitsCall, err)
	}
	snap := &Snapshot{FetchedAt: now, Readings: resp.readings()}

	// account/read is a safe metadata source: auth.json remains restricted to
	// account_id and its token claims are never parsed or forwarded here.
	var accountResp struct {
		Account *struct {
			Email string `json:"email"`
		} `json:"account"`
	}
	if json.Unmarshal(accountRaw, &accountResp) == nil && accountResp.Account != nil {
		snap.LoginEmail = accountResp.Account.Email
	}
	return snap, nil
}

func writeMessage(w io.Writer, msg map[string]any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write to codex app-server: %w", err)
	}
	return nil
}

// awaitResult reads until the reply carrying id arrives.
//
// Anything else on the stream is skipped rather than treated as the answer:
// notifications are emitted whenever the server has something to say, and one
// of them landing between the request and its reply is ordinary.
func awaitResult(br *bufio.Reader, id int) (json.RawMessage, error) {
	for i := 0; i < maxMessages; i++ {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("codex app-server exited without answering")
			}
			return nil, err
		}
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if jsonErr := json.Unmarshal(trimLine(line), &msg); jsonErr != nil {
			// A line that is not JSON at all means this is not the protocol
			// stream we think it is; reading on would be guessing.
			return nil, fmt.Errorf("unparseable output from codex app-server: %w", jsonErr)
		}
		// A server request carries an id from the server's own counter and can
		// share a number with ours; only a message without a method is a reply.
		if msg.ID == nil || *msg.ID != id || msg.Method != "" {
			if err != nil {
				return nil, errors.New("codex app-server exited without answering")
			}
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("codex app-server error %d: %s", msg.Error.Code, msg.Error.Message)
		}
		return msg.Result, nil
	}
	return nil, fmt.Errorf("no reply to request %d within %d messages", id, maxMessages)
}

// awaitRateLimits waits for the required quota reply while retaining account
// metadata if it arrived first. account/read is optional: an older server can
// ignore it and the first quota reply still completes the exchange.
func awaitRateLimits(br *bufio.Reader) (json.RawMessage, json.RawMessage, error) {
	var accountRaw json.RawMessage
	for i := 0; i < maxMessages; i++ {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil, errors.New("codex app-server exited without answering")
			}
			return nil, nil, err
		}
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if jsonErr := json.Unmarshal(trimLine(line), &msg); jsonErr != nil {
			return nil, nil, fmt.Errorf("unparseable output from codex app-server: %w", jsonErr)
		}
		if msg.ID == nil {
			continue
		}
		switch *msg.ID {
		case accountID:
			if msg.Error == nil {
				accountRaw = msg.Result
			}
		case rateLimitsID:
			if msg.Error != nil {
				return nil, nil, fmt.Errorf("codex app-server error %d: %s", msg.Error.Code, msg.Error.Message)
			}
			return msg.Result, accountRaw, nil
		}
		if err != nil {
			return nil, nil, errors.New("codex app-server exited without answering")
		}
	}
	return nil, nil, fmt.Errorf("no reply to request %d within %d messages", rateLimitsID, maxMessages)
}

func trimLine(b []byte) []byte {
	return []byte(strings.TrimRight(string(b), "\r\n"))
}

// rawRateLimitsResponse is GetAccountRateLimitsResponse, declaring only the
// fields the history rows need. The response also carries credits and reset
// grants, which nothing here charts.
type rawRateLimitsResponse struct {
	RateLimits          *rawSnapshot           `json:"rateLimits"`
	RateLimitsByLimitID map[string]rawSnapshot `json:"rateLimitsByLimitId"`
}

type rawSnapshot struct {
	LimitID   string     `json:"limitId"`
	LimitName string     `json:"limitName"`
	PlanType  string     `json:"planType"`
	Primary   *rawWindow `json:"primary"`
	Secondary *rawWindow `json:"secondary"`
}

type rawWindow struct {
	UsedPercent float64 `json:"usedPercent"`
	// WindowDurationMins is nullable in the schema. A JSON null leaves this
	// zero, and a window with no declared length is dropped below rather than
	// filed against a series it might not belong to.
	WindowDurationMins int   `json:"windowDurationMins"`
	ResetsAt           int64 `json:"resetsAt"` // unix epoch seconds
}

// readings flattens the response to one Reading per reported window.
//
// rateLimitsByLimitId is the whole point of this path and is read first. The
// single-bucket rateLimits field is the floor, for the same reason the Claude
// poller keeps its legacy fields: the map cannot be assumed present on every
// codex version, and the worst case has to equal what the session log already
// gives rather than nothing at all.
func (r *rawRateLimitsResponse) readings() []Reading {
	var out []Reading
	if len(r.RateLimitsByLimitID) > 0 {
		// Map iteration order is random and the rows are keyed, not positional,
		// so nothing downstream depends on the order they come out in.
		for id, snap := range r.RateLimitsByLimitID {
			// The map key is authoritative. Some app-server versions repeat
			// the generic "codex" id inside model-scoped entries; trusting the
			// duplicate would relabel their five-hour meter as generic Codex
			// and merge it with a different bucket downstream.
			snap.LimitID = id
			out = append(out, snap.readings()...)
		}
		return out
	}
	if r.RateLimits != nil {
		out = append(out, r.RateLimits.readings()...)
	}
	return out
}

// readings converts one bucket's two window slots.
//
// primary and secondary are read as an unordered pair, as the session-log
// parser reads them: the position does not fix which length appears where, and
// filing by position would put weekly percentages on the five-hour line.
func (s *rawSnapshot) readings() []Reading {
	var out []Reading
	for _, w := range []*rawWindow{s.Primary, s.Secondary} {
		if w == nil || w.WindowDurationMins <= 0 {
			continue
		}
		reading := Reading{
			LimitID:       s.LimitID,
			LimitName:     s.LimitName,
			PlanType:      s.PlanType,
			UsedPercent:   w.UsedPercent,
			WindowMinutes: w.WindowDurationMins,
		}
		if w.ResetsAt > 0 {
			t := time.Unix(w.ResetsAt, 0).UTC()
			reading.ResetsAt = &t
		}
		out = append(out, reading)
	}
	return out
}
