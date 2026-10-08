package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"cctrace/internal/openinsights"

	"github.com/spf13/cobra"
)

// insightsCmd answers decision-oriented questions from the Open API. The
// aggregation happens here, not in the agent reading the output, so a skill
// gets a small verified summary instead of thousands of raw rows.
func insightsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "insights",
		Short: "Summarize cost changes and context waste (reads the Open API)",
	}
	cmd.AddCommand(insightsSubcmd("cost", "Explain why cost changed against the previous window", false))
	cmd.AddCommand(insightsSubcmd("context", "Measure cache hit rate, cache rebuilds, and bloated contexts", true))
	return cmd
}

func insightsSubcmd(kind, short string, hasProject bool) *cobra.Command {
	var window, project, profileName string
	var limit int
	var asJSON bool

	cmd := &cobra.Command{
		Use:          kind,
		Short:        short,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 0 {
				return fmt.Errorf("--limit must be zero or more")
			}
			span, err := parseWindow(window)
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
			client := openinsights.NewClient(endpoint, openAPIToken(p))
			tr, err := serverTransport(p.Server.CACertFile)
			if err != nil {
				return err
			}
			if tr != nil {
				client.SetTransport(tr)
			}
			now := time.Now().UTC().Truncate(time.Second)
			if kind == "cost" {
				return runInsightsCost(cmd.Context(), client, now, span, limit, asJSON, os.Stdout)
			}
			return runInsightsContext(cmd.Context(), client, now, span, project, limit, asJSON, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&window, "since", "7d", "Window length (7d, 24h, 90m)")
	cmd.Flags().IntVar(&limit, "limit", 5, "Maximum sessions to list")
	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile to use (overrides CCTRACE_PROFILE env var)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output compact JSON")
	if hasProject {
		cmd.Flags().StringVar(&project, "project", "", "Project hash from 'cctrace projects'")
	}
	return cmd
}

// runInsightsCost compares [now-span, now) against the span before it.
func runInsightsCost(ctx context.Context, c *openinsights.Client, now time.Time, span time.Duration, limit int, asJSON bool, out io.Writer) error {
	cur := openinsights.Window{Since: now.Add(-span), Until: now}
	prev := openinsights.Window{Since: now.Add(-2 * span), Until: cur.Since}

	scope, err := c.Scope(ctx)
	if err != nil {
		return explainAPIError(err)
	}
	prevRows, prevCut, err := c.Sessions(ctx, prev, "")
	if err != nil {
		return explainAPIError(err)
	}
	curRows, curCut, err := c.Sessions(ctx, cur, "")
	if err != nil {
		return explainAPIError(err)
	}
	prevModels, err := c.ModelCosts(ctx, prev)
	if err != nil {
		return explainAPIError(err)
	}
	curModels, err := c.ModelCosts(ctx, cur)
	if err != nil {
		return explainAPIError(err)
	}
	res := openinsights.AggregateCost(prev, cur, prevRows, curRows, prevModels, curModels, limit, scope)
	if prevCut || curCut {
		res.Truncated = true
		res.Caveats = append(res.Caveats, openinsights.Caveat{Code: "truncated", Detail: "a window exceeded the row cap; its oldest sessions are missing"})
	}
	return writeInsights(out, res, asJSON)
}

// runInsightsContext summarizes context use over [now-span, now).
func runInsightsContext(ctx context.Context, c *openinsights.Client, now time.Time, span time.Duration, projectHash string, limit int, asJSON bool, out io.Writer) error {
	w := openinsights.Window{Since: now.Add(-span), Until: now}
	scope, err := c.Scope(ctx)
	if err != nil {
		return explainAPIError(err)
	}
	var projectName string
	if projectHash != "" {
		projects, err := c.Projects(ctx)
		if err != nil {
			return explainAPIError(err)
		}
		for _, p := range projects {
			if p.ProjectHash == projectHash {
				// Never fall back to the hash: it is a filesystem path.
				projectName = cmp.Or(p.ProjectName, "(unnamed)")
			}
		}
		if projectName == "" {
			return fmt.Errorf("unknown project; list valid values with 'cctrace projects'")
		}
	}

	events, truncated, err := c.Events(ctx, w, projectHash)
	if err != nil {
		return explainAPIError(err)
	}
	res := openinsights.AggregateContext(w, events, limit, scope)
	res.Project = projectName
	if truncated {
		markContextTruncated(&res, events)
	}
	return writeInsights(out, res, asJSON)
}

func markContextTruncated(res *openinsights.ContextResult, events []openinsights.Event) {
	res.Truncated = true
	for _, e := range events {
		if res.CoveredSince == nil || e.Ts.Before(*res.CoveredSince) {
			ts := e.Ts
			res.CoveredSince = &ts
		}
	}
	res.Caveats = append(res.Caveats, openinsights.Caveat{Code: "truncated", Detail: "the window exceeded the row cap; only data after covered_since is included"})
}

func explainAPIError(err error) error {
	var apiErr *openinsights.APIError
	if errors.As(err, &apiErr) && apiErr.Code == "ingestion_token" {
		return uploadTokenCannotRead(apiErr.Message)
	}
	return err
}

func writeInsights(out io.Writer, v any, compact bool) error {
	var raw []byte
	var err error
	if compact {
		raw, err = json.Marshal(v)
	} else {
		raw, err = json.MarshalIndent(v, "", "  ")
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(raw))
	return err
}
