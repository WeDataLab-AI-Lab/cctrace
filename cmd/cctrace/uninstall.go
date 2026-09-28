package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"cctrace/internal/profile"

	"github.com/spf13/cobra"
)

func uninstallCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove all cctrace configuration and the binary itself",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUninstall(force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	return cmd
}

func runUninstall(force bool) error {
	if !force {
		reader := bufio.NewReader(os.Stdin)
		fmt.Println()
		fmt.Println("  This will:")
		fmt.Println("    - Remove all cctrace profiles (default + named)")
		fmt.Println("    - Remove OTEL vars + sync hooks from all settings.json")
		fmt.Println("    - Delete the cctrace binary from PATH")
		fmt.Println()
		fmt.Print("  Proceed? [y/N]: ")
		line, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(line)) != "y" {
			fmt.Println("  Cancelled.")
			return nil
		}
	}

	fmt.Println()

	// 1. Reset all profiles (default + named)
	runReset(true, true, "")

	// 2. Remove ~/.cctrace directory
	cctraceDir := profile.DefaultDir()
	if _, err := os.Stat(cctraceDir); err == nil {
		if err := os.RemoveAll(cctraceDir); err != nil {
			fmt.Printf("  [!] Failed to remove %s: %v\n", cctraceDir, err)
		} else {
			fmt.Printf("  [OK] Removed %s\n", cctraceDir)
		}
	}

	// 3. Remove all cctrace binaries from PATH
	removed := 0
	for _, binPath := range findAllInstalledBinaries() {
		if removeBinary(binPath) {
			fmt.Printf("  [OK] Removed %s\n", binPath)
			removed++
		} else {
			fmt.Printf("  [!] Could not remove %s — remove manually\n", binPath)
		}
	}
	if removed == 0 {
		fmt.Printf("  [--] Binary not found in PATH (already removed?)\n")
	}

	fmt.Println()
	fmt.Println("  cctrace has been uninstalled.")
	fmt.Println()
	return nil
}

// findAllInstalledBinaries finds all cctrace binaries in PATH directories.
func findAllInstalledBinaries() []string {
	name := "cctrace"
	if runtime.GOOS == "windows" {
		name = "cctrace.exe"
	}

	seen := make(map[string]bool)
	var results []string

	// Also check well-known locations beyond PATH
	extraDirs := []string{"/usr/local/bin"}
	home, _ := os.UserHomeDir()
	if home != "" {
		extraDirs = append(extraDirs, filepath.Join(home, "go", "bin"))
	}

	pathDirs := filepath.SplitList(os.Getenv("PATH"))
	allDirs := append(pathDirs, extraDirs...)

	for _, dir := range allDirs {
		p := filepath.Join(dir, name)
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		if _, err := os.Stat(resolved); err != nil {
			continue
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		results = append(results, resolved)
	}
	return results
}

func removeBinary(path string) bool {
	// Try direct remove
	if err := os.Remove(path); err == nil {
		return true
	}

	if runtime.GOOS == "windows" {
		// Windows: can't delete running binary — rename + schedule background delete
		tmp := path + ".uninstall.tmp"
		if err := os.Rename(path, tmp); err != nil {
			fmt.Printf("  [!] Could not rename %s: %v\n", path, err)
			return false
		}
		// Spawn detached cmd that waits 2 seconds then deletes
		exec.Command("cmd", "/c", "ping", "localhost", "-n", "3", ">nul", "&", "del", "/f", tmp).Start()
		return true
	}

	// Unix: try with sudo
	cmd := exec.Command("sudo", "rm", path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err == nil {
		return true
	}

	return false
}
