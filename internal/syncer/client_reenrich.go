package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type syncCapabilities struct {
	Reenrich bool `json:"reenrich"`
}

func (c *Client) SendReenrich(ctx context.Context, payload SyncPayload) (int, error) {
	if err := c.requireReenrichSupport(ctx); err != nil {
		return 0, err
	}
	payload.Reenrich = true
	resp, err := c.sendResponse(ctx, payload)
	if err != nil {
		return 0, err
	}
	return resp.Updated, nil
}

func (c *Client) requireReenrichSupport(ctx context.Context) error {
	c.reenrichMu.Lock()
	if c.reenrichOK {
		c.reenrichMu.Unlock()
		return nil
	}
	c.reenrichMu.Unlock()

	if err := c.fetchReenrichSupport(ctx); err != nil {
		return err
	}

	c.reenrichMu.Lock()
	c.reenrichOK = true
	c.reenrichMu.Unlock()
	return nil
}

func (c *Client) fetchReenrichSupport(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/sync/capabilities", nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	if c.clientVersion != "" {
		req.Header.Set("X-Cctrace-Version", c.clientVersion)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrReenrichUnsupported
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := 5 * time.Second
		if v := resp.Header.Get("Retry-After"); v != "" {
			if secs, err := strconv.Atoi(v); err == nil {
				retryAfter = time.Duration(secs) * time.Second
			}
		}
		return &RetryableError{
			Err:        fmt.Errorf("server returned 429"),
			RetryAfter: retryAfter,
		}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var caps syncCapabilities
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if !caps.Reenrich {
		return ErrReenrichUnsupported
	}
	return nil
}
