package dependencyaudit

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/openinsights"
)

// secretShapes are built at run time so that no credential-shaped literal sits
// in the source tree (the public mirror scans for these shapes).
func secretShapes() []string {
	r := strings.Repeat
	return []string{
		"cc" + "t_" + r("A1", 12),
		"sk-" + "ant-" + r("b2", 12),
		"sk" + "-" + r("C3", 12),
		"gh" + "p_" + r("D4", 18),
		"github" + "_pat_" + r("E5", 15),
		"gl" + "pat-" + r("F6", 12),
		"AK" + "IA" + r("G", 16),
		"xo" + "xb-" + r("7h", 8),
		"xo" + "xp-" + r("8i", 8),
		"ey" + "J" + r("x", 12) + ".ey" + "J" + r("y", 12) + "." + r("z", 16),
		"-----BEGIN RSA " + "PRIVATE KEY-----\n" + r("k", 40) + "\n-----END RSA " + "PRIVATE KEY-----",
	}
}

func TestMaskRemovesEverySecretShape(t *testing.T) {
	t.Parallel()
	for _, secret := range secretShapes() {
		masked := Mask("before " + secret + " after")
		if strings.Contains(masked, secret) || !strings.Contains(masked, "[REDACTED]") || !strings.HasPrefix(masked, "before ") || !strings.HasSuffix(masked, " after") {
			t.Fatalf("secret survived or text lost: %q", masked)
		}
	}
	if got := Mask("sk-short and task-notification stay"); got != "sk-short and task-notification stay" {
		t.Fatalf("ordinary text masked: %q", got)
	}
}

func TestExcerptMasksBeforeCutting(t *testing.T) {
	t.Parallel()
	secret := secretShapes()[3]
	long := strings.Repeat("가", 150) + " " + secret
	got := excerpt(long)
	if strings.Contains(got, secret[:12]) || len([]rune(got)) > 160 {
		t.Fatalf("excerpt %q", got)
	}
	if excerpt("  a\n\n b\tc  ") != "a b c" {
		t.Fatalf("whitespace not collapsed: %q", excerpt("  a\n\n b\tc  "))
	}
}

// Each credential form is masked and what names it is kept: the URL scheme
// and host, "Bearer", the key of a key=value pair. A key-shaped run inside an
// ordinary word is not a key.
func TestMaskCoversEachCredentialForm(t *testing.T) {
	t.Parallel()
	r := strings.Repeat
	for _, tc := range []struct{ in, want string }{
		{"token " + "gh" + "o_" + r("Ab", 12) + " end", "token [REDACTED] end"},
		{"token " + "gh" + "s_" + r("Cd", 12) + " end", "token [REDACTED] end"},
		{"token " + "gh" + "u_" + r("Ef", 12) + " end", "token [REDACTED] end"},
		{"token " + "gh" + "r_" + r("Gh", 12) + " end", "token [REDACTED] end"},
		{"clone https://" + "deploy" + ":" + "hunter2pass" + "@example.test/repo.git", "clone https://[REDACTED]@example.test/repo.git"},
		{"psql postgres://" + "app" + ":" + "dbpassword9" + "@127.0.0.1:1/db", "psql postgres://[REDACTED]@127.0.0.1:1/db"},
		{"Authorization: Bearer " + r("tok", 5), "Authorization: Bearer [REDACTED]"},
		{"set password=" + "s3cr3tvalue", "set password=[REDACTED]"},
		{"PASSWD: " + "letmein42", "PASSWD: [REDACTED]"},
		{"api_key=" + "abcdef123456", "api_key=[REDACTED]"},
		{"api-key: " + "abcdef123456", "api-key: [REDACTED]"},
		{"TOKEN=" + "opaque-value", "TOKEN=[REDACTED]"},
		{"secret: " + "plainwords", "secret: [REDACTED]"},
		{"mcp__" + secretShapes()[3], "mcp__[REDACTED]"},
		{"access_token=" + "opaque-value", "access_token=[REDACTED]"},
	} {
		got := Mask(tc.in)
		if got != tc.want {
			t.Errorf("Mask(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, text := range []string{"risk-assessment-document-version-2", "the task-notification-handler-module", "a bearer of news"} {
		if got := Mask(text); got != text {
			t.Errorf("Mask(%q) = %q, want it unchanged", text, got)
		}
	}
}

func TestCommandKeyDropsASecondWordThatCarriesCredentials(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"psql postgres://" + "app" + ":" + "pw" + "@127.0.0.1:1/db": "psql",
		"ssh deploy@127.0.0.1":              "ssh",
		"env PASSWORD=" + "x go test ./...": "go test",
		"git push origin main":              "git push",
		"cd /work && FOO=1 go test ./...":   "go test",
	} {
		if got := commandKey(in); got != want {
			t.Errorf("commandKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every string the output carries is masked or checked against the values it
// can take, map keys included: identifiers and names come from files anyone
// can write.
func TestEveryPrintedStringIsMaskedOrKnown(t *testing.T) {
	t.Parallel()
	secret := secretShapes()[3]
	home := filepath.Join(t.TempDir(), "claude")
	s := newClaude("sess-"+secret, time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC))
	s.typed("run it", "typed")
	s.tool("tk1", "mcp__"+secret, map[string]any{})
	s.result("tk1", false, permission("accept", "user_"+secret))
	s.tool("tk2", "Bash", map[string]any{"command": "ls"})
	s.result("tk2", true, map[string]any{"toolDenialKind": "kind-" + secret})
	s.typed("yes", "typed")
	writeLines(t, filepath.Join(home, "projects", "-work-alpha", "f-"+secret+".jsonl"), s.lines, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	res, raw := runFixture(t, Options{Agent: "claude", ClaudeDirs: []string{home}})
	for _, v := range stringValues(t, raw) {
		if strings.Contains(v, secret) {
			t.Fatalf("a printed string holds the secret: %q", v)
		}
	}
	if res.Signals.Permission.BySource["other"] != 1 {
		t.Fatalf("an unknown permission source was not folded into other: %v", res.Signals.Permission.BySource)
	}
	p := ServerPractices(&openinsights.Practices{Rows: []openinsights.PracticeRow{{Kind: "kind-" + secret, Name: "n"}}, Caveats: []string{"caveat " + secret}})
	if p.Rows[0].Kind != "other" || strings.Contains(p.Caveats[0], secret) {
		t.Fatalf("practices = %+v", p)
	}
}
