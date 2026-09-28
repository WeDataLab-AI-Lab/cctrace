package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"time"

	"cctrace/internal/store"

	"golang.org/x/time/rate"
)

// ErrProjectRulesUnsupported is returned when the server has no
// /api/project-rules endpoint (a newer client talking to an older server).
// Callers should stop attempting rule sync rather than treat it as an error.
var ErrProjectRulesUnsupported = errors.New("server does not support project rules endpoint")

// ErrProjectRulesForbidden is returned when the server denies the request (403).
// The caller should back off for this repository rather than retrying every cycle.
var ErrProjectRulesForbidden = errors.New("server denied project rules access for this repository")

var ErrReenrichUnsupported = errors.New("server does not support session reenrich")

// ErrQuotaSamplesUnsupported is returned when the server has no
// /api/quota-samples endpoint. The snapshot route still works, so the caller
// should stop sending history rather than treat collection as broken.
var ErrQuotaSamplesUnsupported = errors.New("server does not support quota samples endpoint")

// ErrBodyTooLarge marks a 413. It is not a RetryableError: resending the same
// bytes cannot succeed. Send splits the batch on it, which is the only response
// that makes progress -- a batch is a client-side grouping, so half of one is
// still a valid request.
var ErrBodyTooLarge = errors.New("server refused the request body as too large")

// ErrTransferTimeout marks an upload that ran out of time rather than being
// refused. Kept separate from ErrBodyTooLarge because the response differs: a
// refusal is answered by splitting the batch, a timeout by looking at the link
// or the server, and conflating them sends the reader to the wrong place.
var ErrTransferTimeout = errors.New("request body did not transfer before the deadline")

// Client sends session records to the cctraced server.
type Client struct {
	endpoint      string
	authToken     string
	clientVersion string
	http          *http.Client
	limiter       *rate.Limiter // client-side throttle (100 RPS, burst 200)
	reenrichMu    sync.Mutex
	reenrichOK    bool
	// redact is applied to every batch on its way out. Held on the client, not
	// passed per call, so a new caller cannot forget it.
	redact RedactPolicy
	// excludeAccounts is options.exclude_accounts, keyed as SetExcludedAccounts
	// normalises it.
	excludeAccounts map[string]bool
	// updateStall reports this install's self-update state on every payload it
	// sends. Nil means this build does not report at all, which the server
	// stores differently from a report saying nothing is wrong (#750).
	updateStall func() *store.ClientUpdateStall
}

// SetUpdateStallReporter installs the source of the self-update report. It is a
// function rather than a value because the state changes while the process
// lives: the daemon runs for days, and an update can fail, or finally land,
// between one sync and the next.
func (c *Client) SetUpdateStallReporter(fn func() *store.ClientUpdateStall) { c.updateStall = fn }

// SetRedactPolicy installs the client-side privacy policy. Callers set it from the
// profile once, at construction; records are scrubbed in sendResponse for both normal
// sync and reenrich, regardless of which agent produced them.
func (c *Client) SetRedactPolicy(p RedactPolicy) { c.redact = p }

// maxRetryAfterDelay caps a parsed Retry-After, and maxRetryAfterSeconds is the
// same bound expressed in whole seconds so the header can be range-checked
// before it is multiplied into a time.Duration.
const (
	maxRetryAfterDelay   = 24 * time.Hour
	maxRetryAfterSeconds = int(maxRetryAfterDelay / time.Second)
)

// RetryableError indicates that the request can be retried after a delay.
type RetryableError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *RetryableError) Error() string {
	return fmt.Sprintf("%v (retry after %v)", e.Err, e.RetryAfter)
}

func (e *RetryableError) Unwrap() error { return e.Err }

// NewClient creates a new sync client.
func NewClient(endpoint, authToken, clientVersion string) *Client {
	return &Client{
		endpoint:      endpoint,
		authToken:     authToken,
		clientVersion: clientVersion,
		// The ceiling, not the working deadline. Each upload sets its own from
		// transferDeadline(); this only keeps a request that somehow escapes that
		// from hanging forever.
		http:    &http.Client{Timeout: maxTransferDeadline},
		limiter: rate.NewLimiter(100, 200),
	}
}

func (c *Client) newJSONPostRequest(ctx context.Context, path string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	if c.clientVersion != "" {
		req.Header.Set("X-Cctrace-Version", c.clientVersion)
	}
	req.Header.Set("X-Cctrace-Os", runtime.GOOS)
	req.Header.Set("X-Cctrace-Arch", runtime.GOARCH)
	return req, nil
}

