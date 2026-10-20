package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"cctrace/internal/codexlog"
	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/samefolder"
	"cctrace/internal/syncer"
)

// claimWait bounds the request to the daemon, which answers once it has decided,
// and the wait for what it publishes after rebuilding the routes file.
const claimWait = 10 * time.Second

// claimPoll is how often the command looks for that publication.
const claimPoll = 20 * time.Millisecond

// claimStateFiles are the files each kind of source keeps its cursor in, in a
// profile's directory.
var claimStateFiles = map[string]string{
	"claude": "sync-state.json", "codex": "codex-sync-state.json", "gjc": "gjc-sync-state.json", "omo": "omo-sync-state.json",
}

type claimArgs struct {
	profile string
	sources []string
	all     bool
	force   bool
}

// profileClaimEnv is what the command touches, so a test can stand in for it.
type profileClaimEnv struct {
	root     string
	registry profile.Registry
	in       io.Reader
	out      io.Writer
	tty      bool
	claim    func(ctx context.Context, root string, req localdaemon.ClaimRequest) error
}

// claimPlan is one source to hand to the profile, with what the person is shown
// about it.
type claimPlan struct {
	req    localdaemon.ClaimRequest
	line   string // "kind:folder"
	holder *profile.Entry
	// owner is the route's owner as the routes file writes it; "" if nobody's.
	owner string
	// from is the profile that read the source last, for one nobody holds.
	from   string
	known  bool // the route was read, so the holder is known
	state  string
	routed bool // a live profile reads the source now: it lets go first
}

