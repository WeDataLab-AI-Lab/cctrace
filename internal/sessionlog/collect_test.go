package sessionlog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// recordKeys are the fields Record carried before CollectVersion. What a line
// keeps under the collection rules is everything else json.Marshal produces.
var recordKeys = []string{
	"type", "timestamp", "uuid", "parentUuid", "sessionId", "cwd", "version", "gitBranch",
	"isSidechain", "agentId", "forkedFrom", "isCompactSummary", "isMeta", "promptSource",
	"entrypoint", "attributionSkill", "message",
}

// collected returns what r adds to raw beyond recordKeys.
func collected(t *testing.T, r *Record) map[string]interface{} {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range recordKeys {
		delete(m, k)
	}
	return m
}

func decodeJSON(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad expectation %s: %v", s, err)
	}
	return m
}

func scanLines(t *testing.T, lines []string) []*Record {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	records, _, err := ScanFile(path, 0)
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if len(records) != len(lines) {
		t.Fatalf("scanned %d records from %d lines: a line failed to decode", len(records), len(lines))
	}
	return records
}

// Each line type keeps exactly the fields listed for it, in the shapes listed,
// and nothing else -- not the fields listed for other line types, not unknown
// fields, not the members of a kept object that are not listed. The line's own
// claims to cctraceCollect or imagePasteCount do not survive either: both are
// this client's to write.
func TestScanKeepsOnlyTheCollectedFieldsPerLineType(t *testing.T) {
	const head = `"timestamp":"2026-10-01T00:00:00Z","sessionId":"s1"`
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			name: "user",
			line: `{"type":"user","uuid":"u-1",` + head + `,"message":{"role":"user","content":"hello"},` +
				`"promptId":"p-1",` +
				`"origin":{"kind":"peer","from":"other","name":"n","body":"PEER BODY"},` +
				`"turnOrigin":"peer",` +
				`"turnPosition":{"promptIndex":2,"turnIndex":5,"extra":true},` +
				`"permissionMode":"plan","toolDenialKind":"user-rejected","interruptedMessageId":"msg-9",` +
				`"imagePasteIds":[3,4,5],` +
				`"toolUseResult":{"questions":[{"question":"Q?"}],"answers":{"Q?":"A"},"annotations":{"Q?":{"notes":"N"}},` +
				`"stdout":"STDOUT","status":"async_launched","isAsync":true,"agentId":"ag-1","agent_id":"ag-2","backgroundTaskId":"bg-1","interrupted":false},` +
				`"slug":"unknown-field","requestId":"r","subtype":"s","content":"c","error":"e","attachment":{"type":"model"},"durationMs":5,` +
				`"cctraceCollect":99,"imagePasteCount":42}`,
			want: `{"promptId":"p-1","origin":{"kind":"peer"},"turnOrigin":"peer","turnPosition":{"promptIndex":2,"turnIndex":5},` +
				`"permissionMode":"plan","toolDenialKind":"user-rejected","interruptedMessageId":"msg-9","imagePasteCount":3,` +
				`"toolUseResult":{"answers":{"Q?":"A"},"annotations":{"Q?":{"notes":"N"}},"status":"async_launched","isAsync":true,` +
				`"agentId":"ag-1","agent_id":"ag-2","backgroundTaskId":"bg-1","interrupted":false},"cctraceCollect":4}`,
		},
		{
			name: "user toolUseResult that is a string",
			line: `{"type":"user","uuid":"u-2",` + head + `,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]},"toolUseResult":"Error: boom"}`,
			want: `{"cctraceCollect":4}`,
		},
		{
			name: "user toolUseResult with no kept keys",
			line: `{"type":"user","uuid":"u-3",` + head + `,"message":{"role":"user","content":"x"},"toolUseResult":{"stdout":"x","stderr":""}}`,
			want: `{"cctraceCollect":4}`,
		},
		{
			// A changed shape costs the field, never the line, and the mark names
			// it: an absence the rules caused is not a 0. A null is absent, and
			// an array toolUseResult (MCP content) is a result that holds none of
			// the kept keys, so neither is named.
			name: "user fields of the wrong JSON type",
			line: `{"type":"user","uuid":"u-4",` + head + `,"message":{"role":"user","content":"x"},` +
				`"promptId":5,"origin":"human","turnOrigin":{"a":1},"turnPosition":[1,2],"permissionMode":null,"imagePasteIds":"x","toolUseResult":[{"answers":1}]}`,
			want: `{"cctraceCollect":4,"cctraceDropped":["promptId","origin","turnOrigin","turnPosition","imagePasteCount"]}`,
		},
		{
			name: "user toolUseResult of no result shape",
			line: `{"type":"user","uuid":"u-5",` + head + `,"message":{"role":"user","content":"x"},"toolUseResult":7,"toolDenialKind":true,"interruptedMessageId":[]}`,
			want: `{"cctraceCollect":4,"cctraceDropped":["toolDenialKind","interruptedMessageId","toolUseResult"]}`,
		},
		{
			name: "assistant fields of the wrong JSON type",
			line: `{"type":"assistant","uuid":"a-2",` + head + `,"message":{"role":"assistant"},"requestId":1,"isApiErrorMessage":"yes","error":null}`,
			want: `{"cctraceCollect":4,"cctraceDropped":["requestId","isApiErrorMessage"]}`,
		},
		{
			name: "system fields of the wrong JSON type",
			line: `{"type":"system","uuid":"y-4",` + head + `,"subtype":"away_summary","content":{"text":"recap"},"logicalParentUuid":1,` +
				`"durationMs":"5","messageCount":true,"pendingBackgroundAgentCount":"2","pendingWorkflowCount":[1],"compactMetadata":"auto"}`,
			want: `{"subtype":"away_summary","cctraceCollect":4,"cctraceDropped":["logicalParentUuid","durationMs","messageCount",` +
				`"pendingBackgroundAgentCount","pendingWorkflowCount","content","compactMetadata"]}`,
		},
		{
			name: "attachment that is not an object",
			line: `{"type":"attachment","uuid":"t-10",` + head + `,"attachment":"queued_command"}`,
			want: `{"cctraceCollect":4,"cctraceDropped":["attachment"]}`,
		},
		{
			name: "attachment members of the wrong JSON type",
			line: `{"type":"attachment","uuid":"t-11",` + head + `,"attachment":{"type":"queued_command","prompt":5,"commandMode":"prompt","origin":"peer"}}`,
			want: `{"attachment":{"type":"queued_command","commandMode":"prompt"},"cctraceCollect":4,"cctraceDropped":["attachment.prompt","attachment.origin"]}`,
		},
		{
			name: "attachment task_reminder content that is not a list",
			line: `{"type":"attachment","uuid":"t-12",` + head + `,"attachment":{"type":"task_reminder","content":{"id":"1"}}}`,
			want: `{"attachment":{"type":"task_reminder"},"cctraceCollect":4,"cctraceDropped":["attachment.content"]}`,
		},
		{
			name: "attachment plan_file_reference planContent that is not text",
			line: `{"type":"attachment","uuid":"t-13",` + head + `,"attachment":{"type":"plan_file_reference","planFilePath":"/p.md","planContent":["# Plan"]}}`,
			want: `{"attachment":{"type":"plan_file_reference","planFilePath":"/p.md"},"cctraceCollect":4,"cctraceDropped":["attachment.planContent"]}`,
		},
		{
			name: "attachment model identity that is not an object",
			line: `{"type":"attachment","uuid":"t-14",` + head + `,"attachment":{"type":"model","identity":"fixture-model"}}`,
			want: `{"attachment":{"type":"model"},"cctraceCollect":4,"cctraceDropped":["attachment.identity"]}`,
		},
		{
			name: "assistant",
			line: `{"type":"assistant","uuid":"a-1",` + head + `,"message":{"role":"assistant","model":"m"},` +
				`"requestId":"req_1","isApiErrorMessage":true,"error":"rate_limit","promptId":"p-1","subtype":"s"}`,
			want: `{"requestId":"req_1","isApiErrorMessage":true,"error":"rate_limit","cctraceCollect":4}`,
		},
		{
			name: "system turn_duration",
			line: `{"type":"system","uuid":"y-1",` + head + `,"subtype":"turn_duration","content":"not a recap","level":"info",` +
				`"durationMs":1234,"messageCount":7,"pendingBackgroundAgentCount":2,"pendingWorkflowCount":1,"promptId":"p"}`,
			want: `{"subtype":"turn_duration","content":"not a recap","durationMs":1234,"messageCount":7,"pendingBackgroundAgentCount":2,"pendingWorkflowCount":1,"cctraceCollect":4}`,
		},
		{
			name: "system compact_boundary",
			line: `{"type":"system","uuid":"y-2",` + head + `,"subtype":"compact_boundary","logicalParentUuid":"lp-1",` +
				`"compactMetadata":{"trigger":"auto","preTokens":100,"postTokens":10,"durationMs":5,"preservedSegment":{"headUuid":"h"}}}`,
			want: `{"subtype":"compact_boundary","logicalParentUuid":"lp-1","compactMetadata":{"trigger":"auto","preTokens":100,"postTokens":10},"cctraceCollect":4}`,
		},
		{
			name: "system away_summary",
			line: `{"type":"system","uuid":"y-3",` + head + `,"subtype":"away_summary","content":"recap text"}`,
			want: `{"subtype":"away_summary","content":"recap text","cctraceCollect":4}`,
		},
		{
			name: "attachment task_status",
			line: `{"type":"attachment","uuid":"t-1",` + head + `,"attachment":{"type":"task_status","taskId":"b1","taskType":"local_bash","status":"running","description":"D","outputFilePath":"/o"}}`,
			want: `{"attachment":{"type":"task_status","taskId":"b1","taskType":"local_bash","status":"running"},"cctraceCollect":4}`,
		},
		{
			name: "attachment task_reminder",
			line: `{"type":"attachment","uuid":"t-2",` + head + `,"attachment":{"type":"task_reminder","itemCount":2,` +
				`"content":[{"id":"1","subject":"S","status":"pending","description":"D","activeForm":"A"},{"other":1}]}}`,
			want: `{"attachment":{"type":"task_reminder","content":[{"id":"1","subject":"S","status":"pending"}]},"cctraceCollect":4}`,
		},
		{
			// An empty list says there are no tasks; dropping it would say nothing.
			name: "attachment task_reminder empty",
			line: `{"type":"attachment","uuid":"t-3",` + head + `,"attachment":{"type":"task_reminder","content":[],"itemCount":0}}`,
			want: `{"attachment":{"type":"task_reminder","content":[]},"cctraceCollect":4}`,
		},
		{
			name: "attachment queued_command",
			line: `{"type":"attachment","uuid":"t-4",` + head + `,"attachment":{"type":"queued_command","prompt":"typed while busy","commandMode":"prompt",` +
				`"origin":{"kind":"peer","body":"PEER BODY","from":"x"},"source_uuid":"su","imagePasteIds":[1],"usage":{}}}`,
			want: `{"attachment":{"type":"queued_command","prompt":"typed while busy","commandMode":"prompt","origin":{"kind":"peer"}},"cctraceCollect":4}`,
		},
		{
			name: "attachment plan_mode",
			line: `{"type":"attachment","uuid":"t-5",` + head + `,"attachment":{"type":"plan_mode","reminderType":"full","isSubAgent":false,"planFilePath":"/p.md","planExists":true}}`,
			want: `{"attachment":{"type":"plan_mode","planFilePath":"/p.md","planExists":true},"cctraceCollect":4}`,
		},
		{
			name: "attachment plan_mode_exit",
			line: `{"type":"attachment","uuid":"t-6",` + head + `,"attachment":{"type":"plan_mode_exit","planFilePath":"/p.md","planExists":true}}`,
			want: `{"attachment":{"type":"plan_mode_exit"},"cctraceCollect":4}`,
		},
		{
			name: "attachment plan_file_reference",
			line: `{"type":"attachment","uuid":"t-7",` + head + `,"attachment":{"type":"plan_file_reference","planFilePath":"/p.md","planContent":"# Plan","extra":1}}`,
			want: `{"attachment":{"type":"plan_file_reference","planFilePath":"/p.md","planContent":"# Plan"},"cctraceCollect":4}`,
		},
		{
			name: "attachment model",
			line: `{"type":"attachment","uuid":"t-8",` + head + `,"attachment":{"type":"model","text":"T","identity":{"modelId":"fixture-model[1m]","marketingName":"M","knowledgeCutoff":"K"}}}`,
			want: `{"attachment":{"type":"model","identity":{"modelId":"fixture-model[1m]"}},"cctraceCollect":4}`,
		},
		{
			name: "attachment of an unlisted type",
			line: `{"type":"attachment","uuid":"t-9",` + head + `,"attachment":{"type":"unknown_attachment","content":"hook output","hookName":"h"}}`,
			want: `{"cctraceCollect":4}`,
		},
		{
			// No uuid: keyed by a digest of the line. Keeps nothing, not even
			// permissionMode, which permission-mode lines carry under the same name.
			name: "permission-mode metadata line",
			line: `{"type":"permission-mode","permissionMode":"plan","sessionId":"s1"}`,
			want: `{}`,
		},
		{
			name: "queue-operation metadata line",
			line: `{"type":"queue-operation","operation":"enqueue",` + head + `,"content":"queued text"}`,
			want: `{}`,
		},
		{
			name: "user line without a uuid",
			line: `{"type":"user",` + head + `,"message":{"role":"user","content":"x"},"promptId":"p-1","toolUseResult":{"status":"completed"}}`,
			want: `{}`,
		},
		{
			name: "other line type with a uuid",
			line: `{"type":"progress","uuid":"g-1",` + head + `,"promptId":"p-1","content":"c","attachment":{"type":"model"}}`,
			want: `{}`,
		},
	}

	lines := make([]string, len(cases))
	for i, tc := range cases {
		lines[i] = tc.line
	}
	records := scanLines(t, lines)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collected(t, records[i])
			want := decodeJSON(t, tc.want)
			if !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.Marshal(got)
				t.Errorf("kept fields\n got  %s\n want %s", gotJSON, tc.want)
			}
		})
	}
}

