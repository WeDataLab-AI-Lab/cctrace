package main

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// The environment only ever set the *default*: an admin's choice in Admin > AI
// overrides every one of these, and the switch an admin flips is stored in the
// database. Named without that qualifier, CCTRACE_AI_ENABLED read like the
// authority, and a stack started without it looked broken rather than
// undecided -- which is exactly how a demo came up with reports switched off.
// The _DEFAULT suffix says who wins.
func TestAIDefaultsAreReadFromTheDefaultNames(t *testing.T) {
	t.Setenv("CCTRACE_AI_ENABLED_DEFAULT", "true")
	t.Setenv("CCTRACE_AI_RUNTIME_DEFAULT", "claude-api")
	t.Setenv("CCTRACE_AI_MODEL_DEFAULT", "gpt-model")
	t.Setenv("CCTRACE_AI_REASONING_EFFORT_DEFAULT", "low")
	t.Setenv("CCTRACE_AI_OPENAI_MODEL_DEFAULT", "openai-model")
	t.Setenv("CCTRACE_AI_CLAUDE_MODEL_DEFAULT", "claude-model")
	t.Setenv("CCTRACE_AI_NVIDIA_MODEL_DEFAULT", "nvidia-model")
	t.Setenv("CCTRACE_AI_LITELLM_MODEL_DEFAULT", "litellm-model")
	t.Setenv("CCTRACE_AI_LITELLM_BASE_URL_DEFAULT", "https://proxy.example.test")

	env := aiEnvFromEnv(t.TempDir())
	if env.Enabled == nil || !*env.Enabled {
		t.Errorf("Enabled = %v, want true", env.Enabled)
	}
	for _, c := range []struct{ name, got, want string }{
		{"Runtime", env.Runtime, "claude-api"},
		{"Model", env.Model, "gpt-model"},
		{"ReasoningEffort", env.ReasoningEffort, "low"},
		{"OpenAIModel", env.OpenAIModel, "openai-model"},
		{"ClaudeModel", env.ClaudeModel, "claude-model"},
		{"NVIDIAModel", env.NVIDIAModel, "nvidia-model"},
		{"LiteLLMModel", env.LiteLLMModel, "litellm-model"},
		{"LiteLLMBaseURL", env.LiteLLMBaseURL, "https://proxy.example.test"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// A deployment that set the old names keeps working. Renaming a variable that
// lives in someone's .env is not worth a silent outage, so the old spelling is
// still read -- with a warning that names its replacement.
func TestOldAINamesStillWork(t *testing.T) {
	t.Setenv("CCTRACE_AI_ENABLED", "true")
	t.Setenv("CCTRACE_AI_RUNTIME", "openai-api")
	t.Setenv("CCTRACE_AI_MODEL", "old-model")
	t.Setenv("CCTRACE_AI_LITELLM_BASE_URL", "https://old.example.test")

	env := aiEnvFromEnv(t.TempDir())
	if env.Enabled == nil || !*env.Enabled {
		t.Errorf("Enabled = %v, want true from the old name", env.Enabled)
	}
	if env.Runtime != "openai-api" || env.Model != "old-model" || env.LiteLLMBaseURL != "https://old.example.test" {
		t.Errorf("old names ignored: runtime=%q model=%q baseURL=%q", env.Runtime, env.Model, env.LiteLLMBaseURL)
	}
}

// With both spellings present the new one wins: an operator midway through the
// rename should not have to guess which of the two the server obeys.
func TestNewAINameWinsOverTheOldOne(t *testing.T) {
	t.Setenv("CCTRACE_AI_RUNTIME", "openai-api")
	t.Setenv("CCTRACE_AI_RUNTIME_DEFAULT", "claude-api")
	t.Setenv("CCTRACE_AI_ENABLED", "false")
	t.Setenv("CCTRACE_AI_ENABLED_DEFAULT", "true")

	env := aiEnvFromEnv(t.TempDir())
	if env.Runtime != "claude-api" {
		t.Errorf("Runtime = %q, want the _DEFAULT value", env.Runtime)
	}
	if env.Enabled == nil || !*env.Enabled {
		t.Errorf("Enabled = %v, want the _DEFAULT value true", env.Enabled)
	}
}

// The deprecation is only useful if an operator is told which name replaced
// theirs. A silent fallback would keep the old spelling alive forever: nothing
// in the logs would ever say to change it.
func TestOldAINameWarningNamesItsReplacement(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	t.Setenv("CCTRACE_AI_RUNTIME", "openai-api")
	if env := aiEnvFromEnv(t.TempDir()); env.Runtime != "openai-api" {
		t.Fatalf("Runtime = %q, want the old name's value", env.Runtime)
	}

	out := logged.String()
	if !strings.Contains(out, "CCTRACE_AI_RUNTIME is deprecated") {
		t.Errorf("no deprecation warning for the old name; log was %q", out)
	}
	if !strings.Contains(out, "CCTRACE_AI_RUNTIME_DEFAULT") {
		t.Errorf("the warning does not name its replacement; log was %q", out)
	}
}

// The current name must not warn: an operator who already renamed should see
// nothing.
func TestCurrentAINameDoesNotWarn(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	t.Setenv("CCTRACE_AI_RUNTIME_DEFAULT", "claude-api")
	_ = aiEnvFromEnv(t.TempDir())
	if strings.Contains(logged.String(), "deprecated") {
		t.Errorf("the current name warned: %q", logged.String())
	}
}
