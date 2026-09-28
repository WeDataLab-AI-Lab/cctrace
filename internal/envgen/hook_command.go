package envgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

var (
	executablePath = os.Executable
	lookPath       = exec.LookPath
)

// isCctraceHook returns true if the hook command belongs to cctrace.
// Matches "cctrace sync ...", full-path variants like
// "/c/Users/.../cctrace.exe sync ...", temp/test variants like
// "/private/tmp/cctrace-test sync ...", hooks written by a distribution binary
// run in place ("/usr/local/bin/cctrace-linux sync ... --claude-dir ..."), and
// the current running binary. See isManagedHookBinary for why a cctrace-ish
// name alone is not enough.
func isCctraceHook(cmd string) bool {
	binPart, rest, ok := firstShellToken(cmd)
	if !ok {
		return false
	}
	subcommand, _, ok := firstShellToken(rest)
	if ok && subcommand == "sync" && isManagedHookBinary(binPart, true, hasGeneratedSyncFlags(cmd)) {
		return true
	}
	if legacyBin, ok := legacyUnquotedBinaryBeforeSync(cmd); ok &&
		isManagedHookBinary(legacyBin, false, hasGeneratedSyncFlags(cmd)) {
		return true
	}
	return false
}

// hasGeneratedSyncFlags reports whether the command carries a flag that only
// our own generated hooks pass. syncCommands always emits --claude-dir, and a
// foreign tool that happens to expose a "sync" subcommand does not.
func hasGeneratedSyncFlags(cmd string) bool {
	return strings.Contains(cmd, "--claude-dir")
}

func isManagedHookBinary(binPart string, allowBare, generatedFlags bool) bool {
	if !allowBare && !isPathLikeHookBinaryCandidate(binPart) {
		return false
	}
	baseLower := strings.ToLower(filepath.Base(strings.ToLower(binPart)))
	// Only the exact file name counts. Neither a suffix ("my-cctrace",
	// "notcctrace") nor a prefix ("cctrace-audit") tells our binary apart from
	// somebody else's, and claiming one means apply overwrites their hook and
	// reset deletes it. The settings guard cannot catch that either: it selects
	// the hooks to protect with this same predicate, so anything we misclaim is
	// excluded from the protected set.
	//
	// A hook written by a differently-named binary (a downloaded
	// cctrace-darwin-arm64 run in place) is still recognized by the
	// running-binary path check below. If that binary is gone the hook is left
	// behind on reset — a visible leftover, which is the safer failure than
	// deleting a file we do not own.
	managed := baseLower == "cctrace" || baseLower == "cctrace.exe"
	// A cctrace-prefixed name counts when something else also marks the hook as
	// ours, because the name alone cannot separate our cctrace-linux from
	// somebody's cctrace-audit:
	//
	//   - the command carries a flag only syncCommands generates, so it is a
	//     hook we wrote. The quickstart tells Linux and Windows users to run the
	//     downloaded binary in place, so these are the hooks those installs
	//     produce; not recognizing them makes upsertHookCommand append a second
	//     hook on every later run.
	//   - or the path is somewhere we ourselves refuse to write a stable hook to
	//     (a temp dir, a *.test binary), which only our own leftovers occupy.
	//     Recognizing those is what lets apply rewrite them to the installed
	//     path.
	if !managed && (strings.HasPrefix(baseLower, "cctrace-") || strings.HasPrefix(baseLower, "cctrace.")) &&
		(generatedFlags || shouldAvoidHookPath(binPart)) {
		managed = true
	}
	if managed && (allowBare || strings.ContainsAny(binPart, `/\`)) {
		return true
	}
	if cur := hookBinaryPath(); cur != "cctrace" {
		if strings.EqualFold(binPart, cur) {
			return true
		}
	}
	return false
}

func isPathLikeHookBinaryCandidate(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) ||
		strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") ||
		strings.HasPrefix(value, "~/") {
		return true
	}
	if len(value) >= 3 && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		drive := value[0]
		return ('a' <= drive && drive <= 'z') || ('A' <= drive && drive <= 'Z')
	}
	return false
}

func legacyUnquotedBinaryBeforeSync(cmd string) (string, bool) {
	idx := strings.Index(cmd, " sync ")
	if idx < 0 {
		return "", false
	}
	bin := strings.TrimSpace(cmd[:idx])
	if bin == "" || strings.HasPrefix(bin, "'") || strings.HasPrefix(bin, `"`) {
		return "", false
	}
	return bin, true
}

func firstShellToken(cmd string) (string, string, bool) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", "", false
	}
	if cmd[0] != '\'' && cmd[0] != '"' {
		for i, r := range cmd {
			if unicode.IsSpace(r) {
				return cmd[:i], strings.TrimSpace(cmd[i:]), true
			}
		}
		return cmd, "", true
	}

	if cmd[0] == '"' {
		var b strings.Builder
		escaped := false
		for i := 1; i < len(cmd); i++ {
			switch {
			case escaped:
				b.WriteByte(cmd[i])
				escaped = false
			case cmd[i] == '\\':
				escaped = true
			case cmd[i] == '"':
				return b.String(), strings.TrimSpace(cmd[i+1:]), true
			default:
				b.WriteByte(cmd[i])
			}
		}
		return "", "", false
	}

	var b strings.Builder
	for i := 1; i < len(cmd); {
		if cmd[i] != '\'' {
			b.WriteByte(cmd[i])
			i++
			continue
		}
		if i+3 < len(cmd) && cmd[i+1] == '\\' && cmd[i+2] == '\'' && cmd[i+3] == '\'' {
			b.WriteByte('\'')
			i += 4
			continue
		}
		return b.String(), strings.TrimSpace(cmd[i+1:]), true
	}
	return "", "", false
}

