package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// openAPIResourceCmd exposes the resource summaries that usage-insights agents
// need without giving them a general-purpose URL fetcher.
func openAPIResourceCmd(resource string, hasWindow bool) *cobra.Command {
	var window, agent, status, query, profileName string
	var limit int
	var asJSON bool

	cmd := &cobra.Command{
		Use:          resource,
		Short:        "List " + resource + " usage (reads the Open API)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			var duration time.Duration
			if hasWindow {
				duration, err = parseWindow(window)
				if err != nil {
					return err
				}
			}
			return runOpenAPIResource(endpoint, openAPIToken(p), resource, duration, agent, status, query, limit, asJSON, nil)
		},
	}

	if hasWindow {
		cmd.Flags().StringVar(&window, "since", "7d", "Time window (7d, 24h, 90m)")
	}
	if resourceSupportsAgentFilter(resource) {
		cmd.Flags().StringVar(&agent, "agent", "", "Filter by agent")
	}
	if resource == "rules" {
		cmd.Flags().StringVar(&status, "status", "", "Filter by status")
		cmd.Flags().StringVar(&query, "query", "", "Search rule metadata")
		cmd.Flags().IntVar(&limit, "limit", 100, "Maximum rules to return")
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile to use (overrides CCTRACE_PROFILE env var)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON")
	return cmd
}

func runOpenAPIResource(endpoint, token, resource string, window time.Duration, agent, status, query string, limit int, asJSON bool, out io.Writer) error {
	if out == nil {
		out = os.Stdout
	}
	q := url.Values{}
	if window > 0 {
		until := time.Now().UTC()
		q.Set("since", until.Add(-window).Format(time.RFC3339))
		q.Set("until", until.Format(time.RFC3339))
	}
	if resourceSupportsAgentFilter(resource) && agent != "" {
		q.Set("agent", agent)
	}
	if resource == "rules" {
		if status != "" {
			q.Set("status", status)
		}
		if query != "" {
			q.Set("query", query)
		}
		if limit > 0 {
			q.Set("limit", fmt.Sprint(limit))
		}
	}
	raw, err := fetchJSON(endpoint+"/api/open/v1/"+resource+"?"+q.Encode(), token)
	if err != nil {
		return err
	}
	if asJSON {
		_, err = out.Write(raw)
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return fmt.Errorf("decode %s: %w", resource, err)
	}
	_, err = fmt.Fprintln(out, pretty.String())
	return err
}

func resourceSupportsAgentFilter(resource string) bool {
	return resource == "plugins" || resource == "skills" || resource == "rules"
}
