package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cctrace/internal/store"
)

// ErrExclusionsUnsupported marks a server with no /api/sync/exclusions route.
// Such a server excludes nothing a client could learn about, so sync goes on
// with the local list alone.
var ErrExclusionsUnsupported = errors.New("server does not support account exclusion queries")

// AccountRef names a billing account the way exclusions are keyed: the same id
// under two providers is two accounts.
type AccountRef struct {
	BillingProvider string `json:"billing_provider"`
	AccountID       string `json:"account_id"`
}

// Key is the provider:account_id spelling, the one options.exclude_accounts
// and `cctrace status` use.
func (a AccountRef) Key() string { return a.BillingProvider + ":" + a.AccountID }

// SetExcludedAccounts installs options.exclude_accounts. Held on the client for
// the same reason the redact policy is: every syncer shares it and none can
// forget it. An entry is provider:account_id, or a bare id that matches the id
// under any provider.
func (c *Client) SetExcludedAccounts(entries []string) {
	c.excludeAccounts = make(map[string]bool, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if provider, id, ok := strings.Cut(e, ":"); ok {
			e = strings.ToLower(strings.TrimSpace(provider)) + ":" + strings.TrimSpace(id)
		}
		if e != "" {
			c.excludeAccounts[e] = true
		}
	}
}

func (c *Client) excludedLocally(a AccountRef) bool {
	return c.excludeAccounts[a.Key()] || c.excludeAccounts[a.AccountID]
}

// QueryExcludedAccounts asks the server which of accounts are excluded from
// collection. Only the named accounts are answered for; the server's list names
// other people's accounts and is never downloaded.
func (c *Client) QueryExcludedAccounts(ctx context.Context, accounts []AccountRef) ([]AccountRef, error) {
	body, err := encodeRequestJSON(map[string]any{"accounts": accounts})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	req, err := c.newJSONPostRequest(ctx, "/api/sync/exclusions", body)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrExclusionsUnsupported
	case http.StatusTooManyRequests:
		retryAfter := 5 * time.Second
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			retryAfter = min(time.Duration(min(secs, maxRetryAfterSeconds))*time.Second, maxRetryAfterDelay)
		}
		return nil, &RetryableError{Err: fmt.Errorf("server returned 429"), RetryAfter: retryAfter}
	default:
		return nil, fmt.Errorf("exclusion query: server returned %d", resp.StatusCode)
	}
	var out struct {
		Accounts []AccountRef `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return out.Accounts, nil
}

// AccountFilter keeps an excluded account's records from being sent (#715,
// #716). One per syncer, so a pass asks the server once per account it saw
// rather than once per record or file.
type AccountFilter struct {
	client *Client
	state  *State
	// asked holds the server's answers by AccountRef.Key, each reused for
	// exclusionAnswerTTL so an exclusion added or lifted on the server applies
	// without a restart.
	asked map[string]exclusionAnswer
	// unsupportedUntil parks the query after a 404 (exclusionsUnsupportedPark):
	// asking every pass would only repeat it.
	unsupportedUntil time.Time
	// passErr is this pass's failed query. Files that need an answer are held
	// on it without asking again: every file of the pass would otherwise repeat
	// the same failing request.
	passErr error
}

// maxExclusionQueryAccounts is the server's per-query cap; it refuses a larger
// query whole, so the filter asks in chunks of this size.
const maxExclusionQueryAccounts = 100

// exclusionsUnsupportedPark is how long a 404 stops the filter from asking.
// Not for good: during a rolling deploy one old instance can answer 404 while
// the rest have the route, and a latch would leave server exclusions unapplied
// until the daemon restarts.
const exclusionsUnsupportedPark = time.Hour

// exclusionAnswerTTL is how long the server's answer about one account is
// reused. Watch mode polls every second and the query shares /api/sync's rate
// bucket, so asking every pass is too often; an exclusion lifted meanwhile
// applies a few minutes late, and one added meanwhile is refused at ingest.
const exclusionAnswerTTL = 5 * time.Minute

type exclusionAnswer struct {
	excluded bool
	at       time.Time
}

func NewAccountFilter(client *Client, state *State) *AccountFilter {
	return &AccountFilter{client: client, state: state}
}

// BeginPass forgets the previous pass's failed query, so this pass asks again.
func (f *AccountFilter) BeginPass() {
	if f != nil {
		f.passErr = nil
	}
}

// Drop returns records without those of excluded accounts. defaultProvider
// stands in for a record that carries no billing_provider, the way the server
// fills it from the agent. A record with no account id is never dropped: that
// is the absence of an identity, not a match.
//
// A failed query is returned as an error rather than read as "nothing
// excluded": sending on a guess would put an excluded account's conversation
// on the wire, and the caller already holds the file for a failed send.
func (f *AccountFilter) Drop(ctx context.Context, defaultProvider string, records []*store.SessionRecord) ([]*store.SessionRecord, error) {
	if f == nil || f.client == nil {
		return records, nil
	}
	refOf := func(r *store.SessionRecord) (AccountRef, bool) {
		if r.AccountID == "" {
			return AccountRef{}, false
		}
		provider := r.BillingProvider
		if provider == "" {
			provider = defaultProvider
		}
		return AccountRef{BillingProvider: strings.ToLower(provider), AccountID: r.AccountID}, true
	}

	var unknown []AccountRef
	pending := map[string]bool{}
	for _, r := range records {
		a, ok := refOf(r)
		if !ok || f.client.excludedLocally(a) || pending[a.Key()] {
			continue
		}
		if ans, known := f.asked[a.Key()]; known && nowFn().Sub(ans.at) < exclusionAnswerTTL {
			continue
		}
		pending[a.Key()] = true
		unknown = append(unknown, a)
	}
	for len(unknown) > 0 && !nowFn().Before(f.unsupportedUntil) {
		if f.passErr != nil {
			return nil, f.passErr
		}
		chunk := unknown[:min(len(unknown), maxExclusionQueryAccounts)]
		unknown = unknown[len(chunk):]
		excluded, err := f.client.QueryExcludedAccounts(ctx, chunk)
		if errors.Is(err, ErrExclusionsUnsupported) {
			f.unsupportedUntil = nowFn().Add(exclusionsUnsupportedPark)
			log.Printf("[syncer] server has no /api/sync/exclusions endpoint; only options.exclude_accounts applies for %s", exclusionsUnsupportedPark)
			break
		}
		if err != nil {
			f.passErr = err
			return nil, err
		}
		if f.asked == nil {
			f.asked = map[string]exclusionAnswer{}
		}
		now := nowFn()
		for _, a := range chunk {
			f.asked[a.Key()] = exclusionAnswer{at: now}
		}
		for _, a := range excluded {
			f.asked[a.Key()] = exclusionAnswer{excluded: true, at: now}
		}
		f.state.NoteServerExclusions(chunk, excluded, now)
	}

	kept := make([]*store.SessionRecord, 0, len(records))
	for _, r := range records {
		if a, ok := refOf(r); ok && (f.client.excludedLocally(a) || f.asked[a.Key()].excluded) {
			continue
		}
		kept = append(kept, r)
	}
	return kept, nil
}
