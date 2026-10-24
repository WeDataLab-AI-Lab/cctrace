package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(data, '\n'))
}

// gitEnv keeps git away from any configuration but the persona's own, and pins
// the dates so the same seed gives the same commit hashes.
func gitEnv(home string, at time.Time) []string {
	date := at.Format(time.RFC3339)
	return []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
}

func git(dir, home string, at time.Time, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, gitEnv(home, at)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

// initRepo makes the project a real repository with an origin remote: the client
// resolves repository names, branches and commits by running git in each
// session's working directory.
func initRepo(home string, p *persona, proj *project, at time.Time) error {
	dir := filepath.Join(home, "work", proj.Key)
	files := map[string]string{
		"README.md": docSnippet(proj, "README.md"),
		"CLAUDE.md": proj.Rules,
	}
	if proj.Org == "orbit" || proj.Key == "nebula-ingest" {
		files["AGENTS.md"] = proj.Rules
	}
	for _, c := range proj.Components {
		t := task{Project: proj, Comp: c}
		files[c.File] = snippet(c)
		files[c.Test] = testSnippet(t)
	}
	for _, d := range proj.Docs {
		files[d] = docSnippet(proj, d)
	}
	for name, body := range files {
		if err := writeFile(filepath.Join(dir, name), []byte(body)); err != nil {
			return err
		}
	}
	steps := [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", p.Name},
		{"config", "user.email", p.Email},
		{"add", "-A"},
		{"commit", "-q", "-m", "Initial import"},
		{"remote", "add", "origin", fmt.Sprintf("https://git.example.com/%s/%s.git", proj.Org, proj.Key)},
	}
	for _, s := range steps {
		if err := git(dir, home, at, s...); err != nil {
			return err
		}
	}
	return nil
}

// prepareHome lays out one persona's machine: repositories, the Claude Code and
// Codex logins, the cctrace profile, a sync state that already knows the
// accounts, and every transcript.
func prepareHome(root string, p *persona, acct accounts, token, server, otlpGRPC string, pl *plan) (string, int, error) {
	home := filepath.Join(root, p.Home)
	earliest := pl.Anchor.AddDate(0, 0, -pl.Days-2)
	for _, key := range p.Projects {
		if err := initRepo(home, p, projects[key], earliest); err != nil {
			return "", 0, err
		}
	}
	if err := writeJSON(filepath.Join(home, ".claude.json"), map[string]any{
		"hasCompletedOnboarding": true,
		"oauthAccount": map[string]any{"accountUuid": acct.ClaudeAccount, "emailAddress": p.Email,
			"organizationUuid": acct.ClaudeOrg, "organizationType": "claude_max", "displayName": p.Name},
	}); err != nil {
		return "", 0, err
	}
	if err := writeJSON(filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"OPENAI_API_KEY": nil, "tokens": map[string]any{"account_id": acct.CodexAccount},
	}); err != nil {
		return "", 0, err
	}

	if err := writePlugins(home); err != nil {
		return "", 0, err
	}

	prof := profile.NewDefault()
	prof.User = profile.UserInfo{ID: p.UserID, Name: p.Name, Email: p.Email, Team: p.Team}
	prof.Server = profile.ServerInfo{Endpoint: otlpGRPC, SyncEndpoint: server, Protocol: "grpc", AuthToken: token}
	prof.Options.CodexSyncEnabled = true
	prof.CreatedAt, prof.UpdatedAt = earliest, earliest
	// Saved the way `cctrace init` saves it: the client's profile registry
	// refuses a hand-written file, and a profile it cannot list reads nothing.
	prof.ID = "default"
	if err := profile.SaveTo(prof, filepath.Join(home, ".cctrace", "profile.json")); err != nil {
		return "", 0, err
	}

	// A state file that exists makes every transcript new, so it is read from the
	// start; the observations dated before the first session give old records
	// their account instead of leaving them unattributed.
	claudeState, err := syncer.LoadState(filepath.Join(home, ".cctrace", "sync-state.json"))
	if err != nil {
		return "", 0, err
	}
	claudeState.ClaudeAccountObservations = []syncer.ClaudeAccountObservation{{ObservedAt: earliest, Home: filepath.Join(home, ".claude"), AccountUUID: acct.ClaudeAccount}}
	if err := claudeState.Save(); err != nil {
		return "", 0, err
	}
	codexState, err := syncer.LoadState(filepath.Join(home, ".cctrace", "codex-sync-state.json"))
	if err != nil {
		return "", 0, err
	}
	codexState.CodexAccountObservations = []syncer.CodexAccountObservation{{ObservedAt: earliest, Home: filepath.Join(home, ".codex"), AccountID: acct.CodexAccount}}
	if err := codexState.Save(); err != nil {
		return "", 0, err
	}

	written := 0
	for _, s := range pl.Sessions {
		if s.P != p {
			continue
		}
		var path string
		var data []byte
		if s.Agent == "claude" {
			cwd := filepath.Join(home, "work", s.Proj.Key)
			path = filepath.Join(home, ".claude", "projects", claudeProjectDir(cwd), s.ID+".jsonl")
			data = claudeTranscript(home, s)
		} else {
			path = filepath.Join(home, ".codex", codexRolloutPath(s))
			data = codexRollout(home, s)
		}
		if err := writeFile(path, data); err != nil {
			return "", 0, err
		}
		mtime := s.End
		_ = os.Chtimes(path, mtime, mtime)
		written += len(data)
	}
	return home, written, nil
}

// runSync uploads the persona's transcripts with the real client, as a one-shot
// sync with nothing inherited from this process's environment.
func runSync(bin, home, server string) (string, error) {
	cmd := exec.Command(bin, "sync", "--endpoint", server)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TZ=Asia/Seoul"}
	cmd.Dir = home
	out, err := cmd.CombinedOutput()
	return string(out), err
}
