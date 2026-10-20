package gitctx

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

const maxReflogEntries = 2000
const maxReflogBytes = 4 << 20

type Commit struct {
	Hash     string
	At       int64
	ReflogAt int64
	Parents  int
	Kind     string
	Subject  string
	Replaces string
}

type CommitLog struct {
	Items     []Commit
	Branch    string
	Truncated bool
}

type cappedGitOutput struct {
	bytes.Buffer
	exceeded bool
}

func (w *cappedGitOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > maxReflogBytes {
		w.exceeded = true
		return 0, errors.New("git output exceeds collection limit")
	}
	return w.Buffer.Write(p)
}

// ReadCommitLog reads only this work tree's HEAD reflog. Returned subjects are
// ephemeral candidates: callers must verify a call receipt before persisting one.
func ReadCommitLog(ctx context.Context, root string) (CommitLog, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "--no-optional-locks", "log", "-g", "--date=unix", "-n", strconv.Itoa(maxReflogEntries), "--format=%H%x00%gd%x00%gs%x00%ct%x00%P%x00%s%x1e", "HEAD")
	cmd.WaitDelay = commandWaitDelay
	var out cappedGitOutput
	cmd.Stdout = &out
	err := cmd.Run()
	if out.exceeded {
		return CommitLog{}, errors.New("reflog output incomplete")
	}
	if err != nil {
		return CommitLog{}, err
	}
	log := parseCommitLog(out.Bytes())
	branch, err := gitOutput(ctx, root, "branch", "--show-current")
	if err != nil {
		return CommitLog{}, err
	}
	log.Branch = strings.TrimSpace(string(branch))
	return log, nil
}

func parseCommitLog(raw []byte) CommitLog {
	log := CommitLog{}
	rows := strings.Split(string(raw), "\x1e")
	type entry struct {
		hash   string
		fields []string
	}
	var entries []entry
	for _, row := range rows {
		fields := strings.Split(strings.TrimPrefix(row, "\n"), "\x00")
		if len(fields) != 6 {
			if strings.TrimSpace(row) != "" {
				log.Truncated = true
			}
			continue
		}
		entries = append(entries, entry{fields[0], fields})
	}
	log.Truncated = log.Truncated || len(entries) >= maxReflogEntries
	for i, e := range entries {
		f := e.fields
		if !fullCommitHash(e.hash) {
			log.Truncated = true
			continue
		}
		selector := strings.TrimSuffix(strings.TrimPrefix(f[1], "HEAD@{"), "}")
		reflogAt, err := strconv.ParseInt(selector, 10, 64)
		if err != nil {
			log.Truncated = true
			continue
		}
		at, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			log.Truncated = true
			continue
		}
		if delta := at - reflogAt; delta < -1 || delta > 1 {
			continue
		}
		action, detail, _ := strings.Cut(f[2], ":")
		if strings.Contains(detail, "Fast-forward") {
			continue
		}
		parents := len(strings.Fields(f[4]))
		kind := ""
		switch {
		case strings.Contains(action, "rebase"):
			continue
		case action == "commit" || action == "commit (initial)":
			kind = "commit"
		case action == "commit (amend)":
			kind = "amend"
		case action == "cherry-pick" || action == "commit (cherry-pick)":
			kind = "cherry_pick"
		case action == "revert" || action == "commit (revert)":
			kind = "revert"
		case parents >= 2 && (action == "commit (merge)" || strings.HasPrefix(action, "merge ") || strings.HasPrefix(action, "pull ")):
			kind = "merge"
		default:
			continue
		}
		item := Commit{Hash: e.hash, At: at, ReflogAt: reflogAt, Parents: parents, Kind: kind, Subject: f[5]}
		if kind == "amend" && i+1 < len(entries) && fullCommitHash(entries[i+1].hash) {
			item.Replaces = entries[i+1].hash
		}
		log.Items = append(log.Items, item)
	}
	return log
}

func fullCommitHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
