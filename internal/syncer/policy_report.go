package syncer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"cctrace/internal/store"
)

// SendPolicyReport posts what this install says about itself as a recipient of
// signed server policies to /api/client-policy-report.
//
// 204 is success. 404 is a server older than the route and is not an error: a
// client updates before or after its server, some servers will never carry the
// route, and the caller runs every five minutes -- an error here would be a log
// line nobody can act on, twelve times an hour. Anything else is returned with
// the server's own words, which for a 400 name the field it refused.
//
// It does not wait on the upload rate limiter. That paces a pass that sends
// hundreds of bodies; this is one small request per policy check.
func (c *Client) SendPolicyReport(ctx context.Context, report store.ClientPolicyReport) error {
	body, err := encodeRequestJSON(report)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := c.newJSONPostRequest(ctx, "/api/client-policy-report", body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		return nil
	}
	reason, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("server answered %d: %s", resp.StatusCode, strings.TrimSpace(string(reason)))
}
