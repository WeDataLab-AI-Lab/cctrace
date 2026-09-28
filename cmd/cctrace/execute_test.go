package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// A failing command printed its error twice: cobra wrote "Error: ..." and main
// wrote the same error again.
func TestExecutePrintsAFailureOnce(t *testing.T) {
	root := &cobra.Command{Use: "cctrace"}
	root.AddCommand(&cobra.Command{
		Use:          "boom",
		SilenceUsage: true,
		RunE:         func(*cobra.Command, []string) error { return errors.New("it broke") },
	})
	root.SetArgs([]string{"boom"})

	var stderr strings.Builder
	if code := execute(root, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if got := strings.Count(stderr.String(), "it broke"); got != 1 {
		t.Fatalf("error printed %d times:\n%s", got, stderr.String())
	}
	if !strings.HasPrefix(stderr.String(), "Error: ") {
		t.Fatalf("lost the Error: prefix:\n%s", stderr.String())
	}
}
