package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cctrace/internal/codexsyncer"
	"cctrace/internal/envgen"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
	"cctrace/internal/usage"

	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	var profileName string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show current cctrace profile and connection status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(profileName)
		},
	}

	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile to show status for")
	return cmd
}

func runStatus(profileName string) error {
	var p *profile.Profile
	var profileDir string
	var label string

	if profileName != "" {
		var err error
		p, err = profile.LoadNamed(profileName)
		if err != nil {
			return err
		}
		namedDir, _ := profile.NamedDir(profileName)
		profileDir = namedDir
		label = profileName
	} else {
		if !profile.Exists() {
			fmt.Fprintln(os.Stderr, "  No profile found. Run 'cctrace init' first.")
			os.Exit(1)
		}
		var err error
		p, err = profile.Load()
		if err != nil {
			return fmt.Errorf("failed to load profile: %w", err)
		}
		profileDir = profile.DefaultDir()
		label = "default"
	}

	fmt.Println()

	// --- User Identity ---
	fmt.Println("  USER")
	if profileName != "" {
		fmt.Printf("    Profile:       %s\n", label)
	}
	fmt.Printf("    Name:          %s\n", p.User.Name)
	fmt.Printf("    Email:         %s\n", p.User.Email)
	fmt.Printf("    User ID:       %s\n", valueOrDash(p.User.ID))
	fmt.Printf("    Team:          %s\n", valueOrDash(p.User.Team))

	// --- Server ---
	fmt.Println()
	fmt.Println("  SERVER")
	protocol := p.Server.Protocol
	if protocol == "" {
		protocol = "grpc"
	}
	if p.Server.Endpoint != "" {
		fmt.Printf("    OTEL:          %s (%s)\n", p.Server.Endpoint, protocol)
		fmt.Printf("    OTEL status:   %s\n", checkEndpoint(p.Server.Endpoint))
	} else {
		fmt.Println("    OTEL:          (not configured)")
	}
	if p.Server.SyncEndpoint != "" {
		fmt.Printf("    Sync:          %s\n", p.Server.SyncEndpoint)
		// Two subjects, two lines. This used to be one line, `Sync status:`, and it
		// reported whether *this* process can reach the server -- which for two days
		// said healthy while the daemon on the same machine failed every send, and
		// the true answer to one question was read as the answer to the other
		// (#712). Naming who probed is the fix; the reachability answer itself is
		// still worth printing, because "this process reaches it, the daemon does
		// not" is what identified the stuck daemon.
		fmt.Printf("    서버 도달:     %s\n", checkHTTPHealth(p.Server.SyncEndpoint))
		// Reachability is not the same as completeness. A run that skipped content
		// at first sync reports "healthy" on the line above and is missing session
		// history all the same, so the two facts are printed together.
		// Every agent skips on its own first run and each keeps its own state file,
		// so a user of only one of them must not be told everything is fine.
		//
		// This list has to grow with the syncers. It did not when gjc and omo were
		// added, and they are the two that need it most: neither installs a hook, so
		// "turning the integration on" is always a first run, and every new user of
		// them lands on exactly the silence this notice exists to break.
		skipped := make(map[string]*syncer.FileState)
		rulesDenied := make(map[string]time.Time)
		serverExcluded := make(map[string]time.Time)
		var stall *syncer.TransportFailure
		for _, path := range syncStatePaths(profileName) {
			st, err := syncer.LoadState(path)
			if err != nil {
				continue
			}
			for file, fs := range st.Files {
				skipped[file] = fs
			}
			for repo, at := range st.RulesDenied {
				rulesDenied[repo] = at
			}
			for account, at := range st.ServerExcludedAccounts {
				serverExcluded[account] = at
			}
			stall = longerStall(stall, st.TransportFailure)
		}
		fmt.Printf("    수집 상태:     %s\n", collectionStatusValue(stall, daemonHoldsSyncLock(profileName)))
		if notice := firstRunSkipNotice(skipped); notice != "" {
			fmt.Printf("                   %s\n", notice)
		}
		if notice := bodyLimitBlockNotice(skipped); notice != "" {
			fmt.Printf("                   %s\n", notice)
		}
		if notice := rulesDeniedNotice(rulesDenied); notice != "" {
			fmt.Printf("                   %s\n", notice)
		}
		if notice := excludedAccountsNotice(p.Options.ExcludeAccounts, serverExcluded, time.Now()); notice != "" {
			fmt.Printf("                   %s\n", notice)
		}
		// Next to the sync status rather than under PATHS: what this reports is
		// the running daemon, not the file on disk. A binary replaced on disk
		// does not replace the resident watch child, so the server, the disk and
		// the process disagree with nothing saying so (#458).
		if notice := daemonRestartNotice(profileName, p.Options.SyncEnabled); notice != "" {
			fmt.Println(notice)
		}
	} else {
		fmt.Println("    Sync:          (not configured)")
	}

	// Outside the sync-endpoint block on purpose. Self-update resolves its
	// endpoint through profileHTTPAPIEndpoint, which falls back to Server.Endpoint
	// when the sync endpoint is empty -- so a profile in that shape can stall,
	// record it, and show nothing. Nesting the one notice that reports "you are
	// not receiving fixes" under an unrelated field is how it goes missing.
	//
	// Read on its own rather than merged out of the syncer state files above:
	// which binary this install runs is not a per-agent fact, so it is one record
	// per profile. Two production installs sat on an old version for ten days
	// apiece while the daemon refetched the same artifact every five minutes, and
	// until this line there was nowhere a user could see that (#623).
	if st, ok := readUpdateStall(profileName); ok {
		if notice := updateStallNotice(st, version); notice != "" {
			fmt.Printf("    Update:        %s\n", notice)
		}
	}

	// Auth token
	if p.Server.AuthToken != "" {
		prefix := p.Server.AuthToken
		if len(prefix) > 12 {
			prefix = prefix[:12] + "..."
		}
		fmt.Printf("    Auth token:    %s\n", prefix)
	} else {
		fmt.Println("    Auth token:    (none)")
	}

	// --- Paths ---
	fmt.Println()
	fmt.Println("  PATHS")

	// Where the binary lives decides whether it can ever replace itself. A
	// root-owned directory makes every self-update fail at the last step, and
	// that failure is otherwise only a line in sync.log that scrolls away --
	// one client sat four versions behind for weeks before anyone looked (#459).
	if executable, err := os.Executable(); err == nil {
		fmt.Printf("    Binary:        %s\n", executable)
		// The other reason a binary never updates, reported in the same place as
		// the writable check because it answers the same question. A dev build
		// carries no version and no signing key -- Makefile ldflags inject both --
		// so all three update paths refuse it. That refusal is correct: replacing
		// a locally built binary would destroy whatever somebody was reproducing.
		// It was just silent, so the only way to learn it was to ask why automatic
		// updates were not happening (#406).
		if version == "dev" {
			fmt.Printf("                   [!] 개발 빌드 (version=dev) -- 자동 갱신 대상이 아닙니다\n")
			fmt.Printf("                       릴리스 바이너리로 교체하면 자동 갱신이 다시 동작합니다\n")
		}
		if !updateTargetWritable(executable) {
			fmt.Printf("                   [!] 자동 갱신 불가 -- %s 에 쓰기 권한이 없습니다\n", filepath.Dir(executable))
			fmt.Printf("                       교체 시 sudo 가 필요하고, 자동 갱신은 매번 실패합니다\n")
		}
	}

	// Claude config dir
	claudeDir := p.ClaudeConfigDir
	if claudeDir == "" {
		home, _ := os.UserHomeDir()
		claudeDir = filepath.Join(home, ".claude")
	}
	fmt.Printf("    Claude home:   %s\n", claudeDir)

	// Settings.json
	settingsPath, _ := envgen.ClaudeSettingsPath(p)
	if _, err := os.Stat(settingsPath); err == nil {
		fmt.Printf("    Settings:      %s (applied)\n", settingsPath)
	} else {
		fmt.Printf("    Settings:      %s (missing)\n", settingsPath)
	}

	// Profile dir
	fmt.Printf("    Profile dir:   %s\n", profileDir)

	// --- Options ---
	fmt.Println()
	fmt.Println("  OPTIONS")
	fmt.Printf("    Sync enabled:  %s\n", boolStatus(p.Options.SyncEnabled))
	// Shown only when on. A privacy setting has to be visible where a person checks
	// their configuration -- the flag this replaces was visible in `cctrace config`
	// and connected to nothing, so "the operator saw it" was never evidence that it
	// did anything. Printing it here, next to what is actually being collected, is
	// where the claim can be checked against the rest of the output.
	if p.Options.RedactUserPrompts {
		fmt.Println("    Prompts:       redacted before upload")
	}
	if p.Options.RedactToolDetails {
		fmt.Println("    Tool details:  redacted before upload")
	}

	// --- Named profiles ---
	if profileName == "" {
		names, err := profile.ListNamed()
		if err == nil && len(names) > 0 {
			fmt.Println()
			fmt.Println("  NAMED PROFILES")
			for _, name := range names {
				np, err := profile.LoadNamed(name)
				if err != nil {
					fmt.Printf("    [%s] (error loading)\n", name)
					continue
				}
				dir := np.ClaudeConfigDir
				if dir == "" {
					home, _ := os.UserHomeDir()
					dir = filepath.Join(home, ".claude")
				}
				tokenStatus := "no token"
				if np.Server.AuthToken != "" {
					t := np.Server.AuthToken
					if len(t) > 12 {
						t = t[:12] + "..."
					}
					tokenStatus = t
				}
				syncStatus := "-"
				if np.Server.SyncEndpoint != "" {
					syncStatus = np.Server.SyncEndpoint
				}
				fmt.Printf("    [%s]\n", name)
				fmt.Printf("      User:        %s <%s> (ID: %s)\n", np.User.Name, np.User.Email, valueOrDash(np.User.ID))
				fmt.Printf("      Team:        %s\n", valueOrDash(np.User.Team))
				fmt.Printf("      Claude home: %s\n", dir)
				fmt.Printf("      Sync:        %s\n", syncStatus)
				fmt.Printf("      Auth token:  %s\n", tokenStatus)
			}
		}
	}

	// --- Quota ---
	fmt.Println()
	fmt.Println("  QUOTA")
	u, err := usage.Fetch(p.ClaudeConfigDir)
	if err != nil {
		fmt.Printf("    (unavailable: %v)\n", err)
	} else {
		printWindow := func(label string, w *usage.UsageWindow) {
			if w == nil {
				return
			}
			reset := w.ResetTime()
			resetStr := ""
			if !reset.IsZero() {
				resetStr = fmt.Sprintf("  resets %s", reset.Local().Format("Jan 2 15:04"))
			}
			fmt.Printf("    %-14s %s%s\n", label, usage.FormatBar(w.Utilization, 20), resetStr)
		}
		printWindow("5h:", u.FiveHour)
		printWindow("7d:", u.SevenDay)
		printWindow("7d (sonnet):", u.SevenDaySonnet)
		if u.SevenDayOpus != nil {
			printWindow("7d (opus):", u.SevenDayOpus)
		}
	}

	fmt.Println()

	// Exit code 2 if server unreachable
	if p.Server.Endpoint != "" && !isEndpointReachable(p.Server.Endpoint) {
		os.Exit(2)
	}

	return nil
}