// The free-text fields are capped, on a rune boundary so the cut never leaves
// invalid UTF-8 behind, and the head of the text is what remains.
func TestScanCapsFreeTextFields(t *testing.T) {
	const head = `"timestamp":"2026-10-01T00:00:00Z","sessionId":"s1"`
	recap := strings.Repeat("가", 2000) // 6,000 bytes, 3 per rune
	plan := strings.Repeat("p", 20000) // over 16 KiB
	errText := strings.Repeat("e", 3000)
	errObject := `{"type":"api_error","message":"` + strings.Repeat("m", 3000) + `"}`
	quote := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	records := scanLines(t, []string{
		`{"type":"system","uuid":"y-1",` + head + `,"subtype":"away_summary","content":` + quote(recap) + `}`,
		`{"type":"attachment","uuid":"t-1",` + head + `,"attachment":{"type":"plan_file_reference","planFilePath":"/p.md","planContent":` + quote(plan) + `}}`,
		`{"type":"assistant","uuid":"a-1",` + head + `,"message":{},"error":` + quote(errText) + `}`,
		`{"type":"assistant","uuid":"a-2",` + head + `,"message":{},"error":` + errObject + `}`,
	})

	var gotRecap string
	if err := json.Unmarshal(records[0].Content, &gotRecap); err != nil {
		t.Fatalf("recap: %v (%s)", err, records[0].Content)
	}
	// 4,096 is not a rune boundary in a run of 3-byte runes; 4,095 is.
	if len(gotRecap) != 4095 || !utf8.ValidString(gotRecap) || !strings.HasPrefix(recap, gotRecap) {
		t.Errorf("away_summary content: %d bytes, valid=%v, prefix=%v; want the first 4,095 bytes", len(gotRecap), utf8.ValidString(gotRecap), strings.HasPrefix(recap, gotRecap))
	}

	var attachment struct {
		PlanContent string `json:"planContent"`
	}
	if err := json.Unmarshal(records[1].Attachment, &attachment); err != nil {
		t.Fatalf("attachment: %v", err)
	}
	if attachment.PlanContent != plan[:16<<10] {
		t.Errorf("planContent: %d bytes, want the first %d", len(attachment.PlanContent), 16<<10)
	}

	var gotErr string
	if err := json.Unmarshal(records[2].Error, &gotErr); err != nil {
		t.Fatalf("error: %v", err)
	}
	if gotErr != errText[:2<<10] {
		t.Errorf("error string: %d bytes, want the first %d", len(gotErr), 2<<10)
	}

	// An oversized non-string error is kept as the head of its text: the field's
	// presence is the signal, so it must not vanish with the excess.
	var gotObjErr string
	if err := json.Unmarshal(records[3].Error, &gotObjErr); err != nil {
		t.Fatalf("oversized object error is not a string: %v (%d bytes)", err, len(records[3].Error))
	}
	if gotObjErr != errObject[:2<<10] {
		t.Errorf("object error: %d bytes, want the first %d bytes of its text", len(gotObjErr), 2<<10)
	}
}