// SyncPayload is the JSON envelope sent to /api/sync.
type SyncPayload struct {
	ProfileEmail       string `json:"profile_email"`
	LoginEmail         string `json:"login_email,omitempty"`
	UserID             string `json:"user_id"`
	Agent              string `json:"agent,omitempty"`
	ProjectHash        string `json:"project_hash"`
	ProjectName        string `json:"project_name,omitempty"`
	GitRemoteURL       string `json:"git_remote_url,omitempty"`
	RepositoryID       string `json:"repository_id,omitempty"`
	RepositoryIDSource string `json:"repository_id_source,omitempty"`
	RepositoryName     string `json:"repository_name,omitempty"`
	RepoSubpath        string `json:"repo_subpath,omitempty"`
	RepoSubpathPresent bool   `json:"repo_subpath_present"`
	CommitSHA          string `json:"commit_sha,omitempty"`
	Branch             string `json:"branch,omitempty"`
	Reenrich           bool   `json:"reenrich,omitempty"`
	// UpdateStall rides the sync the client was already making. Absent means the
	// client does not report; present but empty means it reports no failure.
	UpdateStall *store.ClientUpdateStall `json:"update_stall,omitempty"`
	Records     []*store.SessionRecord   `json:"records"`
}

// ProjectIdentity is the metadata portion of a live sync envelope. Keeping it
// together prevents a caller from silently dropping authority/presence fields
// while still leaving the agent and record scope explicit at the call site.
type ProjectIdentity struct {
	GitRemoteURL       string
	RepositoryID       string
	RepositoryIDSource string
	RepositoryName     string
	RepoSubpath        string
	RepoSubpathPresent bool
	CommitSHA          string
	Branch             string
}

type syncResponse struct {
	Inserted int `json:"inserted"`
	Updated  int `json:"updated"`
}

