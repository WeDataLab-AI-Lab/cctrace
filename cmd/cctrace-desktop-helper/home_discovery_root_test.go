package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixtureHomeDiscoveryRootIsExplicitAndIsolated(t *testing.T) {
	t.Parallel()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	homes := filepath.Join(root, "homes")
	fixture := filepath.Join(root, "fixture")
	for _, dir := range []string{homes, fixture, filepath.Join(root, "support"), filepath.Join(root, "registry")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"--app-support", filepath.Join(root, "s1"), "--home-discovery-root", homes},
		{"--app-support", filepath.Join(root, "s2"), "--provider-mode", "--home-discovery-root", homes},
		{"--app-support", filepath.Join(root, "s3"), "--fixture-root", fixture, "--home-discovery-root", homes},
	} {
		var out, diagnostic bytes.Buffer
		if code := run(args, strings.NewReader(""), &out, &diagnostic); code != 2 {
			t.Fatalf("unsafe flag combination accepted: %v", args)
		}
	}
	var out, diagnostic bytes.Buffer
	if code := run([]string{"--app-support", filepath.Join(root, "support"), "--fixture-root", fixture, "--profile-root", filepath.Join(root, "registry"), "--home-discovery-root", homes}, strings.NewReader(""), &out, &diagnostic); code != 0 {
		t.Fatalf("isolated fixture rejected: %d %s", code, diagnostic.String())
	}
}
