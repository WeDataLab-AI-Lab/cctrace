package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"cctrace/internal/profile"

	"github.com/spf13/cobra"
)

type sessionRecord struct {
	Ts                time.Time       `json:"ts"`
	SessionID         string          `json:"session_id,omitempty"`
	ProjectHash       string          `json:"project_hash,omitempty"`
	RecordType        string          `json:"record_type"`
	ProfileEmail      string          `json:"profile_email,omitempty"`
	Model             string          `json:"model,omitempty"`
	InputTokens       *int            `json:"input_tokens,omitempty"`
	OutputTokens      *int            `json:"output_tokens,omitempty"`
	CacheReadTokens   *int            `json:"cache_read_tokens,omitempty"`
	CacheCreateTokens *int            `json:"cache_create_tokens,omitempty"`
	Raw               json.RawMessage `json:"raw"`
}

func sessionsCmd() *cobra.Command {
	var profileEmail string
	var sessionID string
	var limit int
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List session records from the trace server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessions(profileEmail, sessionID, limit, jsonOutput)
		},
	}

	cmd.Flags().StringVar(&profileEmail, "user", "", "Filter by profile email")
	cmd.Flags().StringVar(&sessionID, "session", "", "Filter by session ID")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of records to return")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output raw JSON")

	return cmd
}

func runSessions(profileEmail, sessionID string, limit int, jsonOutput bool) error {
	if !profile.Exists() {
		fmt.Fprintln(os.Stderr, "  No profile found. Run 'cctrace init' first.")
		os.Exit(1)
	}

	p, err := profile.Load()
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}

	apiEndpoint := profileHTTPAPIEndpoint(p)
	if apiEndpoint == "" {
		return fmt.Errorf("no sync endpoint configured; run 'cctrace init' to set one")
	}

	// Build request URL with query params
	u, err := url.Parse(apiEndpoint + "/api/sessions")
	if err != nil {
		return fmt.Errorf("parse endpoint: %w", err)
	}

	q := u.Query()
	if profileEmail != "" {
		q.Set("profile_email", profileEmail)
	}
	if sessionID != "" {
		q.Set("session_id", sessionID)
	}
	q.Set("limit", strconv.Itoa(limit))
	u.RawQuery = q.Encode()

	// The dashboard routes refuse the upload token, like the Open API (#702).
	body, err := fetchJSON(u.String(), openAPIToken(p))
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(string(body))
		return nil
	}

	var records []sessionRecord
	if err := json.Unmarshal(body, &records); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	fmt.Printf("=== Session Records (last %d) ===\n", limit)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tSESSION\tTYPE\tMODEL\tIN\tOUT")

	for _, r := range records {
		ts := r.Ts.Format("2006-01-02 15:04:05")

		sid := r.SessionID
		if len(sid) > 12 {
			sid = sid[:12] + "..."
		}

		inTok := "-"
		if r.InputTokens != nil {
			inTok = strconv.Itoa(*r.InputTokens)
		}
		outTok := "-"
		if r.OutputTokens != nil {
			outTok = strconv.Itoa(*r.OutputTokens)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			ts, sid, r.RecordType, r.Model, inTok, outTok)
	}

	return w.Flush()
}
