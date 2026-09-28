package insights

import "testing"

// Claude Code stores a lot of machinery as role=user records: hook output, slash
// command scaffolding, another agent's message, the marker left when somebody
// presses escape. PromptFromRaw reads their text correctly -- they really are text
// on a user turn -- so the classifier saw them and answered "unknown", which is how
// a category that does not exist became the biggest slice of the chart.
//
// Measured on 61,131 typed turns from real logs, these are 50.7% of them (#429).
func TestMachineWrittenTurnsAreNotTaskInput(t *testing.T) {
	for _, prompt := range []string{
		"[Request interrupted by user]",
		"[Request interrupted by user for tool use]",
		"<system-reminder>\nPreToolUse:Read hook additional context: ...",
		"<local-command-caveat>Caveat: The messages below were generated ...",
		"<command-name>/model</command-name>",
		"<bash-input>ls -la</bash-input>",
		"<teammate-message teammate_id=\"team-lead\">",
		"Stop hook feedback:\n[bash .claude/hooks/guard.sh]",
		"Another Claude session sent a message:\n<teammate-message ...",
		"<task-notification>\n<task-id>b1hh8clff</task-id>",
		"[SYSTEM NOTIFICATION - NOT USER INPUT]",
		"[Image: source: /var/folders/hw/xyz.png]",
		"Tool result (Bash, id=toolu_01Lfkrwy):\n\noutput",
		"Tool loaded.",
		"/compact",
		"/oh-my-claudecode:cancel",
	} {
		if got := ClassifyPrompt(prompt); got != NonTask {
			t.Errorf("ClassifyPrompt(%.40q) = %q, want %q", prompt, got, NonTask)
		}
	}
}

// Acknowledgements and approvals carry no task description: the work was named in
// the turn before. Labelling them would put "do both" in a chart that answers
// "what kind of work happened".
func TestAcknowledgementsAreNotTaskInput(t *testing.T) {
	for _, prompt := range []string{"ㅇㅇ", "계속", "ok", "1", "y", ".", "done", "진행해", "둘 다 해줘"} {
		if got := ClassifyPrompt(prompt); got != NonTask {
			t.Errorf("ClassifyPrompt(%q) = %q, want %q", prompt, got, NonTask)
		}
	}
}

// The exclusions have to stay narrow, or they swallow the work they exist to
// reveal. A quoted marker inside a real request is still a real request, and a
// slash command with arguments carries one.
func TestRealRequestsSurviveTheExclusions(t *testing.T) {
	for _, tc := range []struct{ prompt, want string }{
		{"why does [Request interrupted by user] appear in the logs?", "question"},
		{"/loop 5m 진행상황 보고", "status"},
		{"continue from the readme and add the missing section", "documentation"},
		{"테스트 추가해줘", "testing"},
	} {
		if got := ClassifyPrompt(tc.prompt); got != tc.want {
			t.Errorf("ClassifyPrompt(%.40q) = %q, want %q", tc.prompt, got, tc.want)
		}
	}
}
