package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"cctrace/internal/store"
)

type sessionFactsResetRunner func(context.Context, string, io.Writer) error

func handleSessionFactsResetCommand(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	out io.Writer,
	run sessionFactsResetRunner,
) (bool, error) {
	commandIndex := -1
	for i := 1; i < len(args); i++ {
		if args[i-1] == "repair" && args[i] == "session-facts-reset" {
			commandIndex = i - 1
			break
		}
	}
	if commandIndex < 0 {
		for _, arg := range args {
			if arg == "session-facts-reset" {
				return true, fmt.Errorf("repair session-facts-reset must be the first command")
			}
		}
		return false, nil
	}
	if commandIndex != 0 {
		return true, fmt.Errorf("repair session-facts-reset must be the first command")
	}
	if len(args) != 2 {
		return true, fmt.Errorf("usage: cctraced repair session-facts-reset")
	}
	dsn := strings.TrimSpace(getenv("DATABASE_URL"))
	if dsn == "" {
		return true, fmt.Errorf("DATABASE_URL is required for session facts reset")
	}
	return true, run(ctx, dsn, out)
}

func runSessionFactsReset(ctx context.Context, dsn string, out io.Writer) error {
	pg, err := store.NewPgStore(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect session facts reset database: connection failed")
	}
	defer pg.Close() //nolint:errcheck
	report, err := pg.ResetSessionFacts(ctx)
	if err != nil {
		return err
	}
	payload := struct {
		BinaryVersion string                        `json:"binary_version"`
		Report        store.SessionFactsResetReport `json:"report"`
	}{BinaryVersion: version, Report: report}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}
