package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

// downgradeRow is a route an older develop build cannot represent: it knows a
// job that owns or is blocked, and nothing between.
type downgradeRow struct{ Source, Reason string }

// runManagedStatus prints what the running daemon last published: the table
// for a person, or the snapshot itself for a script.
func runManagedStatus(w io.Writer, asJSON bool, now time.Time) error {
	root := profile.DefaultDir()
	snapshot, err := localdaemon.ReadSnapshot(root)
	if err != nil {
		return err
	}
	if asJSON {
		b, err := json.MarshalIndent(snapshot, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(b))
		return nil
	}
	var rows []downgradeRow
	// A routes file that cannot be read says nothing about what it holds, so it
	// adds no rows; the profile list is the same, and then no holder is called
	// removed.
	if file, err := localdaemon.ReadRoutes(root); err == nil {
		var known map[string]bool
		if entries, err := (profile.Registry{Root: root}).List(); err == nil {
			known = map[string]bool{}
			for _, entry := range entries {
				known[entry.ID] = true
			}
		}
		rows = downgradeRows(file, known, snapshot)
	}
	renderSyncStatus(w, snapshot, rows, now)
	return nil
}

func renderSyncStatus(w io.Writer, snapshot localdaemon.Snapshot, downgrade []downgradeRow, now time.Time) {
	observed := "-"
	if !snapshot.ObservedAt.IsZero() {
		observed = max(now.Sub(snapshot.ObservedAt), 0).Round(time.Second).String() + " ago"
	}
	fmt.Fprintf(w, "daemon   pid %d   version %s   started %s   observed %s   complete %s\n",
		snapshot.PID, dash(snapshot.Version), clock(snapshot.StartedAt), observed, yesNo(snapshot.Complete))

	sentWidth := len("SENT")
	for _, job := range snapshot.Jobs {
		sentWidth = max(sentWidth, len(fmt.Sprint(job.Sent)))
	}
	fmt.Fprintln(w)
	table := newStatusTable(w)
	fmt.Fprintf(table, "PROFILE\tSTATE\t%*s\tLAST PASS\tNEXT RETRY\tCODE\n", sentWidth, "SENT")
	for _, job := range snapshot.Jobs {
		fmt.Fprintf(table, "%s\t%s\t%*d\t%s\t%s\t%s\n", dash(jobName(job)), dash(job.State), sentWidth, job.Sent, clock(job.LastPassAt), clock(job.NextRetryAt), dash(job.ErrorCode))
	}
	_ = table.Flush()

	var shared []string
	for _, job := range snapshot.Jobs {
		for _, s := range job.SharedSources {
			shared = append(shared, strings.Join([]string{dash(jobName(job)), dash(s.Kind), dash(s.Source), dash(s.Label), dash(s.Reason), dash(holderOf(s)), dash(s.HolderState)}, "\t"))
		}
	}
	if len(shared) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "SHARED SOURCES (collected by another profile, not by the one on the left)")
		table = newStatusTable(w)
		fmt.Fprintln(table, "PROFILE\tKIND\tSOURCE\tLABEL\tREASON\tHELD BY\tHOLDER STATE")
		for _, row := range shared {
			fmt.Fprintln(table, row)
		}
		_ = table.Flush()
	}

	if len(downgrade) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "DOWNGRADE (routes older develop builds cannot represent; v0.7.65 is not affected)")
		table = newStatusTable(w)
		fmt.Fprintln(table, "SOURCE\tREASON")
		for _, row := range downgrade {
			fmt.Fprintf(table, "%s\t%s\n", row.Source, row.Reason)
		}
		_ = table.Flush()
	}
}

func newStatusTable(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0) }

func jobName(job localdaemon.JobStatus) string {
	if job.Name != "" {
		return job.Name
	}
	return job.ID
}

// holderOf names the profile that holds a source: by its name, else by what is
// left of its identity when the profile is gone. Nobody's source has none.
func holderOf(s localdaemon.SharedSource) string {
	if s.HolderName != "" {
		return s.HolderName
	}
	return strings.TrimPrefix(s.Holder, "named:")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func clock(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// downgradeRows lists the routes that an older develop build would decide again
// from nothing but "owner" and "blocked", and so might hand to another profile
// with other filters. known is the set of registry profile IDs, or nil when the
// registry could not be read, in which case no holder is called removed.
func downgradeRows(file localdaemon.RouteFile, known map[string]bool, snapshot localdaemon.Snapshot) []downgradeRow {
	owners := map[string]bool{}
	for _, r := range file.Routes {
		if r.Owner != "" {
			owners[r.Owner] = true
		}
	}
	// A profile that owns a source and is held back from another is split: the old
	// model has an owner or a blocked job, not both.
	split := map[string]bool{}
	for _, job := range snapshot.Jobs {
		if !owners[job.ID] {
			continue
		}
		for _, s := range job.SharedSources {
			if s.Source != "" {
				split[s.Source] = true
			}
		}
	}
	const resolve = "resolve with: cctrace profile claim <profile> --source "
	var rows []downgradeRow
	for _, r := range file.Routes {
		var reason string
		switch {
		case known != nil && r.Owner != "" && !known[r.Owner]:
			reason = "its holder profile no longer exists; " + resolve + r.ID
		case r.State == localdaemon.RouteUnowned:
			reason = "no profile holds it; " + resolve + r.ID
		case r.State == localdaemon.RouteReleasing || r.From != "" || r.Handoff != "":
			reason = "a hand-over is in progress; check again in a minute"
		case len(r.Aliases) > 0:
			reason = "it keeps the destination a policy move left behind; it clears when the other profiles move too, or " + resolve + r.ID
		case split[r.ID]:
			reason = "holder split across profiles; " + resolve + r.ID
		default:
			continue
		}
		rows = append(rows, downgradeRow{Source: r.ID, Reason: reason})
	}
	return rows
}
