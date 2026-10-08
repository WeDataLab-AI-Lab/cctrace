package openinsights

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPageSize = 1000
	// defaultMaxRows bounds one window's download. The server orders newest
	// first, so hitting it drops the oldest part of the window.
	defaultMaxRows = 50_000
	maxRetries     = 3
	maxRetryAfter  = 30 * time.Second
	requestTimeout = 30 * time.Second
	maxErrorBody   = 4 << 10
)

// Client reads the Open API with a per-user web token.
type Client struct {
	endpoint string
	token    string
	http     *http.Client
	wait     func(context.Context, time.Duration) error
	pageSize int
	maxRows  int
}

// NewClient returns a client for the server at endpoint.
func NewClient(endpoint, token string) *Client {
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		http:     &http.Client{Timeout: requestTimeout},
		wait:     sleepContext,
		pageSize: defaultPageSize,
		maxRows:  defaultMaxRows,
	}
}

// SetTransport installs the transport that carries a private CA, keeping the
// per-request timeout.
func (c *Client) SetTransport(rt http.RoundTripper) { c.http.Transport = rt }

// APIError is a non-2xx answer. Code carries the server's machine-readable
// reason when it sent one, such as "ingestion_token".
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("open api: HTTP %d", e.Status)
	}
	return fmt.Sprintf("open api: HTTP %d: %s", e.Status, e.Message)
}

// Project is one element of GET /api/open/v1/projects.
type Project struct {
	ProjectHash string `json:"project_hash"`
	ProjectName string `json:"project_name"`
}

// Sessions lists every session with activity in w, optionally in one project.
func (c *Client) Sessions(ctx context.Context, w Window, projectHash string) ([]Session, bool, error) {
	return fetchAll(ctx, c, "/api/open/v1/sessions", w, projectHash, func(s Session) string { return s.SessionID })
}

// Events lists every event in w, optionally in one project.
func (c *Client) Events(ctx context.Context, w Window, projectHash string) ([]Event, bool, error) {
	return fetchAll(ctx, c, "/api/open/v1/events", w, projectHash, eventKey)
}

// ModelCosts returns per-model cost over w, aggregated per request.
func (c *Client) ModelCosts(ctx context.Context, w Window) ([]ModelCost, error) {
	q := url.Values{}
	q.Set("since", w.Since.UTC().Format(time.RFC3339))
	q.Set("until", w.Until.UTC().Format(time.RFC3339))
	q.Set("group_by", "model")
	var out struct {
		Items []ModelCost `json:"items"`
	}
	err := c.get(ctx, "/api/open/v1/usage", q, &out)
	return out.Items, err
}

// Scope establishes what the token can read by asking the admin-only
// organization-insights endpoint over a one-second window: an administrator's
// aggregate over one second is cheap, and a regular user is refused before any
// query runs. Only the server's own refusal body counts as a regular user; a
// 403 from a proxy or a missing route (an older server) yields ScopeUnknown,
// which withholds detail like an administrator's.
func (c *Client) Scope(ctx context.Context) (Scope, error) {
	until := time.Now().UTC().Truncate(time.Second)
	q := url.Values{}
	q.Set("since", until.Add(-time.Second).Format(time.RFC3339))
	q.Set("until", until.Format(time.RFC3339))
	var ignored json.RawMessage
	err := c.get(ctx, "/api/open/v1/organization-insights", q, &ignored)
	var apiErr *APIError
	switch {
	case err == nil:
		return ScopeAdmin, nil
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden && apiErr.Message == "forbidden" && apiErr.Code == "":
		return ScopeUser, nil
	case errors.As(err, &apiErr) && (apiErr.Status == http.StatusForbidden || apiErr.Status == http.StatusNotFound):
		return ScopeUnknown, nil
	default:
		return ScopeUnknown, err
	}
}

// Projects lists the projects the caller may filter by.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var out struct {
		Items []Project `json:"items"`
	}
	err := c.get(ctx, "/api/open/v1/projects", nil, &out)
	return out.Items, err
}

// fetchAll pages through a bare-array endpoint. The window is sent as fixed
// bounds on every page, but a row synced into the window mid-download still
// shifts later pages, repeating a row across the page boundary; keyOf drops
// those repeats. The offset advances by rows received, not rows kept, so a
// repeat cannot stall the loop. It reports truncated when more than maxRows
// distinct rows exist.
func fetchAll[T any](ctx context.Context, c *Client, path string, w Window, projectHash string, keyOf func(T) string) ([]T, bool, error) {
	var all []T
	seen := map[string]bool{}
	offset := 0
	for {
		q := url.Values{}
		q.Set("since", w.Since.UTC().Format(time.RFC3339))
		q.Set("until", w.Until.UTC().Format(time.RFC3339))
		q.Set("limit", strconv.Itoa(c.pageSize))
		q.Set("offset", strconv.Itoa(offset))
		if projectHash != "" {
			q.Set("project_hash", projectHash)
		}
		var page []T
		if err := c.get(ctx, path, q, &page); err != nil {
			return nil, false, err
		}
		offset += len(page)
		kept := len(all)
		for _, row := range page {
			k := keyOf(row)
			if seen[k] {
				continue
			}
			seen[k] = true
			all = append(all, row)
		}
		if len(all) > c.maxRows {
			return all[:c.maxRows], true, nil
		}
		if len(page) < c.pageSize {
			return all, false, nil
		}
		if len(all) == kept {
			return nil, false, fmt.Errorf("GET %s: a full page repeated earlier rows; retry the command", path)
		}
	}
}

// eventKey identifies an event by its whole value: events carry no ID, and a
// row repeated by offset drift is identical field for field.
func eventKey(e Event) string {
	b, _ := json.Marshal(e)
	return string(b)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.endpoint + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := c.http.Do(req)
		if err != nil {
			// *url.Error prints the full URL, and project_hash in the query is a
			// filesystem path. Keep only the endpoint path.
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			return fmt.Errorf("GET %s: %w", path, err)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			delay := retryAfter(resp.Header.Get("Retry-After"))
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
			_ = resp.Body.Close()
			if err := c.wait(ctx, delay); err != nil {
				return err
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return apiError(resp)
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
		return nil
	}
}

func retryAfter(header string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || secs < 1 {
		return time.Second
	}
	if d := time.Duration(secs) * time.Second; secs < int(maxRetryAfter/time.Second) {
		return d
	}
	return maxRetryAfter
}

func apiError(resp *http.Response) error {
	e := &APIError{Status: resp.StatusCode}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxErrorBody)).Decode(&body) == nil {
		e.Code, e.Message = body.Code, body.Error
	}
	return e
}
