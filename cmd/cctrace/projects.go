package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// projects lists what the project_hash filter accepts.
//
// Other commands take --project, and until this existed there was no way to learn a
// valid value short of reading the database.
func projectsCmd() *cobra.Command {
	var asJSON bool
	var profileName string

	cmd := &cobra.Command{
		Use:          "projects",
		Short:        "List projects you can filter by (reads the Open API)",
		Long:         "List projects from /api/open/v1/projects.\n\nUses server.read_token; create one with 'cctrace auth read' (administrators: Settings > API Access Tokens).",
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
			return runProjects(endpoint, openAPIToken(p), asJSON, nil)
		},
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile to use (overrides CCTRACE_PROFILE env var)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON")
	return cmd
}

func runProjects(endpoint, token string, asJSON bool, out io.Writer) error {
	if out == nil {
		out = os.Stdout
	}
	raw, err := fetchJSON(endpoint+"/api/open/v1/projects", token)
	if err != nil {
		return err
	}
	if asJSON {
		_, err := out.Write(raw)
		return err
	}
	var body struct {
		Items []struct {
			ProjectHash    string `json:"project_hash"`
			ProjectName    string `json:"project_name"`
			RepositoryName string `json:"repository_name"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("decode projects: %w", err)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tREPOSITORY\tHASH")
	for _, p := range body.Items {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.ProjectName, p.RepositoryName, p.ProjectHash)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	// The endpoint returns everything today. Read total anyway: the spec declares it
	// required, and a list silently cut short is the failure this notice exists for.
	if body.Total > len(body.Items) {
		fmt.Fprintf(out, "\n  showing %d of %d\n", len(body.Items), body.Total)
	}
	return nil
}
