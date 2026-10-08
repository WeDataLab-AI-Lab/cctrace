package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// events reads /api/open/v1/events for one session.
//
// A session id is required. The endpoint will happily return events across every
// session in the window, which is a firehose nobody reads and a large query for the
// server to answer; if that is ever wanted, it should be asked for explicitly rather
// than arrived at by leaving a flag off.
func eventsCmd() *cobra.Command {
	var sessionID, profileName string
	var limit int
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show events for one session (reads the Open API)",
		Long: "Read events from /api/open/v1/events.\n\n" +
			"Uses server.read_token; create one with 'cctrace auth read' (administrators: Settings > API Access Tokens).\n" +
			"Events carry timing and token counts, not conversation text.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if profileName == "" {
				profileName = os.Getenv("CCTRACE_PROFILE")
			}
			p, err := loadProfile(profileName)
			if err != nil {
				return fmt.Errorf("load profile: %w", err)
			}
			endpoint := profileHTTPAPIEndpoint(p)
			if endpoint == "" {
				return fmt.Errorf("no sync endpoint configured; run 'cctrace init' to set one")
			}
			client, err := serverClient(p.Server.CACertFile, 0)
			if err != nil {
				return err
			}
			return runEvents(client, endpoint, openAPIToken(p), sessionID, limit, asJSON, nil)
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "Session id (required)")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum events to return")
	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile to use (overrides CCTRACE_PROFILE env var)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON")
	return cmd
}

func runEvents(client *http.Client, endpoint, token, sessionID string, limit int, asJSON bool, out io.Writer) error {
	if sessionID == "" {
		return fmt.Errorf("--session is required; find one with 'cctrace ls'")
	}
	if limit < 1 {
		return fmt.Errorf("--limit must be at least 1")
	}
	if out == nil {
		out = os.Stdout
	}
	q := url.Values{}
	q.Set("session_id", sessionID)
	q.Set("limit", strconv.Itoa(limit))

	raw, err := fetchJSON(client, endpoint+"/api/open/v1/events?"+q.Encode(), token)
	if err != nil {
		return err
	}
	if asJSON {
		_, err := out.Write(raw)
		return err
	}
	// The endpoint returns a bare array with no total.
	var events []struct {
		Ts           string   `json:"ts"`
		EventName    string   `json:"event_name"`
		Model        string   `json:"model"`
		CostUSD      *float64 `json:"cost_usd"`
		InputTokens  *int64   `json:"input_tokens"`
		OutputTokens *int64   `json:"output_tokens"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		return fmt.Errorf("decode events: %w", err)
	}
	num := func(p *int64) string {
		if p == nil {
			return "-"
		}
		return strconv.FormatInt(*p, 10)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tEVENT\tMODEL\tCOST ($)\tINPUT\tOUTPUT")
	for _, e := range events {
		cost := "-"
		if e.CostUSD != nil {
			cost = fmt.Sprintf("%.6f", *e.CostUSD)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Ts, e.EventName, e.Model, cost, num(e.InputTokens), num(e.OutputTokens))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(events) >= limit {
		fmt.Fprintf(out, "\n  showing the first %d -- raise --limit to see more\n", len(events))
	}
	return nil
}