// SendProjectRules posts discovered repository rule snapshots to the server.
func (c *Client) SendProjectRules(ctx context.Context, req *store.ProjectRuleIngestRequest) (*store.ProjectRuleIngestResponse, error) {
	if req == nil || len(req.Rules) == 0 {
		return &store.ProjectRuleIngestResponse{}, nil
	}
	if err := c.limiter.Wait(ctx); err != nil {
		if ctxErr := rateLimitContextErr(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("rate limiter: %w", err)
	}
	body, err := encodeRequestJSON(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := c.newJSONPostRequest(ctx, "/api/project-rules", body)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := 5 * time.Second
		if v := resp.Header.Get("Retry-After"); v != "" {
			if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
				// Saturate rather than multiply blindly: a large enough value
				// overflows time.Duration and comes back as a small or negative
				// delay, turning "wait a long time" into "retry immediately".
				if secs > maxRetryAfterSeconds {
					retryAfter = maxRetryAfterDelay
				} else {
					retryAfter = time.Duration(secs) * time.Second
				}
			}
		}
		return nil, &RetryableError{
			Err:        fmt.Errorf("server returned 429"),
			RetryAfter: retryAfter,
		}
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrProjectRulesUnsupported
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, ErrProjectRulesForbidden
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var out store.ProjectRuleIngestResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &out, nil
}

// QuotaPayload carries per-user rate-limit utilization from the Anthropic OAuth usage API.
type QuotaPayload struct {
	ProfileEmail           string  `json:"profile_email"`
	UserID                 string  `json:"user_id,omitempty"`
	FiveHourPct            float64 `json:"five_hour_pct"`
	FiveHourResetsAt       string  `json:"five_hour_resets_at,omitempty"`
	SevenDayPct            float64 `json:"seven_day_pct"`
	SevenDayResetsAt       string  `json:"seven_day_resets_at,omitempty"`
	SevenDaySonnetPct      float64 `json:"seven_day_sonnet_pct,omitempty"`
	SevenDaySonnetResetsAt string  `json:"seven_day_sonnet_resets_at,omitempty"`
}

// QuotaSamplesPayload carries rate-limit history rows to the server.
//
// This is sent alongside the snapshot rather than instead of it. The snapshot
// endpoint is what an older server offers, so a client that finds no route here
// keeps feeding that one and loses only the history, not the current reading.
type QuotaSamplesPayload struct {
	Samples []*store.QuotaSample `json:"samples"`
}

// SendQuotaSamples posts rate-limit history. A 404 is reported as
// ErrQuotaSamplesUnsupported so the caller can tell an older server from a broken one.
func (c *Client) SendQuotaSamples(ctx context.Context, samples []*store.QuotaSample) error {
	if len(samples) == 0 {
		return nil
	}
	body, err := encodeRequestJSON(QuotaSamplesPayload{Samples: samples})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := c.newJSONPostRequest(ctx, "/api/quota-samples", body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrQuotaSamplesUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// SendQuota posts a quota snapshot to the server.
func (c *Client) SendQuota(ctx context.Context, q *QuotaPayload) error {
	body, err := encodeRequestJSON(q)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := c.newJSONPostRequest(ctx, "/api/quota", body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return nil
}

// CheckVersion fetches the server's current version string.
func (c *Client) CheckVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/version", nil)
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("server returned %d", resp.StatusCode)
	}
	var vr struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	return vr.Version, nil
}

// Send posts a batch of session records to the server, halving the batch and
// retrying whenever the server refuses the body as too large.
//
// The client groups records by count, not by bytes, so a run of large records
// builds a body no configured limit was chosen for. Without splitting, the whole
// batch fails, the caller does not advance, and every later record in that file
// stops reaching the server -- one oversized run costing the rest of the stream.
//
// Splitting is preferred over asking the server for its limit: a proxy may
// impose a lower one than the server advertises, and an older server advertises
// nothing. A 413 is the only reliable statement of the limit in force.
func (c *Client) Send(ctx context.Context, agent, profileEmail, userID, projectHash, projectName string, identity ProjectIdentity, records []*store.SessionRecord) (int, error) {
	sent, err := c.sendBatch(ctx, agent, profileEmail, userID, projectHash, projectName, identity, records)
	if err != nil {
		return sent, err
	}
	return sent, nil
}

// sendBatch sends one group, splitting it in half on a 413 until either the
// halves fit or a single record is left. A lone record cannot be split, so the
// error travels up: the bytes are still on disk and a raised limit recovers them,
// which stays true only while the caller declines to advance past them.
func (c *Client) sendBatch(ctx context.Context, agent, profileEmail, userID, projectHash, projectName string, identity ProjectIdentity, records []*store.SessionRecord) (int, error) {
	sent, err := c.sendOne(ctx, agent, profileEmail, userID, projectHash, projectName, identity, records)
	if err == nil || !errors.Is(err, ErrBodyTooLarge) || len(records) < 2 {
		return sent, err
	}
	mid := len(records) / 2
	first, err := c.sendBatch(ctx, agent, profileEmail, userID, projectHash, projectName, identity, records[:mid])
	if err != nil {
		return first, err
	}
	second, err := c.sendBatch(ctx, agent, profileEmail, userID, projectHash, projectName, identity, records[mid:])
	return first + second, err
}

func (c *Client) sendOne(ctx context.Context, agent, profileEmail, userID, projectHash, projectName string, identity ProjectIdentity, records []*store.SessionRecord) (int, error) {
	return c.send(ctx, SyncPayload{
		Agent:              agent,
		ProfileEmail:       profileEmail,
		UserID:             userID,
		ProjectHash:        projectHash,
		ProjectName:        projectName,
		GitRemoteURL:       identity.GitRemoteURL,
		RepositoryID:       identity.RepositoryID,
		RepositoryIDSource: identity.RepositoryIDSource,
		RepositoryName:     identity.RepositoryName,
		RepoSubpath:        identity.RepoSubpath,
		RepoSubpathPresent: identity.RepoSubpathPresent,
		CommitSHA:          identity.CommitSHA,
		Branch:             identity.Branch,
		Records:            records,
	})
}

func (c *Client) send(ctx context.Context, payload SyncPayload) (int, error) {
	// Filled here rather than at each call site so a new one cannot forget it,
	// the same reason redact lives on the client.
	if payload.UpdateStall == nil && c.updateStall != nil {
		payload.UpdateStall = c.updateStall()
	}
	sr, err := c.sendResponse(ctx, payload)
	if err != nil {
		return 0, err
	}
	return sr.Inserted, nil
}

func (c *Client) sendResponse(ctx context.Context, payload SyncPayload) (syncResponse, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		if ctxErr := rateLimitContextErr(ctx); ctxErr != nil {
			return syncResponse{}, ctxErr
		}
		return syncResponse{}, fmt.Errorf("rate limiter: %w", err)
	}
	// Apply the policy once for every /api/sync upload, including reenrich.
	Redact(payload.Records, c.redact)
	body, err := encodeRequestJSON(payload)
	if err != nil {
		return syncResponse{}, fmt.Errorf("marshal: %w", err)
	}

	// Sized to the body: see transferDeadline. Derived here rather than in the
	// caller because this is where the encoded length is known -- redaction and
	// the envelope have both been applied by now, so these are the bytes that go
	// on the wire.
	ctx, cancel := context.WithTimeout(ctx, transferDeadline(int64(len(body))))
	defer cancel()

	req, err := c.newJSONPostRequest(ctx, "/api/sync", body)
	if err != nil {
		return syncResponse{}, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Named apart from a refusal. A 413 says the server decided; this says
		// nobody decided, and the two need different answers -- split the batch
		// versus look at the link. Without the distinction both arrive as
		// "http post: ..." and read like an unreachable server.
		if errors.Is(err, context.DeadlineExceeded) {
			return syncResponse{}, fmt.Errorf("%w: %d bytes did not transfer within %s", ErrTransferTimeout, len(body), transferDeadline(int64(len(body))))
		}
		return syncResponse{}, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := 5 * time.Second // default
		if v := resp.Header.Get("Retry-After"); v != "" {
			if secs, err := strconv.Atoi(v); err == nil {
				retryAfter = time.Duration(secs) * time.Second
			}
		}
		return syncResponse{}, &RetryableError{
			Err:        fmt.Errorf("server returned 429"),
			RetryAfter: retryAfter,
		}
	}
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		return syncResponse{}, ErrBodyTooLarge
	}
	if resp.StatusCode != http.StatusOK {
		return syncResponse{}, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var sr syncResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return syncResponse{}, fmt.Errorf("decode: %w", err)
	}
	return sr, nil
}

// Requests are JSON, not embedded HTML. Keep HTML characters literal to avoid
// expanding large rule contents and identifiers without changing JSON values.
func encodeRequestJSON(v any) ([]byte, error) {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}