func checkEndpoint(endpoint string) string {
	if isEndpointReachable(endpoint) {
		return "[OK] connected"
	}
	return "[WARN] unreachable"
}

// checkHTTPHealth probes the sync endpoint from this process.
//
// The wording names the prober. It used to say "[OK] healthy", which is a claim
// about the server and was read as a claim about collection -- for two days it
// was true while the daemon beside it sent nothing (#712). What this call can
// honestly report is that *this* process reached the server, which is worth
// printing precisely because the daemon's own answer can differ.
func checkHTTPHealth(syncEndpoint string) string {
	u := strings.TrimRight(syncEndpoint, "/") + "/api/health"
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return "[WARN] 이 프로세스에서 서버에 닿지 않습니다"
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		return "[OK] 이 프로세스에서는 서버에 닿습니다"
	}
	return fmt.Sprintf("[WARN] 서버가 HTTP %d 를 돌려줍니다", resp.StatusCode)
}

// syncStatePaths returns every syncer state file for a profile — one per agent.
//
// It is a named function rather than a literal inside the status printer so a
// test can assert that the list covers all four agents. When it was a literal it
// silently kept listing two, and the notice below went missing for gjc and omo:
// the tests exercised the message it produces, never the set of files it reads
// from. Any new syncer belongs here as well as in sync.go.
func syncStatePaths(profileName string) []string {
	return []string{
		syncStatePathForProfile(profileName),
		codexsyncer.StatePathForProfile(profileName),
		gjcStatePathForProfile(profileName),
		omoStatePathForProfile(profileName),
	}
}

