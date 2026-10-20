package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestSessionFactsResetCommand(t *testing.T) {
	var gotDSN string
	called := false
	run := func(_ context.Context, dsn string, _ io.Writer) error {
		called, gotDSN = true, dsn
		return nil
	}
	var out bytes.Buffer
	handled, err := handleSessionFactsResetCommand(context.Background(), []string{"repair", "session-facts-reset"},
		func(string) string { return " postgres://test " }, &out, run)
	if !handled || err != nil || !called || gotDSN != "postgres://test" {
		t.Fatalf("handled=%v called=%v dsn=%q err=%v", handled, called, gotDSN, err)
	}
}

func TestSessionFactsResetCommandRejectsInvalidInvocation(t *testing.T) {
	run := func(context.Context, string, io.Writer) error {
		return errors.New("must not run")
	}
	for _, args := range [][]string{
		{"--help", "repair", "session-facts-reset"},
		{"repair", "session-facts-reset", "--force"},
	} {
		handled, err := handleSessionFactsResetCommand(context.Background(), args,
			func(string) string { return "postgres://test" }, &bytes.Buffer{}, run)
		if !handled || err == nil {
			t.Fatalf("args=%q: handled=%v err=%v", args, handled, err)
		}
	}
	handled, err := handleSessionFactsResetCommand(context.Background(), []string{"repair", "session-facts-reset"},
		func(string) string { return "" }, &bytes.Buffer{}, run)
	if !handled || err == nil {
		t.Fatalf("missing DATABASE_URL: handled=%v err=%v", handled, err)
	}
}

func TestUsageTextDocumentsSessionFactsReset(t *testing.T) {
	help := usageText()
	for _, required := range []string{"cctraced repair session-facts-reset", "stop the server"} {
		if !bytes.Contains([]byte(help), []byte(required)) {
			t.Fatalf("help does not contain %q", required)
		}
	}
}
