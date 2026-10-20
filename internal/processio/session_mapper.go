package processio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cctrace/internal/codexlog"
)

var (
	ErrSessionMappingNotReady    = errors.New("launched process has not exposed a verifiable session yet")
	ErrSessionMappingAmbiguous   = errors.New("launched process has multiple open session logs")
	ErrSessionMappingUnavailable = errors.New("the launched process could not be mapped to one session")
)

const maxSessionMetaLineBytes = 64 << 10
const sessionMappingPollInterval = 100 * time.Millisecond

type SessionMapping struct {
	Agent           string
	SessionID       string
	PID             int
	ProcStart       string
	Source          string
	Status          string
	StatusSource    string
	WaitingKind     string
	StatusChangedAt *time.Time
}

// CheckSessionMappingSupport detects whether this platform can inspect only the
// launched process's own state file or open files before starting an agent.
func CheckSessionMappingSupport(agent, home string) error {
	if agent != "claude" && agent != "codex" {
		return fmt.Errorf("unsupported session mapping agent %q", agent)
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return errors.New("selected agent home is unavailable for safe session mapping")
	}
	if _, err := processStartToken(os.Getpid()); err != nil {
		return errors.New("process start inspection is unavailable for safe session mapping")
	}
	if agent == "codex" {
		if err := checkOpenFileMappingSupport(); err != nil {
			return err
		}
	}
	return nil
}

// ResolveSessionMapping accepts only a live Claude PID state file or a single
// Codex rollout JSONL file open by that exact PID under the selected home.
func ResolveSessionMapping(agent, home string, pid int) (SessionMapping, error) {
	if pid <= 0 {
		return SessionMapping{}, errors.New("launched process has no valid PID")
	}
	if agent == "claude" {
		return resolveClaudeMapping(home, pid)
	}
	if agent == "codex" {
		return resolveCodexMapping(home, pid)
	}
	return SessionMapping{}, fmt.Errorf("unsupported session mapping agent %q", agent)
}

// VerifySessionMapping rechecks the exact process/session identity before a
// later counter snapshot is attributed. A PID that now names another session
// or process is never silently kept under the original session ID.
func VerifySessionMapping(agent, home string, pid int, expected SessionMapping) (SessionMapping, error) {
	current, err := ResolveSessionMapping(agent, home, pid)
	if err != nil {
		return SessionMapping{}, fmt.Errorf("%w: %v", ErrSessionMappingUnavailable, err)
	}
	if current.Agent != expected.Agent || current.PID != expected.PID || current.SessionID != expected.SessionID || current.ProcStart != expected.ProcStart {
		return SessionMapping{}, ErrSessionMappingUnavailable
	}
	return current, nil
}

func WaitForSessionMapping(ctx context.Context, agent, home string, pid int) (SessionMapping, error) {
	ticker := time.NewTicker(sessionMappingPollInterval)
	defer ticker.Stop()
	for {
		mapping, err := ResolveSessionMapping(agent, home, pid)
		if err == nil || !errors.Is(err, ErrSessionMappingNotReady) {
			return mapping, err
		}
		select {
		case <-ctx.Done():
			return SessionMapping{}, fmt.Errorf("%w: %v", ErrSessionMappingUnavailable, ctx.Err())
		case <-ticker.C:
		}
	}
}

type claudeProcessFile struct {
	PID             int     `json:"pid"`
	SessionID       string  `json:"sessionId"`
	ProcStart       string  `json:"procStart"`
	ProcStartFt     string  `json:"procStartFt"`
	Status          string  `json:"status"`
	WaitingFor      string  `json:"waitingFor"`
	StatusUpdatedAt float64 `json:"statusUpdatedAt"`
}

func resolveClaudeMapping(home string, pid int) (SessionMapping, error) {
	before, err := processStartToken(pid)
	if err != nil {
		return SessionMapping{}, ErrSessionMappingNotReady
	}
	path := filepath.Join(home, "sessions", fmt.Sprintf("%d.json", pid))
	first, err := readClaudeProcessFile(path)
	if err != nil || first.PID != pid || !validSessionID(first.SessionID) {
		return SessionMapping{}, fmt.Errorf("claude PID state file is not ready: %w", ErrSessionMappingNotReady)
	}
	second, err := readClaudeProcessFile(path)
	if err != nil || second.PID != pid || second.SessionID != first.SessionID || claudeProcStart(second) != claudeProcStart(first) {
		return SessionMapping{}, fmt.Errorf("claude PID state changed while being verified: %w", ErrSessionMappingNotReady)
	}
	if !matchesClaudeProcStart(pid, first.ProcStart, first.ProcStartFt) {
		return SessionMapping{}, fmt.Errorf("claude PID start token does not match its state file: %w", ErrSessionMappingNotReady)
	}
	after, err := processStartToken(pid)
	if err != nil || before != after {
		return SessionMapping{}, fmt.Errorf("claude PID changed while being verified: %w", ErrSessionMappingNotReady)
	}
	procStart := claudeProcStart(first)
	if procStart == "" {
		return SessionMapping{}, ErrSessionMappingNotReady
	}
	status, statusSource := "unknown", "process_io_unknown"
	if second.Status == "busy" || second.Status == "idle" || second.Status == "waiting" || second.Status == "shell" {
		status, statusSource = second.Status, "claude_process_file"
	}
	var statusChangedAt *time.Time
	if second.StatusUpdatedAt > 0 {
		changed := time.UnixMilli(int64(second.StatusUpdatedAt)).UTC()
		statusChangedAt = &changed
	}
	return SessionMapping{
		Agent: agentClaude, SessionID: first.SessionID, PID: pid, ProcStart: procStart, Source: "claude_pid_state",
		Status: status, StatusSource: statusSource, WaitingKind: processWaitingKind(second.WaitingFor), StatusChangedAt: statusChangedAt,
	}, nil
}