func profileClaimCmd() *cobra.Command {
	var args claimArgs
	cmd := &cobra.Command{
		Use:   "claim <profile>",
		Short: "Give a shared collection source to a profile",
		Long: `Give a source that several profiles share to <profile>. The local daemon lets a
source be read by one profile at a time; the others are held back from it. Claim
moves it: the profile that holds it lets go first, and <profile> goes on from its
cursor.

Example:
  cctrace profile claim work --source 3fa91c2d
  cctrace profile claim work --all --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, a []string) error {
			args.profile = a[0]
			return runProfileClaim(profileClaimEnv{
				root: profile.DefaultDir(), registry: profile.Registry{Root: profile.DefaultDir()},
				in: cmd.InOrStdin(), out: cmd.OutOrStdout(), tty: term.IsTerminal(int(os.Stdin.Fd())), claim: localdaemon.Claim,
			}, args)
		},
	}
	cmd.Flags().StringArrayVar(&args.sources, "source", nil, "Source to claim: its id, or its folder (repeatable)")
	cmd.Flags().BoolVar(&args.all, "all", false, "Claim every source the profile is held back from")
	cmd.Flags().BoolVar(&args.force, "force", false, "Skip confirmation")
	return cmd
}

func runProfileClaim(env profileClaimEnv, args claimArgs) error {
	if running, err := localdaemon.Running(env.root); err != nil || !running {
		return errors.New("the local daemon is not running -- start it with `cctrace sync --daemon`, then run this again")
	}
	target, err := env.registry.Load(args.profile)
	if err != nil {
		return err
	}
	entries, err := env.registry.List()
	if err != nil {
		return fmt.Errorf("read the profiles: %w", err)
	}
	held, err := heldBack(env.root, target)
	if err != nil {
		return err
	}
	if len(held) == 0 {
		return fmt.Errorf("nothing to claim: profile %q is not held back from any source", target.Name)
	}
	chosen, err := chooseHeld(held, args)
	if err != nil {
		return err
	}
	if !args.force && !env.tty {
		return errors.New("claim asks before it moves a source and there is no terminal to ask on; pass --force to confirm in advance")
	}
	routes, routesErr := localdaemon.ReadRoutes(env.root)
	plans := make([]claimPlan, 0, len(chosen))
	for _, h := range chosen {
		plans = append(plans, planClaim(target, entries, routes, routesErr == nil, h))
	}
	for _, p := range plans {
		printClaimPlan(env.out, target, p)
	}
	if !args.force {
		fmt.Fprint(env.out, "Proceed? [y/N] ")
		answer, _ := bufio.NewReader(env.in).ReadString('\n')
		if strings.TrimSpace(answer) != "y" && strings.TrimSpace(answer) != "Y" {
			fmt.Fprintln(env.out, "Aborted.")
			return nil
		}
	}
	var failed []error
	rebuilt := false
	for _, p := range plans {
		if !p.known && rebuilt {
			// The claim before this one had the daemon rebuild the routes file, in
			// which the line has an id now; it is claimed by that.
			next, held, err := rebuiltClaim(env, target, entries, p.line)
			if err != nil || !held {
				if err == nil {
					err = errors.New("the profile is no longer held back from this source")
				}
				failed = append(failed, fmt.Errorf("%s: %w", claimName(p), err))
				continue
			}
			p = next
		}
		ctx, cancel := context.WithTimeout(context.Background(), claimWait)
		err := env.claim(ctx, env.root, p.req)
		cancel()
		if err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", claimName(p), claimRefusal(err)))
			continue
		}
		rebuilt = rebuilt || !p.known
		if p.routed {
			fmt.Fprintf(env.out, "Claim %s accepted: %q lets go of it first, then %q starts reading it.\n", claimName(p), holderName(p), target.Name)
		} else {
			fmt.Fprintf(env.out, "Claim %s accepted: %q starts reading it.\n", claimName(p), target.Name)
		}
	}
	return errors.Join(failed...)
}

// heldBack are the lines the daemon left the profile out of, as it last
// published them. A record from another run of the daemon, or of the profile
// before it was edited, is not one to act on.
func heldBack(root string, target profile.Entry) ([]localdaemon.ExcludedLine, error) {
	stale := errors.New("the daemon's record of what this profile is held back from is not current -- try again in a moment")
	rt, err := localdaemon.ReadRuntime(root)
	if err != nil {
		return nil, stale
	}
	alloc, err := localdaemon.ReadAllocations(root)
	if err != nil || alloc.InstanceID != rt.InstanceID {
		return nil, stale
	}
	job, ok := alloc.Jobs[target.ID]
	if ok && job.Revision != target.Revision {
		return nil, stale
	}
	return job.Excluded, nil
}

// rebuiltClaim plans the claim of a line once the daemon has published what it
// reads after rebuilding the routes file, which gives the line an id. held is
// false when the profile is not held back from the line any more.
func rebuiltClaim(env profileClaimEnv, target profile.Entry, entries []profile.Entry, line string) (claimPlan, bool, error) {
	for deadline := time.Now().Add(claimWait); ; time.Sleep(claimPoll) {
		if held, err := heldBack(env.root, target); err == nil {
			i := slices.IndexFunc(held, func(h localdaemon.ExcludedLine) bool { return h.Line == line })
			if i < 0 {
				return claimPlan{}, false, nil
			}
			if held[i].Route != "" {
				routes, err := localdaemon.ReadRoutes(env.root)
				return planClaim(target, entries, routes, err == nil, held[i]), true, nil
			}
		}
		if time.Now().After(deadline) {
			return claimPlan{}, false, errors.New("the daemon has not published the rebuilt routes yet -- run the command again")
		}
	}
}

// chooseHeld picks the lines the arguments name, and tells the person what they
// could have named when they named none.
func chooseHeld(held []localdaemon.ExcludedLine, args claimArgs) ([]localdaemon.ExcludedLine, error) {
	if args.all {
		return held, nil
	}
	if len(args.sources) == 0 {
		var ids []string
		for _, h := range held {
			ids = append(ids, claimSourceName(h))
		}
		return nil, fmt.Errorf("say what to claim: --source <id|folder> (held back from: %s), or --all", strings.Join(ids, ", "))
	}
	var chosen []localdaemon.ExcludedLine
	for _, want := range args.sources {
		i := slices.IndexFunc(held, func(h localdaemon.ExcludedLine) bool { return matchesHeld(h, want) })
		if i < 0 {
			return nil, fmt.Errorf("profile is not held back from %q -- see `cctrace status`, or use --all", want)
		}
		if !slices.Contains(chosen, held[i]) {
			chosen = append(chosen, held[i])
		}
	}
	return chosen, nil
}

// matchesHeld says whether want names the line: by the route's id, by the line,
// or by the folder, which may be spelled another way than the profile has it.
func matchesHeld(h localdaemon.ExcludedLine, want string) bool {
	if h.Route != "" && want == h.Route || want == h.Line {
		return true
	}
	kind, dir, _ := strings.Cut(h.Line, ":")
	if prefix, rest, ok := strings.Cut(want, ":"); ok && prefix == kind {
		want = rest
	}
	return samefolder.Same(dir, codexlog.ExpandHome(want))
}

func claimSourceName(h localdaemon.ExcludedLine) string {
	kind, _, _ := strings.Cut(h.Line, ":")
	if h.Route == "" {
		return kind
	}
	return h.Route + " (" + kind + ")"
}

// planClaim reads who holds the line and at which revisions. When the routes
// file cannot be used the daemon has no ids and the line is the name; then the
// holder is not known, and the request says so by asking for none.
func planClaim(target profile.Entry, entries []profile.Entry, routes localdaemon.RouteFile, routesRead bool, h localdaemon.ExcludedLine) claimPlan {
	p := claimPlan{line: h.Line, req: localdaemon.ClaimRequest{Profile: target.ID, Source: h.Route, TargetRev: target.Revision}}
	if p.req.Source == "" {
		p.req.Source = h.Line
	}
	i := slices.IndexFunc(routes.Routes, func(r localdaemon.Route) bool { return h.Route != "" && r.ID == h.Route })
	if !routesRead || i < 0 {
		return p
	}
	r := routes.Routes[i]
	p.known, p.owner, p.from = true, r.Owner, r.From
	p.req.Holder, p.req.RouteRev = r.Owner, r.Rev
	if j := slices.IndexFunc(entries, func(e profile.Entry) bool { return r.Owner != "" && e.ID == r.Owner }); j >= 0 {
		p.holder = &entries[j]
		p.req.HolderRev = entries[j].Revision
		p.routed = r.State == localdaemon.RouteActive
	}
	p.state = holderStateOf(target.ID, h.Route)
	return p
}

// holderStateOf is what the daemon last published about the holder of a source
// the profile is held back from; "" when nothing current was published.
func holderStateOf(profileID, route string) string {
	snapshot, ok := readDaemonSnapshotFn()
	if !ok {
		return ""
	}
	for _, job := range snapshot.Jobs {
		if job.ID != profileID {
			continue
		}
		for _, s := range job.SharedSources {
			if s.Source == route {
				return s.HolderState
			}
		}
	}
	return ""
}

func claimName(p claimPlan) string {
	if p.known {
		return p.req.Source
	}
	kind, _, _ := strings.Cut(p.line, ":")
	return kind + " source"
}

func holderName(p claimPlan) string {
	if p.holder != nil {
		return p.holder.Name
	}
	return strings.TrimPrefix(p.owner, "named:")
}

func printClaimPlan(out io.Writer, target profile.Entry, p claimPlan) {
	kind, _, _ := strings.Cut(p.line, ":")
	label := profileJobSpec(target, daemonOverrides{}).SourceLabels[p.line]
	id := ""
	if p.known {
		id = " " + p.req.Source
	}
	fmt.Fprintf(out, "Claim %s source%s (%s) for profile %q\n", kind, id, claimLabel(label), target.Name)
	fmt.Fprintf(out, "  current holder : %s\n", claimHolder(p))
	fmt.Fprintf(out, "  cursor         : %s\n", claimCursor(kind, p))
	for i, line := range claimFilters(target, p) {
		lead := "  filters        : "
		if i > 0 {
			lead = "                   "
		}
		fmt.Fprintln(out, lead+line)
	}
	switch {
	case p.routed:
		fmt.Fprintf(out, "  effect         : %q stops reading this source; %q starts after %q has released it\n", holderName(p), target.Name, holderName(p))
	default:
		fmt.Fprintf(out, "  effect         : %q starts reading this source at once\n", target.Name)
	}
}

// claimLabel says in words where the profile got the folder from. It is the
// label the planner was given, never the folder.
func claimLabel(label string) string {
	switch label {
	case "default":
		return "default folder"
	case "explicit":
		return "explicit Codex home"
	case "home":
		return "Claude home"
	case "env":
		return "CODEX_HOME folder"
	}
	return "configured folder"
}

func claimHolder(p claimPlan) string {
	switch {
	case !p.known:
		return "(unknown; the routes file could not be read)"
	case p.owner == "":
		return "(nobody)"
	case p.holder == nil || p.state == localdaemon.HolderRemoved:
		return holderName(p) + " (removed profile)"
	case p.state == "" || p.state == localdaemon.HolderActive:
		return holderName(p) + " (" + localdaemon.HolderActive + ")"
	case p.state == localdaemon.HolderTransferring:
		return holderName(p) + " (handing the source over)"
	}
	return holderName(p) + " (" + p.state + ")"
}

// claimCursor says where the profile will go on from: the holder's cursor, or for
// a source nobody holds the cursor of the profile that read it last, which the
// daemon takes over, if that profile's state can be read.
func claimCursor(kind string, p claimPlan) string {
	const unavailable = "holder state unavailable; existing sessions will be skipped, new ones collected"
	id, name := "", ""
	switch {
	case p.holder != nil:
		id, name = p.holder.ID, p.holder.Name
	case p.known && p.owner == "":
		id, name = p.from, strings.TrimPrefix(p.from, "named:")
	}
	if id == "" {
		return unavailable
	}
	if _, origin, _ := previousHolderState(id, claimStateFiles[kind]); origin == syncer.StateValid {
		return fmt.Sprintf("continue from %q", name)
	}
	return unavailable
}

// claimFilters shows how the two profiles' filters differ in size, never in
// value: a repository prefix is a path.
func claimFilters(target profile.Entry, p claimPlan) []string {
	own := profileJobSpec(target, daemonOverrides{}).Filters
	if p.holder == nil {
		return []string{
			"the holder's settings are not known; " + fmt.Sprintf("%q applies its own:", target.Name),
			"repository allowlist: " + countWord(len(own.RepoPrefixes)) + prefixNote(own),
			"prompt redaction: " + onOff(own.RedactPrompts),
			"tool redaction: " + onOff(own.RedactTools),
			fmt.Sprintf("excluded accounts: %d", len(own.ExcludeAccounts)),
		}
	}
	old := profileJobSpec(*p.holder, daemonOverrides{}).Filters
	h, t := p.holder.Name, target.Name
	lines := []string{
		fmt.Sprintf("repository allowlist: %s %s, %q %s%s", h, countWord(len(old.RepoPrefixes)), t, countWord(len(own.RepoPrefixes)), prefixNote(own)),
		fmt.Sprintf("prompt redaction: %s %s, %q %s", h, onOff(old.RedactPrompts), t, onOff(own.RedactPrompts)),
		fmt.Sprintf("tool redaction: %s %s, %q %s", h, onOff(old.RedactTools), t, onOff(own.RedactTools)),
		fmt.Sprintf("excluded accounts: %s %d, %q %d", h, len(old.ExcludeAccounts), t, len(own.ExcludeAccounts)),
	}
	if !localdaemon.FilterNoLooser(old, own) {
		lines = append(lines, fmt.Sprintf("note: %q would send more than %q does", t, h))
	}
	return lines
}

func countWord(n int) string {
	switch n {
	case 0:
		return "none"
	case 1:
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

func prefixNote(f localdaemon.Filters) string {
	if len(f.RepoPrefixes) == 0 {
		return " (collects every repository)"
	}
	return ""
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// claimRefusal puts the daemon's refusal into words a person can act on.
func claimRefusal(err error) error {
	var refused *localdaemon.ControlError
	if !errors.As(err, &refused) {
		return err
	}
	switch refused.Code {
	case "route_changed", "profile_changed":
		return errors.New("the routes or the profiles changed since the plan was made; nothing was done -- run the command again")
	case "handoff_pending":
		return errors.New("a hand-over of this source is still in progress -- try again in a minute")
	case "invalid_source", "route_unknown":
		return errors.New("the profile is no longer held back from this source")
	case "line_busy":
		return errors.New("the source is still being read -- try again in a moment")
	case "daemon_instance_changed":
		return errors.New("the daemon was restarted since the plan was made -- run the command again")
	case "invalid_action":
		return errors.New("the running daemon is too old to take this request -- restart it (`cctrace sync --stop`, then `cctrace sync --daemon`)")
	case "state_write_failed":
		return errors.New("the daemon could not save the routes -- see the sync log")
	}
	return err
}