// hookBinaryPath returns the full path of the running cctrace binary in a
// bash-compatible form. On Windows, backslashes are converted to forward
// slashes and the drive letter is rewritten (e.g. C:\ → /c/).
func hookBinaryPath() string {
	rawExe, err := executablePath()
	if err == nil {
		exe := rawExe
		if resolved, err2 := filepath.EvalSymlinks(rawExe); err2 == nil {
			exe = resolved
		}
		if !shouldAvoidHookPath(exe) {
			return normalizeHookPath(exe)
		}
	}

	if pathExe, err := lookPath("cctrace"); err == nil {
		if resolved, err2 := filepath.EvalSymlinks(pathExe); err2 == nil {
			pathExe = resolved
		}
		if !shouldAvoidHookPath(pathExe) {
			return normalizeHookPath(pathExe)
		}
	}

	if err == nil && rawExe != "" && !shouldAvoidHookPath(rawExe) {
		return normalizeHookPath(rawExe)
	}
	return "cctrace"
}

func shouldAvoidHookPath(path string) bool {
	if path == "" {
		return true
	}

	base := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(base, ".test") {
		return true
	}

	clean := filepath.Clean(path)
	tmpRoots := []string{
		os.TempDir(),
		"/tmp",
		"/private/tmp",
	}
	for _, root := range tmpRoots {
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			if isPathUnder(clean, filepath.Clean(resolved)) {
				return true
			}
		}
		if isPathUnder(clean, root) {
			return true
		}
	}
	return false
}

func isPathUnder(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(os.PathSeparator))
}

func normalizeHookPath(path string) string {
	if runtime.GOOS == "windows" {
		path = filepath.ToSlash(path)
		if len(path) >= 2 && path[1] == ':' {
			path = "/" + strings.ToLower(string(path[0])) + path[2:]
		}
	}
	return path
}

// IsCctraceInPath checks whether 'cctrace' is findable in $PATH.
func IsCctraceInPath() bool {
	_, err := lookPath("cctrace")
	return err == nil
}
