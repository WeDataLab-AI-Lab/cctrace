package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"cctrace/internal/store"
)

type projectIdentityRepairRunner func(context.Context, string, string, io.Writer) error

const projectIdentityRepairTimeout = 2 * time.Minute

// handleProjectIdentityRepairCommand recognizes the operator entrypoint with an
// explicit mode: --dry-run classifies without writes, --apply writes the
// recoverable proposals. It runs before dashboard validation, authentication,
// migrations, listeners, and managed server repairs.
func handleProjectIdentityRepairCommand(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	out io.Writer,
	runDryRun projectIdentityRepairRunner,
	runApply projectIdentityRepairRunner,
) (bool, error) {
	commandIndex := -1
	for index, arg := range args {
		if arg == "project-identity-repair" {
			commandIndex = index
			break
		}
	}
	if commandIndex < 0 {
		return false, nil
	}
	if commandIndex != 0 {
		return true, fmt.Errorf("project-identity-repair must be the first argument")
	}
	runners := map[string]projectIdentityRepairRunner{"--dry-run": runDryRun, "--apply": runApply}
	var run projectIdentityRepairRunner
	if len(args) == 2 {
		run = runners[args[1]]
	}
	if run == nil {
		return true, fmt.Errorf("usage: cctraced project-identity-repair --dry-run|--apply")
	}
	dsn := strings.TrimSpace(getenv("DATABASE_URL"))
	if dsn == "" {
		return true, fmt.Errorf("DATABASE_URL is required for project identity repair %s", args[1])
	}
	runCtx, cancel := context.WithTimeout(ctx, projectIdentityRepairTimeout)
	defer cancel()
	return true, run(runCtx, dsn, version, out)
}

func runProjectIdentityRepairDryRun(ctx context.Context, dsn, binaryVersion string, out io.Writer) error {
	pg, err := store.NewPgStore(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect project identity repair database: connection failed")
	}
	defer pg.Close() //nolint:errcheck

	report, err := pg.DryRunProjectIdentityRepair(ctx)
	if err != nil {
		return err
	}
	return writeProjectIdentityRepairReport(out, binaryVersion, report)
}

func runProjectIdentityRepairApply(ctx context.Context, dsn, binaryVersion string, out io.Writer) error {
	pg, err := store.NewPgStore(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect project identity repair database: connection failed")
	}
	defer pg.Close() //nolint:errcheck

	report, err := pg.ApplyProjectIdentityRepair(ctx)
	if err != nil {
		return err
	}
	return writeProjectIdentityRepairReport(out, binaryVersion, report)
}

func writeProjectIdentityRepairReport(out io.Writer, binaryVersion string, report *store.ProjectIdentityRepairReport) error {
	payload := struct {
		BinaryVersion string                             `json:"binary_version"`
		Report        *store.ProjectIdentityRepairReport `json:"report"`
	}{
		BinaryVersion: binaryVersion,
		Report:        report,
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return fmt.Errorf("encode project identity repair report: %w", err)
	}
	return nil
}
