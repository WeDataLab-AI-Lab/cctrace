package sessionview

import (
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"cctrace/internal/store"
)

// The regexes the web viewer used. They define the semantics the marker scanner
// must keep; they are too slow to ship (NFA over the whole replayed transcript).
var (
	oracleReplayRE = regexp.MustCompile(`^[\s\S]*?The above is the conversation history so far[\s\S]*?continue the transcript\.[ \t]*\n?`)
	oracleHeadRE   = regexp.MustCompile(`Tool result \(([^,)]+), id=(toolu_[A-Za-z0-9]+)\):[ \t]*\n?`)
	oracleTailRE   = regexp.MustCompile(`\n?[ \t]*Tool result \([^,)]+, id=toolu_[A-Za-z0-9]+\):[\s\S]*$`)
)

func oracleBody(text string) string {
	return strings.TrimSpace(oracleTailRE.ReplaceAllString(oracleReplayRE.ReplaceAllString(text, ""), ""))
}

func oracleResults(text string) []ToolResult {
	heads := oracleHeadRE.FindAllStringSubmatchIndex(text, -1)
	var out []ToolResult
	for i, h := range heads {
		end := len(text)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		out = append(out, ToolResult{ID: text[h[4]:h[5]], Output: strings.TrimSpace(text[h[1]:end]), Inferred: true})
	}
	return out
}

func TestSdkScanMatchesRegexSemantics(t *testing.T) {
	cases := []string{
		"",
		"plain request",
		"The above is the conversation history so far\ncontinue the transcript.\nreq",
		"The above is the conversation history so far but never the other marker",
		"continue the transcript. before The above is the conversation history so far",
		"a The above is the conversation history so far b continue the transcript. \t \n\nreq",
		"x The above is the conversation history so far continue the transcript. y The above is the conversation history so far continue the transcript. z",
		"req\n \tTool result (Bash, id=toolu_01):\nout",
		"req\n\n  Tool result (Bash, id=toolu_01): out\nTool result (Read, id=toolu_02)  :bad\nTool result (Read, id=toolu_03):\t\n\nok",
		"Tool result (, id=toolu_01): empty name is invalid",
		"Tool result (A)B, id=toolu_01): paren in name is invalid",
		"Tool result (Tool result (Bash, id=toolu_01): nested",
		"Tool result (Bash, id=toolu_): no id chars",
		"Tool result (Bash, id=toolu_ab-c): dash stops id",
		"Tool result (Multi\nLine, id=toolu_9Z):x",
		"Tool result (Bash, id=toolu_01):Tool result (Bash, id=toolu_02):",
		"Tool result (Bash, id=toolu_01)",
		"Tool result (Bash",
		"Tool result (Bash, id=toolu_01):\r\nwindows",
	}
	frags := []string{
		"The above is the conversation history so far", "continue the transcript.", "Tool result (", "Bash", "A,B", ")",
		", id=toolu_", "01", "x-", "):", " ", "\t", "\n", "\n\n", "req", "(", ",", "<skill name=\"s\">",
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 5000; i++ {
		var b strings.Builder
		for n := rng.Intn(14); n >= 0; n-- {
			b.WriteString(frags[rng.Intn(len(frags))])
		}
		cases = append(cases, b.String())
	}
	for _, text := range cases {
		if got, want := sdkBody(text), oracleBody(text); got != want {
			t.Fatalf("sdkBody(%q) = %q, want %q", text, got, want)
		}
		if got, want := sdkToolResults(text), oracleResults(text); !reflect.DeepEqual(got, want) {
			t.Fatalf("sdkToolResults(%q) = %#v, want %#v", text, got, want)
		}
	}
}

// ~1MB replayed transcript, the size that took ~42ms per record with the regexes.
func sdkReplayRecord() *store.SessionRecord {
	var b strings.Builder
	b.WriteString(`{"message":{"content":"<conversation_history>`)
	for b.Len() < 1<<20 {
		b.WriteString(`Human: please look at the file and tell me what it does\nAssistant: it parses the log\n`)
	}
	b.WriteString(`</conversation_history>\nThe above is the conversation history so far. Please continue the transcript.\nRun the tests\nTool result (Bash, id=toolu_01A):\n\nok"}}`)
	return &store.SessionRecord{RecordType: "user", PromptSource: "sdk", Ts: time.Unix(0, 0), Raw: raw(b.String())}
}

func BenchmarkNormalizeSdkReplay1MB(b *testing.B) {
	rec := sdkReplayRecord()
	b.SetBytes(int64(len(rec.Raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Normalize(rec)
	}
}

func BenchmarkSdkScan1MB(b *testing.B) {
	text, _ := contentOf(sdkReplayRecord().Raw[len(`{"message":{"content":`):len(sdkReplayRecord().Raw)-2], "")
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sdkBody(text)
		sdkToolResults(text)
	}
}

func BenchmarkOracleRegex1MB(b *testing.B) {
	text, _ := contentOf(sdkReplayRecord().Raw[len(`{"message":{"content":`):len(sdkReplayRecord().Raw)-2], "")
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		oracleBody(text)
		oracleResults(text)
	}
}