// firstRunSkipNotice describes content the first sync skipped, or "" when there
// was none.
//
// The first sync jumps every pre-existing session file to EOF so a new install
// does not backfill old history. That is correct, but it also catches the user's
// own session in progress — `cctrace init` is normally run from inside one — and
// the dropped part is silently absent from an upload that otherwise succeeds.
// The syncer logs it, yet the hook runs the daemon detached so nothing reaches a
// terminal, and that line rotates out of sync.log in time.
//
// So it is said here, because this is the one place the user is already sent:
// init signs off by telling them to run `cctrace status`.
func firstRunSkipNotice(files map[string]*syncer.FileState) string {
	var n int
	var total int64
	for _, fs := range files {
		if fs == nil || fs.SkippedAtFirstSync <= 0 {
			continue
		}
		n++
		total += fs.SkippedAtFirstSync
	}
	if n == 0 {
		return ""
	}
	noun := "files"
	if n == 1 {
		noun = "file"
	}
	// Both numbers, because they answer different questions: how much of my
	// history is gone, and how many sessions it touches.
	return fmt.Sprintf("[!] %d %s skipped at first sync (%s not collected)", n, noun, humanBytes(total))
}

// bodyLimitBlockNotice describes files whose sync is holding at a record the
// server refuses as too large, or "" when there are none.
//
// Nothing is lost while this shows: the bytes are still on disk and the offset is
// still in front of them, so raising the server's limit and syncing again
// collects everything, including the record that stopped it. That is only
// actionable if someone learns of it, and the syncer's line rotates out of
// sync.log -- so it is said here, beside the sync status, where init already
// sends the user.
func bodyLimitBlockNotice(files map[string]*syncer.FileState) string {
	var n int
	for _, fs := range files {
		if fs != nil && fs.BlockedByBodyLimit {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	noun := "sessions"
	if n == 1 {
		noun = "session"
	}
	// Says what to do, because the fix is on the server and the person reading
	// this is at a client.
	return fmt.Sprintf("[!] %d %s holding: a record exceeds the server's request size limit (raise CCTRACE_MAX_SYNC_BODY_BYTES on the server; nothing is lost meanwhile)", n, noun)
}

// rulesDeniedNotice describes repositories whose project rules the server
// refuses, or "" when there are none.
//
// The refusal is durable and otherwise invisible: the server decides it from
// session history, the client parks the repository and says so once in sync.log,
// and that line rotates away. What is left is a repository whose CLAUDE.md and
// AGENTS.md are simply never collected, with nothing anywhere saying so.
// Production ran this at 23k-41k refused posts a day for at least ten days
// before anyone noticed (#619).
//
// Named, not counted: unlike the body-limit hold, the fix is per repository and
// the person reading this has to know which one.
func rulesDeniedNotice(denied map[string]time.Time) string {
	if len(denied) == 0 {
		return ""
	}
	repos := make([]string, 0, len(denied))
	for repo := range denied {
		repos = append(repos, repo)
	}
	sort.Strings(repos)

	const shown = 3
	listed, suffix := repos, ""
	if len(repos) > shown {
		listed = repos[:shown]
		suffix = fmt.Sprintf(" and %d more", len(repos)-shown)
	}
	// Says what it means rather than what the server said. "Access denied" is
	// accurate and tells the reader nothing about their rules being missing.
	return fmt.Sprintf("[!] project rules not collected for %s%s: the server does not recognise you as an owner (its sessions have to reach the server first)",
		strings.Join(listed, ", "), suffix)
}

// serverExclusionShownFor is how long a server exclusion stays reported after
// the server last said so. Answers are refreshed only for accounts a pass
// still sees, so without a limit an account no longer used here would be
// reported for good.
const serverExclusionShownFor = 7 * 24 * time.Hour

// excludedAccountsNotice names the accounts whose records are consumed without
// being sent, or "" when there are none (#716).
//
// Skipping them is silent by design, and silence is also what a broken sync
// looks like, so the skip has to be visible somewhere. Each account says where
// its exclusion comes from: a local one is undone in this profile, a server one
// only by whoever excluded it there.
func excludedAccountsNotice(local []string, server map[string]time.Time, now time.Time) string {
	seen := map[string]bool{}
	var parts []string
	for _, a := range local {
		if !seen[a] {
			seen[a] = true
			parts = append(parts, a+" (options.exclude_accounts)")
		}
	}
	remote := make([]string, 0, len(server))
	for a, at := range server {
		if now.Sub(at) < serverExclusionShownFor {
			remote = append(remote, a)
		}
	}
	sort.Strings(remote)
	for _, a := range remote {
		// A bare local id matches the account under any provider.
		_, id, _ := strings.Cut(a, ":")
		if !seen[a] && !seen[id] {
			seen[a] = true
			parts = append(parts, a+" (excluded on the server)")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "[!] records not uploaded for " + strings.Join(parts, ", ")
}

// humanBytes renders a byte count at the precision a person reads at. The point
// of showing the size at all is that "a file was skipped" invites a shrug while
// "1.2 KB was skipped" does not.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

func isEndpointReachable(endpoint string) bool {
	host := endpoint

	u, err := url.Parse(endpoint)
	if err == nil && u.Host != "" {
		host = u.Host
	}

	if _, _, err := net.SplitHostPort(host); err != nil {
		host = host + ":4317"
	}

	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func valueOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func boolStatus(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

// daemonRestartNotice renders the SERVER-section warning shown when the running
// watch child is a different build from this binary, or "" when there is
// nothing to say.
//
// The stamp is what sync.go writes into sync-runtime.json when it starts the
// watch child (#103). When an update replaces the binary on disk but fails to
// replace the resident child, the two values split apart and stay split -- one
// client ran a v0.7.27 child against a v0.7.28 binary for five days, reporting
// v0.7.27 to the server the whole time, with nothing on screen saying so (#458).
//
// The whole notice is returned rather than printed, so the wording -- including
// the remedy, which is the part a person acts on -- is fixed by a test. Same
// shape as firstRunSkipNotice.
//
// Three cases stay silent: a dev build, no runtime file, and a runtime file
// without a stamp (a daemon older than #103). A false alarm on a status line is
// worse than silence -- the same principle as updateTargetWritable (#462).
//
// The comparison is plain inequality rather than semverGT: the fact worth
// reporting is "the running process differs from the disk", in either
// direction, and semverGT returns false when a version fails to parse, which
// would hide exactly the old daemons this looks for.
func daemonRestartNotice(profileName string, syncEnabled bool) string {
	if version == "dev" || !syncEnabled {
		return ""
	}
	rt, ok, err := readSyncRuntime(profileName)
	if err != nil || !ok || rt == nil {
		return ""
	}
	if rt.Version == "" || rt.Version == version {
		return ""
	}
	// A runtime file outlives the process it describes. Warning on a stale one
	// hands the user a remedy that cannot work: `sync --stop` writes a stop
	// request, waits for an exit that never comes, and fails after the timeout.
	//
	// The lock answers this, not a signal to rt.PID: os.Process.Signal supports
	// only os.Kill on Windows, so a signal-0 probe would read every Windows
	// daemon as dead -- and Windows is the platform in #458. Acquiring at zero
	// wait is non-invasive in the case that matters: a live daemon holds the
	// lock, so the acquire fails and nothing is released underneath it.
	if free, err := syncLockFreeFn(profileName); err != nil || free {
		return ""
	}
	profileArg := ""
	if profileName != "" {
		profileArg = " --profile " + profileName
	}
	return fmt.Sprintf("                   [!] 실행 중인 데몬은 %s, 이 바이너리는 %s -- 재시작이 필요합니다\n"+
		"                       cctrace sync --stop%s 뒤 cctrace sync --daemon%s 으로 다시 띄우면 새 바이너리가 적용됩니다",
		rt.Version, version, profileArg, profileArg)
}
