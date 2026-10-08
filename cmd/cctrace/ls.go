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
	"time"

	"github.com/spf13/cobra"
)

// ls lists sessions through /api/open/v1/sessions -- the contract external scripts
// read.
//
// It is a separate command from `cctrace sessions` rather than a replacement. That
// one returns raw session records including the transcript; the open API carries no
// transcript by design. Moving it would have quietly removed a capability from
// anyone piping its --json.
func lsCmd() *cobra.Command {
	var window, project, profileName string
	var limit int
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List recent sessions with cost (reads the Open API)",
		Long: "List sessions from /api/open/v1/sessions.\n\n" +
			"Uses server.read_token; create one with 'cctrace auth read' (administrators: Settings > API Access Tokens).\n" +
			"For raw session records including transcripts, use 'cctrace sessions'.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseWindow(window)
			if err != nil {
				return err
			}
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
			return runLs(client, endpoint, openAPIToken(p), d, project, limit, asJSON, nil)
		},
	}
	cmd.Flags().StringVar(&window, "since", "7d", "Time window (7d, 24h, 90m)")
	cmd.Flags().StringVar(&project, "project", "", "Project hash from 'cctrace projects'")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum sessions to list")
	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile to use (overrides CCTRACE_PROFILE env var)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON")
	return cmd
}

type lsSession struct {
	SessionID    string  `json:"session_id"`
	Agent        string  `json:"agent"`
	Model        string  `json:"model"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	EventCount   int64   `json:"event_count"`
}

func runLs(client *http.Client, endpoint, token string, window time.Duration, project string, limit int, asJSON bool, out io.Writer) error {
	if limit < 1 {
		return fmt.Errorf("--limit must be at least 1")
	}
	if out == nil {
		out = os.Stdout
	}
	until := time.Now()
	q := url.Values{}
	q.Set("since", until.Add(-window).UTC().Format(time.RFC3339))
	q.Set("until", until.UTC().Format(time.RFC3339))
	q.Set("limit", strconv.Itoa(limit))
	if project != "" {
		q.Set("project_hash", project)
	}

	raw, err := fetchJSON(client, endpoint+"/api/open/v1/sessions?"+q.Encode(), token)
	if err != nil {
		return err
	}
	if asJSON {
		_, err := out.Write(raw)
		return err
	}
	// The endpoint returns a bare array with no total, so a full page is the only
	// sign that more sessions exist.
	var sessions []lsSession
	if err := json.Unmarshal(raw, &sessions); err != nil {
		return fmt.Errorf("decode sessions: %w", err)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tAGENT\tMODEL\tCOST ($)\tINPUT\tOUTPUT\tEVENTS")
	for _, s := range sessions {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.4f\t%d\t%d\t%d\n",
			s.SessionID, s.Agent, s.Model, s.CostUSD, s.InputTokens, s.OutputTokens, s.EventCount)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(sessions) >= limit {
		fmt.Fprintf(out, "\n  showing the first %d -- raise --limit to see more\n", len(sessions))
	}
	return nil
}
