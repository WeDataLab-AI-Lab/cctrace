package dependencyaudit

import "testing"

func TestStripInjectedRemovesBlocksAndKeepsHumanText(t *testing.T) {
	t.Parallel()
	tags := []string{
		"system-reminder", "bash-input", "bash-stdout", "bash-stderr",
		"command-name", "command-message", "command-args",
		"local-command-stdout", "local-command-stderr", "local-command-caveat",
		"environment_context", "user_instructions",
		"subagent_notification", "cross-session-message", "turn_aborted",
		"skill", "send_user_message", "task-notification",
	}
	for _, tag := range tags {
		block := "<" + tag + ` from="peer">machine words</` + tag + ">"
		for _, tc := range []struct{ text, want string }{
			{block + " fix the parser", "fix the parser"},
			{"fix the parser " + block, "fix the parser"},
			{"fix the " + block + "parser", "fix the parser"},
			{"<" + tag + ">cut off machine words", ""},
			{block + " <" + tag + ">cut off machine words", ""},
			{"fix the parser <" + tag + "> is the tag to grep for", "fix the parser <" + tag + "> is the tag to grep for"},
			{block, ""},
		} {
			got, ok := StripInjected(tc.text)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("%s: StripInjected(%q) = %q %v, want %q", tag, tc.text, got, ok, tc.want)
			}
		}
	}
	for _, text := range []string{
		"Another Claude session sent a message: please review",
		"# AGENTS.md instructions for /work/alpha\n\nrules",
		"<system-reminder>x</system-reminder>\nAnother Claude session sent a message: hi",
	} {
		if got, ok := StripInjected(text); ok || got != "" {
			t.Fatalf("machine message kept: %q -> %q", text, got)
		}
	}
	if got, ok := StripInjected("<skills> are a word here"); !ok || got != "<skills> are a word here" {
		t.Fatalf("a different tag was removed: %q", got)
	}
	if got, ok := StripInjected("quote: Another Claude session sent a message: is a phrase"); !ok || got == "" {
		t.Fatalf("a phrase inside human text dropped the message: %q", got)
	}
}