// Offset is the file offset where the line starts, absolute even when the
// scan begins part-way through the file. Re-enrichment compares it with
// SkippedAtFirstSync, which is an offset into the whole file.
func TestScanRecordsWhereEachLineStarts(t *testing.T) {
	lines := []string{
		`{"type":"user","uuid":"u-1","sessionId":"s1","message":{"role":"user","content":"first"}}`,
		`{"type":"assistant","uuid":"a-1","sessionId":"s1","message":{"role":"assistant"}}`,
		`not json -- skipped, but its bytes still count`,
		`{"type":"user","uuid":"u-2","sessionId":"s1","message":{"role":"user","content":"가나다"}}`,
	}
	var content strings.Builder
	var starts []int64
	for _, line := range lines {
		starts = append(starts, int64(content.Len()))
		content.WriteString(line + "\n")
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	records, _, err := ScanFileContext(context.Background(), path, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{starts[0], starts[1], starts[3]}
	if got := offsetsOf(records); !reflect.DeepEqual(got, want) {
		t.Fatalf("Offset = %v, want %v", got, want)
	}

	tail, _, err := ScanFileContext(context.Background(), path, starts[1])
	if err != nil {
		t.Fatal(err)
	}
	if got, want := offsetsOf(tail), []int64{starts[1], starts[3]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scan from %d: Offset = %v, want %v", starts[1], got, want)
	}
}

func offsetsOf(records []*Record) []int64 {
	out := make([]int64, len(records))
	for i, r := range records {
		out[i] = r.Offset
	}
	return out
}

// A queued prompt keeps its text and nothing else. A string is cut to 16 KiB on
// a rune boundary; an array of blocks keeps its text blocks, joined in order
// under the same cap, and only counts its images -- the image data, base64 in
// the block, never reaches raw.
func TestScanKeepsQueuedPromptTextAndCountsImages(t *testing.T) {
	const head = `"timestamp":"2026-10-01T00:00:00Z","sessionId":"s1"`
	quote := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	long := strings.Repeat("가", 200000) // 600,000 bytes, 3 per rune
	base64Data := strings.Repeat("iVBORw0KGgo", 60000)
	image := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + base64Data + `"}}`
	records := scanLines(t, []string{
		`{"type":"attachment","uuid":"q-1",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":` + quote(long) + `}}`,
		`{"type":"attachment","uuid":"q-2",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":[` +
			`{"type":"text","text":"look at this"},` + image + `,{"type":"text","text":"and this"},` + image + `]}}`,
		`{"type":"attachment","uuid":"q-3",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":[` +
			`{"type":"text","text":` + quote(strings.Repeat("a", 10000)) + `},` + image + `,{"type":"text","text":` + quote(strings.Repeat("b", 10000)) + `}]}}`,
		`{"type":"attachment","uuid":"q-4",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":[` + image + `]}}`,
	})
	type queued struct {
		Prompt     *string `json:"prompt"`
		ImageCount int     `json:"imageCount"`
	}
	read := func(r *Record) queued {
		t.Helper()
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "iVBORw0KGgo") || len(raw) > 17<<10 {
			t.Fatalf("%s: raw carries image data or more than the cap (%d bytes)", r.UUID, len(raw))
		}
		var q queued
		if err := json.Unmarshal(r.Attachment, &q); err != nil || q.Prompt == nil {
			t.Fatalf("%s: attachment %s has no prompt (%v)", r.UUID, r.Attachment, err)
		}
		return q
	}

	// 16,384 is not a rune boundary in a run of 3-byte runes; 16,383 is.
	if q := read(records[0]); len(*q.Prompt) != 16383 || !utf8.ValidString(*q.Prompt) || !strings.HasPrefix(long, *q.Prompt) || q.ImageCount != 0 {
		t.Errorf("string prompt: %d bytes, valid=%v, imageCount=%d; want the first 16,383 bytes and no count", len(*q.Prompt), utf8.ValidString(*q.Prompt), q.ImageCount)
	}
	if q := read(records[1]); *q.Prompt != "look at this\nand this" || q.ImageCount != 2 {
		t.Errorf("block prompt = %q with %d images, want the text blocks joined and 2 images", *q.Prompt, q.ImageCount)
	}
	want := strings.Repeat("a", 10000) + "\n" + strings.Repeat("b", 16384-10001)
	if q := read(records[2]); *q.Prompt != want || q.ImageCount != 1 {
		t.Errorf("joined text: %d bytes with %d images, want the first 16,384 bytes of the join and 1 image", len(*q.Prompt), q.ImageCount)
	}
	if q := read(records[3]); *q.Prompt != "" || q.ImageCount != 1 {
		t.Errorf("image-only prompt = %q with %d images, want empty text and 1 image", *q.Prompt, q.ImageCount)
	}
}

// What this client derives is its own: a line's claim to the same names, of any
// type and in any case, is ignored rather than failing the line -- which the
// scanner would then drop, conversation and all.
func TestScanIgnoresTheLinesOwnDerivedFields(t *testing.T) {
	const head = `"timestamp":"2026-10-01T00:00:00Z","sessionId":"s1","message":{"role":"user","content":"x"}`
	cases := []struct {
		name string
		line string
		want string
	}{
		{"imagePasteCount as text", `{"type":"user","uuid":"d-1",` + head + `,"imagePasteIds":[7,8],"imagePasteCount":"three"}`,
			`{"imagePasteCount":2,"cctraceCollect":4}`},
		{"cctraceCollect fractional", `{"type":"user","uuid":"d-2",` + head + `,"cctraceCollect":1.5}`, `{"cctraceCollect":4}`},
		{"cctraceCollect past any int", `{"type":"user","uuid":"d-3",` + head + `,"cctraceCollect":1e40}`, `{"cctraceCollect":4}`},
		{"keys in upper case", `{"type":"user","uuid":"d-4",` + head + `,"CCTRACECOLLECT":"three","IMAGEPASTECOUNT":{"n":1}}`, `{"cctraceCollect":4}`},
		{"cctraceDropped claimed", `{"type":"user","uuid":"d-5",` + head + `,"promptId":"p","cctraceDropped":"promptId","CCTRACEDROPPED":[1]}`,
			`{"promptId":"p","cctraceCollect":4}`},
		{"line without a uuid", `{"type":"permission-mode","sessionId":"s1","cctraceCollect":"three","imagePasteCount":1e40}`, `{}`},
	}
	lines := make([]string, len(cases))
	for i, tc := range cases {
		lines[i] = tc.line
	}
	records := scanLines(t, lines)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := collected(t, records[i]), decodeJSON(t, tc.want); !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.Marshal(got)
				t.Errorf("kept fields\n got  %s\n want %s", gotJSON, tc.want)
			}
		})
	}
}

