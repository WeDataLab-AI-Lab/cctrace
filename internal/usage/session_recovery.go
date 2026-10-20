package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ClaudeSessionRecoverySupported deliberately limits owner startup to the OS
// where the selected Keychain ownership contract has been verified.
func ClaudeSessionRecoverySupported() bool { return runtime.GOOS == "darwin" }

type recoveryDependencies struct {
	read                     func(context.Context, string) (Credentials, error)
	command                  func([]string) *exec.Cmd
	ownerStarted             func()
	environment              []string
	now                      func() time.Time
	interval, timeout, grace time.Duration
}

// RecoverClaudeSession is an explicit owner action. No polling path calls it.
// Claude owns all credential writes; this process never sends PTY input.
func RecoverClaudeSession(ctx context.Context, home, support string) error {
	if !ClaudeSessionRecoverySupported() {
		return ErrRecoveryBlocked
	}
	return recoverClaudeSession(ctx, home, support, recoveryDependencies{
		read: credentialsForContext, command: func(args []string) *exec.Cmd {
			binary, err := exec.LookPath("claude")
			if err != nil {
				return nil
			}
			return exec.Command(binary, args...)
		},
		environment: os.Environ(), now: time.Now, interval: 100 * time.Millisecond, timeout: 8 * time.Second, grace: 100 * time.Millisecond,
	})
}

func recoverClaudeSession(ctx context.Context, home, support string, d recoveryDependencies) error {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	err := recoverClaudeSessionWithin(ctx, home, support, d)
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrRecoveryBlocked
	}
	return err
}

func recoverClaudeSessionWithin(ctx context.Context, home, support string, d recoveryDependencies) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(home) || !filepath.IsAbs(support) || !recoveryHomeCurrent(home) {
		return ErrRecoveryBlocked
	}
	before, err := d.read(ctx, home)
	if err != nil {
		return err
	}
	if before.AccessToken != "" && (before.ExpiresAt.IsZero() || before.ExpiresAt.After(d.now())) {
		return nil
	}
	if before.AccessToken == "" || !before.RefreshAvailable {
		return ErrRecoveryBlocked
	}
	dir, err := recoveryDirectory(support, home)
	if err != nil {
		return ErrRecoveryBlocked
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return ErrRecoveryBlocked
	}
	args := []string{"--safe-mode", "--no-chrome", "--tools", "", "--strict-mcp-config", "--settings", `{"remoteControlAtStartup":false,"disableAllHooks":true}`, "--session-id", id.String()}
	cmd := d.command(args)
	if cmd == nil {
		return ErrRecoveryBlocked
	}
	cmd.Dir = dir
	cmd.Env = recoveryEnvironment(d.environment, home)
	return runRecoveryOwner(ctx, cmd, home, before, d)
}

func recoveryEnvironment(base []string, home string) []string {
	allowed := map[string]bool{"HOME": true, "PATH": true, "USER": true, "LOGNAME": true, "SHELL": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true}
	env := []string{}
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			env = append(env, entry)
		}
	}
	return append(env, "CLAUDE_CONFIG_DIR="+home, "DISABLE_AUTOUPDATER=1", "TERM=xterm-256color")
}

func recoveryDirectory(root, home string) (string, error) {
	clean := filepath.Clean(root)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || resolved != clean {
		return "", ErrRecoveryBlocked
	}
	sum := sha256.Sum256([]byte(home))
	dir := clean
	for _, part := range []string{"claude-session-recovery", hex.EncodeToString(sum[:])} {
		dir = filepath.Join(dir, part)
		if e := os.Mkdir(dir, 0700); e != nil && !os.IsExist(e) {
			return "", ErrRecoveryBlocked
		}
		info, e := os.Lstat(dir)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || !recoveryDirectoryOwned(info) {
			return "", ErrRecoveryBlocked
		}
		actual, e := filepath.EvalSymlinks(dir)
		if e != nil || actual != dir {
			return "", ErrRecoveryBlocked
		}
	}
	return dir, nil
}

var recoveryANSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

func recoveryPrompt(output string) bool {
	scan := strings.ToLower(strings.Join(strings.Fields(recoveryANSI.ReplaceAllString(output, "")), " "))
	for _, word := range []string{"log in", "login", "sign in", "sign-in", "oauth/authorize", "/oauth", "open your browser", "onboarding", "do you trust", "trust this folder", "quick safety check", "ready to code here", "unknown option", "unrecognized option", "unsupported flag", "press enter", "welcome to claude", "choose a theme"} {
		if strings.Contains(scan, word) {
			return true
		}
	}
	return false
}

func recoveryHomeCurrent(home string) bool {
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !recoveryDirectoryOwned(info) {
		return false
	}
	actual, err := filepath.EvalSymlinks(home)
	return err == nil && actual == home
}