const agentClaude = "claude"

func readClaudeProcessFile(path string) (claudeProcessFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return claudeProcessFile{}, err
	}
	defer file.Close()
	var out claudeProcessFile
	if err := json.NewDecoder(io.LimitReader(file, 256<<10)).Decode(&out); err != nil {
		return claudeProcessFile{}, err
	}
	return out, nil
}

func claudeProcStart(file claudeProcessFile) string {
	if file.ProcStartFt != "" {
		return file.ProcStartFt
	}
	return file.ProcStart
}

func resolveCodexMapping(home string, pid int) (SessionMapping, error) {
	before, err := processStartToken(pid)
	if err != nil {
		return SessionMapping{}, ErrSessionMappingNotReady
	}
	paths, err := processOpenFiles(pid)
	if err != nil {
		return SessionMapping{}, ErrSessionMappingNotReady
	}
	candidates, err := rolloutCandidates(home, paths)
	if err != nil {
		return SessionMapping{}, err
	}
	if len(candidates) == 0 {
		return SessionMapping{}, ErrSessionMappingNotReady
	}
	if len(candidates) != 1 {
		return SessionMapping{}, ErrSessionMappingAmbiguous
	}
	path, id := candidates[0].path, candidates[0].sessionID
	if err := verifyCodexSessionMeta(path, id); err != nil {
		return SessionMapping{}, err
	}
	after, err := processStartToken(pid)
	if err != nil || before != after {
		return SessionMapping{}, ErrSessionMappingNotReady
	}
	pathsAfter, err := processOpenFiles(pid)
	if err != nil || !containsResolvedPath(pathsAfter, path) {
		return SessionMapping{}, ErrSessionMappingNotReady
	}
	return SessionMapping{
		Agent: "codex", SessionID: id, PID: pid, ProcStart: before, Source: "codex_open_rollout",
		Status: "unknown", StatusSource: "process_io_unknown",
	}, nil
}

func processWaitingKind(waitingFor string) string {
	switch waitingFor {
	case "":
		return ""
	case "input needed":
		return "input"
	case "permission prompt":
		return "permission"
	case "dialog open":
		return "dialog"
	case "sandbox request":
		return "sandbox"
	case "worker request":
		return "worker"
	case "goal proposal":
		return "goal"
	default:
		return "other"
	}
}

type rolloutCandidate struct {
	path      string
	sessionID string
}

func rolloutCandidates(home string, paths []string) ([]rolloutCandidate, error) {
	homePath, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil, ErrSessionMappingNotReady
	}
	sessionsRoot, err := filepath.EvalSymlinks(filepath.Join(homePath, "sessions"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, ErrSessionMappingNotReady
	}
	seen := map[string]bool{}
	var out []rolloutCandidate
	for _, openPath := range paths {
		if !strings.HasSuffix(strings.ToLower(openPath), ".jsonl") {
			continue
		}
		resolved, err := filepath.EvalSymlinks(openPath)
		if err != nil {
			continue
		}
		rootForCheck, pathForCheck := sessionsRoot, resolved
		if runtime.GOOS == "windows" {
			rootForCheck, pathForCheck = strings.ToLower(rootForCheck), strings.ToLower(pathForCheck)
		}
		rel, err := filepath.Rel(rootForCheck, pathForCheck)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		id := codexlog.SessionIDFromPath(resolved)
		if !validSessionID(id) || seen[resolved] {
			continue
		}
		seen[resolved] = true
		out = append(out, rolloutCandidate{path: resolved, sessionID: id})
	}
	return out, nil
}

func verifyCodexSessionMeta(path, sessionID string) error {
	file, err := os.Open(path)
	if err != nil {
		return ErrSessionMappingNotReady
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, maxSessionMetaLineBytes+1))
	scanner.Buffer(make([]byte, 4096), maxSessionMetaLineBytes)
	if !scanner.Scan() {
		return ErrSessionMappingNotReady
	}
	var record struct {
		Type    string `json:"type"`
		Payload struct {
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil || record.Type != "session_meta" {
		return ErrSessionMappingNotReady
	}
	ids := []string{record.Payload.ID, record.Payload.SessionID}
	matched := false
	for _, id := range ids {
		if id == "" {
			continue
		}
		if id != sessionID {
			return errors.New("open Codex rollout ID does not match its session metadata")
		}
		matched = true
	}
	if !matched {
		return ErrSessionMappingNotReady
	}
	return nil
}

func containsResolvedPath(paths []string, expected string) bool {
	for _, path := range paths {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil && filepath.Clean(resolved) == filepath.Clean(expected) {
			return true
		}
	}
	return false
}

func validSessionID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}