// CollectedRaw, what re-enrichment sends as raw, is the collected part of the
// line and nothing else: no message, no identity keys, no key a line merely
// shares a name with -- and a top-level content only as an away_summary recap.
func TestCollectedRawIsTheCollectedFieldsAlone(t *testing.T) {
	const head = `"timestamp":"2026-10-01T00:00:00Z","sessionId":"s1","cwd":"/repo","version":"2.0.0","gitBranch":"main"`
	records := scanLines(t, []string{
		`{"type":"user","uuid":"r-1",` + head + `,"message":{"role":"user","content":"the prompt"},"promptId":"p-1","isMeta":true,"slug":"x"}`,
		`{"type":"assistant","uuid":"r-2",` + head + `,"message":{"role":"assistant","content":[{"type":"text","text":"reply"}]},"requestId":"req_1"}`,
		`{"type":"system","uuid":"r-3",` + head + `,"subtype":"away_summary","content":"recap"}`,
		`{"type":"system","uuid":"r-4",` + head + `,"subtype":"turn_duration","content":"not a recap","durationMs":5}`,
		`{"type":"attachment","uuid":"r-5",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":"typed"}}`,
		`{"type":"queue-operation","operation":"enqueue",` + head + `,"content":"queued text"}`,
	})
	for _, r := range records {
		var got map[string]interface{}
		if err := json.Unmarshal(r.CollectedRaw(), &got); err != nil {
			t.Fatalf("%s: %v", r.UUID, err)
		}
		if want := collected(t, r); !reflect.DeepEqual(got, want) {
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			t.Errorf("%s %s: CollectedRaw\n got  %s\n want %s", r.Type, r.UUID, gotJSON, wantJSON)
		}
		for key := range got {
			if !strings.Contains(" "+strings.Join(CollectedKeys, " ")+" ", " "+key+" ") {
				t.Errorf("%s: CollectedRaw carries %q, which is not a collected key", r.UUID, key)
			}
		}
		if _, ok := got["content"]; ok != (r.UUID == "r-3" || r.UUID == "r-4") {
			t.Errorf("%s: top-level content present=%v, want it on the system records", r.UUID, ok)
		}
	}
}

