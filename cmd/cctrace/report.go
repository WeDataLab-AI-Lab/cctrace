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

	"cctrace/internal/profile"
	"cctrace/internal/store"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

func reportCmd() *cobra.Command {
	var since time.Duration
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Show cost and tool usage report from the trace server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReport(since, asJSON)
		},
	}

	cmd.Flags().DurationVar(&since, "since", 168*time.Hour, "Time window for the report (e.g. 24h, 168h)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON instead of a table")
	return cmd
}

func runReport(since time.Duration, asJSON bool) error {
	if !profile.Exists() {
		fmt.Fprintln(os.Stderr, "  No profile found. Run 'cctrace init' first.")
		os.Exit(1)
	}

	p, err := profile.Load()
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}

	endpoint := profileHTTPAPIEndpoint(p)
	if endpoint == "" {
		return fmt.Errorf("no sync endpoint configured; run 'cctrace init' to set one")
	}

	until := time.Now()
	sinceTime := until.Add(-since)

	q := url.Values{}
	q.Set("since", sinceTime.UTC().Format(time.RFC3339))
	q.Set("until", until.UTC().Format(time.RFC3339))
	queryStr := q.Encode()

	// The dashboard routes refuse the upload token, like the Open API (#702).
	token := openAPIToken(p)

	var byUser []*store.CostSummary
	var byTeam []*store.CostSummary
	var tools []*store.ToolUsageSummary

	var byUserRaw, byTeamRaw, toolsRaw []byte

	g := new(errgroup.Group)

	g.Go(func() error {
		raw, err := fetchJSON(endpoint+"/api/cost/by-user?"+queryStr, token)
		if err != nil {
			return fmt.Errorf("cost/by-user: %w", err)
		}
		byUserRaw = raw
		return json.Unmarshal(raw, &byUser)
	})

	g.Go(func() error {
		raw, err := fetchJSON(endpoint+"/api/cost/by-team?"+queryStr, token)
		if err != nil {
			return fmt.Errorf("cost/by-team: %w", err)
		}
		byTeamRaw = raw
		return json.Unmarshal(raw, &byTeam)
	})

	g.Go(func() error {
		raw, err := fetchJSON(endpoint+"/api/tools?"+queryStr, token)
		if err != nil {
			return fmt.Errorf("tools: %w", err)
		}
		toolsRaw = raw
		return json.Unmarshal(raw, &tools)
	})

	if err := g.Wait(); err != nil {
		return err
	}

	if asJSON {
		fmt.Println("// cost by user")
		fmt.Println(string(byUserRaw))
		fmt.Println("// cost by team")
		fmt.Println(string(byTeamRaw))
		fmt.Println("// tool usage")
		fmt.Println(string(toolsRaw))
		return nil
	}

	days := since.Hours() / 24
	fmt.Printf("\n=== Cost by User (last %.0fd) ===\n", days)
	printCostByUser(os.Stdout, byUser)

	fmt.Printf("\n=== Cost by Team ===\n")
	printCostByTeam(os.Stdout, byTeam)

	fmt.Printf("\n=== Tool Usage ===\n")
	printToolUsage(os.Stdout, tools)

	return nil
}

func fetchJSON(rawURL, token string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		var refusal struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if json.Unmarshal(body, &refusal) == nil && refusal.Code == "ingestion_token" {
			return nil, uploadTokenCannotRead(refusal.Error)
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func printCostByUser(w io.Writer, rows []*store.CostSummary) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "USER EMAIL\tCOST ($)\tREQUESTS")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%.4f\t%d\n", r.ProfileEmail, r.TotalCost, r.RequestCount)
	}
	tw.Flush()
}

func printCostByTeam(w io.Writer, rows []*store.CostSummary) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TEAM\tCOST ($)")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%.4f\n", r.UserTeam, r.TotalCost)
	}
	tw.Flush()
}

func printToolUsage(w io.Writer, rows []*store.ToolUsageSummary) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TOOL\tCALLS\tSUCCESS\tFAIL")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\n", r.ToolName, r.UseCount, r.SuccessCount, r.FailCount)
	}
	tw.Flush()
}
