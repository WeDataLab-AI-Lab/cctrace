package codexconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestPlanServerPolicyConfig(t *testing.T) {
	t.Parallel()
	oldEndpoint, newEndpoint := "http://trace.example.test:4317", "https://trace.example.test:5317"
	prefix, suffix := "model = \"custom\"\n\n", "\n# my tools\n[mcp_servers.custom]\ncommand = \"keep\"\n"
	original := prefix + buildOtelBlock(oldEndpoint, "token", "account", "") + suffix
	out, err := PlanServerPolicyConfig([]byte(original), oldEndpoint, newEndpoint, "token", "", "root.pem")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != prefix+buildOtelBlock(newEndpoint, "token", "account", "root.pem")+suffix {
		t.Fatalf("unexpected output: %s", out)
	}
	rotated, err := PlanServerPolicyConfig(out, newEndpoint, newEndpoint, "token", "root.pem", "rotated.pem")
	if err != nil || !strings.Contains(string(rotated), `ca-certificate = "rotated.pem"`) {
		t.Fatalf("rotation: %s, %v", rotated, err)
	}
	for _, bad := range []string{
		strings.Replace(original, "Bearer token", "Bearer foreign", 1),
		strings.Replace(original, "trace.example.test", "foreign.example.test", 1),
		prefix + buildOtelBlock(oldEndpoint, "token", "account", "user.pem") + suffix,
		strings.Replace(original, "[otel]", "[otel]\nlog_user_prompt = true", 1),
		"model = \"custom\"\n",
	} {
		if _, err := PlanServerPolicyConfig([]byte(bad), oldEndpoint, newEndpoint, "token", "", "root.pem"); err == nil {
			t.Fatalf("accepted unmanaged config: %s", bad)
		}
	}
}

func TestPlanSharedServerPolicyConfig(t *testing.T) {
	t.Parallel()
	oldBlock := PolicyBlock{Endpoint: "http://trace.example.test:4317", Token: "token"}
	newBlock := PolicyBlock{Endpoint: "https://trace.example.test:5317", Token: "token", CA: "root.pem"}
	siblingOld := PolicyBlock{Endpoint: "http://trace.example.test:4317", Token: "sibling"}
	siblingNew := PolicyBlock{Endpoint: "https://trace.example.test:5317", Token: "sibling", CA: "sibling.pem"}
	prefix, suffix := "model = \"custom\"\n\n", "\n# my tools\n[mcp_servers.custom]\ncommand = \"keep\"\n"
	config := func(b PolicyBlock) string {
		return prefix + buildOtelBlock(b.Endpoint, b.Token, "account", b.CA) + suffix
	}
	digest := func(b PolicyBlock) string {
		sum := sha256.Sum256([]byte(strings.TrimSpace(buildOtelBlock(b.Endpoint, b.Token, "account", b.CA))))
		return hex.EncodeToString(sum[:])
	}
	for _, tc := range []struct {
		name    string
		content string
		want    SharedBlock
	}{
		{"old block", config(oldBlock), SharedBlockOld},
		{"new block", config(newBlock), SharedBlockNew},
		{"sibling old block", config(siblingOld), SharedBlockSibling},
		{"sibling new block", config(siblingNew), SharedBlockSibling},
		{"extra exporter option", strings.Replace(config(oldBlock), "[otel]", "[otel]\nlog_user_prompt = true", 1), SharedBlockOther},
		{"unknown token", config(PolicyBlock{Endpoint: oldBlock.Endpoint, Token: "foreign"}), SharedBlockOther},
		{"unknown server", config(PolicyBlock{Endpoint: "http://other.example.test:4317", Token: "token"}), SharedBlockOther},
		{"no otel section", prefix + suffix, SharedBlockOther},
	} {
		plan, err := PlanSharedServerPolicyConfig([]byte(tc.content), oldBlock, newBlock, []PolicyBlock{siblingOld, siblingNew})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if plan.Holds != tc.want {
			t.Errorf("%s: holds %d, want %d", tc.name, plan.Holds, tc.want)
		}
		if (plan.Content != nil) != (tc.want == SharedBlockOld) {
			t.Errorf("%s: content planned %v", tc.name, plan.Content != nil)
		}
		if tc.want == SharedBlockOld && string(plan.Content) != config(newBlock) {
			t.Errorf("%s: planned\n%s", tc.name, plan.Content)
		}
		// Rebuilt from the values given and the config's account, whatever it holds.
		if tc.want != SharedBlockOther && (plan.OldSHA256 != digest(oldBlock) || plan.NewSHA256 != digest(newBlock)) {
			t.Errorf("%s: digests %s %s", tc.name, plan.OldSHA256, plan.NewSHA256)
		}
	}
	if _, err := PlanSharedServerPolicyConfig([]byte("otel.exporter = \"\"\"\nx\n\"\"\"\n"), oldBlock, newBlock, nil); !errors.Is(err, ErrMultilineRootOtelKey) {
		t.Errorf("multi-line root otel key: %v", err)
	}
	if _, err := PlanSharedServerPolicyConfig([]byte(config(oldBlock)), oldBlock, siblingNew, nil); err == nil {
		t.Error("planned a move that changes the token")
	}
}

// The token is read from the config's text, so a caller that already holds it
// need not read the file again; a config with no otel block, or one that sends
// no token, has none.
func TestOtelBearerTokenOf(t *testing.T) {
	t.Parallel()
	block := buildOtelBlock("http://trace.example.test:4317", "token", "account", "root.pem")
	for _, tc := range []struct {
		name, content, want string
	}{
		{"generated block", "model = \"custom\"\n\n" + block, "token"},
		{"no token sent", buildOtelBlock("http://trace.example.test:4317", "", "", ""), ""},
		{"no otel block", "model = \"custom\"\n", ""},
		{"empty", "", ""},
	} {
		if got := OtelBearerTokenOf([]byte(tc.content)); got != tc.want {
			t.Errorf("%s: token %q, want %q", tc.name, got, tc.want)
		}
	}
}