// CollectedKeys is all re-enrichment lets the server merge into a stored row, so
// it has to be exactly the top-level keys keepCollected can write.
func TestCollectedKeysAreTheKeptFields(t *testing.T) {
	var want []string
	fields := reflect.TypeOf(collectedFields{})
	for i := 0; i < fields.NumField(); i++ {
		name, _, _ := strings.Cut(fields.Field(i).Tag.Get("json"), ",")
		if name != "imagePasteIds" { // read to be counted, never written
			want = append(want, name)
		}
	}
	got := append([]string(nil), CollectedKeys...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CollectedKeys\n got  %v\n want %v", got, want)
	}
}

// WithholdContent takes the content fields, leaves every signal, and names what
// it took in cctraceWithheld, so derived code can tell a field withheld from one
// the line never had without reasoning from the line's kind.
func TestWithholdContentLeavesOnlyTheSignals(t *testing.T) {
	const head = `"timestamp":"2026-10-01T00:00:00Z","sessionId":"s1"`
	cases := []struct {
		name string
		line string
		want string
	}{
		{"answers beside signal keys", `{"type":"user","uuid":"w-1",` + head + `,"message":{"role":"user","content":"x"},"promptId":"p-1",` +
			`"toolUseResult":{"answers":{"Q":"A"},"annotations":{"Q":{"notes":"N"}},"status":"completed","isAsync":false}}`,
			`{"promptId":"p-1","toolUseResult":{"status":"completed","isAsync":false},"cctraceCollect":4,` +
				`"cctraceWithheld":["toolUseResult.answers","toolUseResult.annotations"]}`},
		{"answers alone", `{"type":"user","uuid":"w-2",` + head + `,"message":{"role":"user","content":"x"},"toolUseResult":{"questions":[],"answers":{"Q":"A"}}}`,
			`{"cctraceCollect":4,"cctraceWithheld":["toolUseResult.answers"]}`},
		{"queued prompt", `{"type":"attachment","uuid":"w-3",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","origin":{"kind":"human"},` +
			`"prompt":[{"type":"text","text":"typed"},{"type":"image","source":{"data":"x"}}]}}`,
			`{"attachment":{"type":"queued_command","commandMode":"prompt","origin":{"kind":"human"},"imageCount":1},"cctraceCollect":4,"cctraceWithheld":["attachment.prompt"]}`},
		{"background task result", `{"type":"attachment","uuid":"w-11",` + head + `,"attachment":{"type":"queued_command","commandMode":"task-notification","prompt":"<task-notification>done</task-notification>"}}`,
			`{"attachment":{"type":"queued_command","commandMode":"task-notification"},"cctraceCollect":4,"cctraceWithheld":["attachment.prompt"]}`},
		{"plan file", `{"type":"attachment","uuid":"w-4",` + head + `,"attachment":{"type":"plan_file_reference","planFilePath":"/p.md","planContent":"# Plan"}}`,
			`{"attachment":{"type":"plan_file_reference"},"cctraceCollect":4,"cctraceWithheld":["attachment.planFilePath","attachment.planContent"]}`},
		{"plan mode", `{"type":"attachment","uuid":"w-5",` + head + `,"attachment":{"type":"plan_mode","planFilePath":"/p.md","planExists":true}}`,
			`{"attachment":{"type":"plan_mode","planExists":true},"cctraceCollect":4,"cctraceWithheld":["attachment.planFilePath"]}`},
		{"task subjects", `{"type":"attachment","uuid":"w-6",` + head + `,"attachment":{"type":"task_reminder","content":[{"id":"1","subject":"S","status":"pending"}]}}`,
			`{"attachment":{"type":"task_reminder"},"cctraceCollect":4,"cctraceWithheld":["attachment.content"]}`},
		{"recap", `{"type":"system","uuid":"w-7",` + head + `,"subtype":"away_summary","content":"recap"}`,
			`{"subtype":"away_summary","cctraceCollect":4,"cctraceWithheld":["content"]}`},
		{"task status", `{"type":"attachment","uuid":"w-8",` + head + `,"attachment":{"type":"task_status","taskId":"b1","taskType":"local_bash","status":"running"}}`,
			`{"attachment":{"type":"task_status","taskId":"b1","taskType":"local_bash","status":"running"},"cctraceCollect":4}`},
		{"turn duration", `{"type":"system","uuid":"w-9",` + head + `,"subtype":"turn_duration","durationMs":5,"pendingBackgroundAgentCount":1}`,
			`{"subtype":"turn_duration","durationMs":5,"pendingBackgroundAgentCount":1,"cctraceCollect":4}`},
		{"dropped stays named", `{"type":"attachment","uuid":"w-10",` + head + `,"attachment":{"type":"queued_command","prompt":"p","origin":"peer"}}`,
			`{"attachment":{"type":"queued_command"},"cctraceCollect":4,"cctraceDropped":["attachment.origin"],"cctraceWithheld":["attachment.prompt"]}`},
	}
	lines := make([]string, len(cases))
	for i, tc := range cases {
		lines[i] = tc.line
	}
	records := scanLines(t, lines)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records[i].WithholdContent()
			if got, want := collected(t, records[i]), decodeJSON(t, tc.want); !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.Marshal(got)
				t.Errorf("after WithholdContent\n got  %s\n want %s", gotJSON, tc.want)
			}
		})
	}
}
