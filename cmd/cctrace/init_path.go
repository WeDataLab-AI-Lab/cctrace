package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// offerInstallToPath attempts to copy the current binary to a system PATH location.
// Returns true if installation succeeded.
func offerInstallToPath(ir *inputReader) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err == nil {
		exe = resolved
	}

	// Determine install target based on OS
	var installDir, installPath string
	switch runtime.GOOS {
	case "darwin", "linux":
		installDir = "/usr/local/bin"
		installPath = filepath.Join(installDir, "cctrace")
	case "windows":
		home, _ := os.UserHomeDir()
		installDir = filepath.Join(home, "AppData", "Local", "Programs", "cctrace")
		installPath = filepath.Join(installDir, "cctrace.exe")
	default:
		return false
	}

	// Don't offer if already at the install target
	if exe == installPath {
		return false
	}

	fmt.Println()
	answer := ir.Prompt(fmt.Sprintf("  [!] 'cctrace' is not in $PATH. Install to %s? (y/n)", installDir), "y")
	answer = strings.ToLower(answer)
	if answer != "" && answer != "y" && answer != "yes" {
		return false
	}

	// Try direct copy first
	if err := copyFile(exe, installPath); err == nil {
		os.Chmod(installPath, 0755)
		fmt.Printf("  [OK] Installed to %s\n", installPath)
		if runtime.GOOS == "windows" {
			addToWindowsPath(installDir)
		}
		return true
	}

	// Direct copy failed (permission denied) — try with sudo on Unix
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		fmt.Printf("  Installing with sudo...\n")
		cmd := exec.Command("sudo", "cp", exe, installPath)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err == nil {
			exec.Command("sudo", "chmod", "755", installPath).Run()
			fmt.Printf("  [OK] Installed to %s\n", installPath)
			return true
		}
		fmt.Printf("  [!] Installation failed.\n")
	}

	return false
}

// addToWindowsPath adds dir to the user's PATH via setx if not already present.
func addToWindowsPath(dir string) {
	currentPath := os.Getenv("PATH")
	dirLower := strings.ToLower(dir)
	for _, p := range filepath.SplitList(currentPath) {
		if strings.ToLower(p) == dirLower {
			return // already in PATH
		}
	}

	// Read user PATH from registry (setx operates on user-level PATH, not system)
	out, err := exec.Command("reg", "query", `HKCU\Environment`, "/v", "Path").Output()
	if err != nil {
		fmt.Printf("  [!] Could not read user PATH from registry: %v\n", err)
		fmt.Printf("  [!] Add %s to your PATH manually.\n", dir)
		return
	}

	// Parse registry output to get current user PATH
	userPath := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(strings.ToLower(line), "path") && strings.Contains(line, "REG_") {
			parts := strings.SplitN(line, "REG_EXPAND_SZ", 2)
			if len(parts) < 2 {
				parts = strings.SplitN(line, "REG_SZ", 2)
			}
			if len(parts) == 2 {
				userPath = strings.TrimSpace(parts[1])
			}
		}
	}

	// Check if already in user PATH
	for _, p := range strings.Split(userPath, ";") {
		if strings.ToLower(strings.TrimSpace(p)) == dirLower {
			return
		}
	}

	// Append and set via setx
	newPath := userPath
	if newPath != "" {
		newPath += ";"
	}
	newPath += dir

	if err := exec.Command("setx", "PATH", newPath).Run(); err != nil {
		fmt.Printf("  [!] Failed to add to PATH: %v\n", err)
		fmt.Printf("  [!] Add %s to your PATH manually.\n", dir)
		return
	}
	fmt.Printf("  [OK] Added %s to user PATH (restart terminal to apply)\n", dir)
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	// Ensure parent directory exists
	os.MkdirAll(filepath.Dir(dst), 0755)

	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0755)
}

// pathInstallOption is one way to put cctrace on $PATH. Options are collected
// first and lettered at print time: the go-install option only exists when Go
// does, so a hard-coded "b)" on the copy option left binary users staring at a
// "choose one:" list whose only entry was b).
type pathInstallOption struct {
	title string
	steps []string
}

// pathInstallOptions returns the platform's options, most convenient first.
func pathInstallOptions(goos string, hasGo bool, exe string) []pathInstallOption {
	copyTarget := "./cctrace"
	if exe != "" {
		copyTarget = exe
	}
	goInstall := pathInstallOption{
		title: "go install:",
		steps: []string{"go install ./cmd/cctrace", "# make sure ~/go/bin is on your $PATH"},
	}
	unixCopy := pathInstallOption{
		title: "copy the binary:",
		steps: []string{fmt.Sprintf("sudo cp %s /usr/local/bin/cctrace", copyTarget)},
	}

	var opts []pathInstallOption
	switch goos {
	case "darwin", "linux":
		if hasGo {
			opts = append(opts, goInstall)
		}
		opts = append(opts, unixCopy)
	case "windows":
		if hasGo {
			opts = append(opts, goInstall)
		}
		opts = append(opts, pathInstallOption{
			title: "copy the binary into a directory that is on your Path:",
			steps: []string{
				`e.g. C:\Users\<user>\AppData\Local\Programs\cctrace\`,
				"then add that directory under System Environment Variables > Path",
			},
		})
	default:
		steps := []string{}
		if exe != "" {
			steps = append(steps, fmt.Sprintf("current location: %s", exe))
		}
		opts = append(opts, pathInstallOption{
			title: "copy the binary into a directory that is on your $PATH.",
			steps: steps,
		})
	}
	return opts
}

// platformLabel names the OS in the guide header.
func platformLabel(goos string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return goos
	}
}

// pathInstallGuideLines renders the guide. The header and the option letters
// both derive from how many options there actually are, so they cannot disagree:
// one option is stated plainly, several are lettered a), b), ... to choose from.
func pathInstallGuideLines(goos string, hasGo bool, exe string) []string {
	opts := pathInstallOptions(goos, hasGo, exe)
	lines := []string{}
	if len(opts) == 1 {
		lines = append(lines, fmt.Sprintf("      %s:", platformLabel(goos)))
	} else {
		lines = append(lines, fmt.Sprintf("      %s — choose one:", platformLabel(goos)))
	}
	for i, o := range opts {
		if i > 0 {
			lines = append(lines, "")
		}
		if len(opts) == 1 {
			lines = append(lines, fmt.Sprintf("        %s", o.title))
		} else {
			lines = append(lines, fmt.Sprintf("        %c) %s", 'a'+i, o.title))
		}
		for _, st := range o.steps {
			lines = append(lines, fmt.Sprintf("           %s", st))
		}
	}
	return lines
}

// printPathInstallGuide prints platform-specific installation guidance when cctrace is not in $PATH.
func printPathInstallGuide() {
	fmt.Println("  [!] 'cctrace' is not found in $PATH.")
	fmt.Println("      Session sync hooks require cctrace to be globally accessible.")
	fmt.Println()

	// Detect current binary location
	exe, _ := os.Executable()
	if exe != "" {
		resolved, err := filepath.EvalSymlinks(exe)
		if err == nil {
			exe = resolved
		}
	}

	hasGo := false
	if _, err := exec.LookPath("go"); err == nil {
		hasGo = true
	}

	for _, l := range pathInstallGuideLines(runtime.GOOS, hasGo, exe) {
		fmt.Println(l)
	}
	fmt.Println()
}
