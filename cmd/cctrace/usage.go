package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// usage reads /api/open/v1/usage -- the same endpoint external scripts use.
//
// The CLI goes through the public contract on purpose. It makes this command the
// API's first consumer, so a break shows up here before it reaches anyone outside,
// and it keeps one contract instead of two answering the same question.
func usageCmd() *cobra.Command {
	var window, untilAgo, groupBy, profileName string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show token and cost usage (reads the Open API)",
		Long: "Read usage from /api/open/v1/usage.\n\n" +
			"Uses server.read_token; create one with 'cctrace auth read' (administrators: Settings > API Access Tokens).\n" +
			"The token 'cctrace init' writes is for uploading sessions and is not accepted here.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseWindow(window)
			if err != nil {
				return err
			}
			var untilWindow time.Duration
			if untilAgo != "" {
				untilWindow, err = parseWindow(untilAgo)
				if err != nil {
					return err
				}
				if untilWindow >= d {
					return fmt.Errorf("--until must be shorter than --since")
				}
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
			return runUsageWindow(client, endpoint, openAPIToken(p), d, untilWindow, groupBy, asJSON, nil)
		},
	}
	cmd.Flags().StringVar(&window, "since", "7d", "Time window (7d, 24h, 90m)")
	cmd.Flags().StringVar(&untilAgo, "until", "", "End the window this long before now (for example, --since 14d --until 7d)")
	cmd.Flags().StringVar(&groupBy, "group-by", "", "Group by user, team, or model")
	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile to use (overrides CCTRACE_PROFILE env var)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON")
	return cmd
}

func runUsage(client *http.Client, endpoint, token string, window time.Duration, groupBy string, asJSON bool, out io.Writer) error {
	return runUsageWindow(client, endpoint, token, window, 0, groupBy, asJSON, out)
}

func runUsageWindow(client *http.Client, endpoint, token string, window, untilAgo time.Duration, groupBy string, asJSON bool, out io.Writer) error {
	if out == nil {
		out = os.Stdout
	}
	until := time.Now().Add(-untilAgo)
	q := url.Values{}
	// The API takes absolute times. "7d" is a convenience of this command and must
	// not reach the contract.
	q.Set("since", until.Add(-window).UTC().Format(time.RFC3339))
	q.Set("until", until.UTC().Format(time.RFC3339))
	if groupBy != "" {
		q.Set("group_by", groupBy)
	}

	raw, err := fetchJSON(client, endpoint+"/api/open/v1/usage?"+q.Encode(), token)
	if err != nil {
		return err
	}
	if asJSON {
		_, err := out.Write(raw)
		return err
	}
	if groupBy != "" {
		return printUsageGroups(out, raw)
	}
	return printUsageTotals(out, raw)
}

type usageGroupRow struct {
	Key          string  `json:"key"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	RequestCount int64   `json:"request_count"`
}

func printUsageGroups(out io.Writer, raw []byte) error {
	var body struct {
		Items []usageGroupRow `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("decode usage: %w", err)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tCOST ($)\tINPUT\tOUTPUT\tREQUESTS")
	for _, r := range body.Items {
		fmt.Fprintf(tw, "%s\t%.4f\t%d\t%d\t%d\n", r.Key, r.CostUSD, r.InputTokens, r.OutputTokens, r.RequestCount)
	}
	return tw.Flush()
}

func printUsageTotals(out io.Writer, raw []byte) error {
	var body struct {
		SessionCount int64   `json:"session_count"`
		CostUSD      float64 `json:"cost_usd"`
		InputTokens  int64   `json:"input_tokens"`
		OutputTokens int64   `json:"output_tokens"`
		ByModel      []struct {
			Model        string `json:"model"`
			InputTokens  int64  `json:"input_tokens"`
			OutputTokens int64  `json:"output_tokens"`
		} `json:"by_model"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("decode usage: %w", err)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "SESSIONS\t%d\n", body.SessionCount)
	fmt.Fprintf(tw, "COST ($)\t%.4f\n", body.CostUSD)
	fmt.Fprintf(tw, "INPUT\t%d\n", body.InputTokens)
	fmt.Fprintf(tw, "OUTPUT\t%d\n", body.OutputTokens)
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(body.ByModel) == 0 {
		return nil
	}
	fmt.Fprintln(out)
	mw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(mw, "MODEL\tINPUT\tOUTPUT")
	for _, m := range body.ByModel {
		fmt.Fprintf(mw, "%s\t%d\t%d\n", m.Model, m.InputTokens, m.OutputTokens)
	}
	return mw.Flush()
}
