package store

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Search visible message text and tool names, excluding arguments, results and
// thinking bodies. One JSONPath evaluation avoids repeatedly detoasting raw or
// converting complete content arrays (including large tool outputs) to text.
func traceFlowSearchValues(query string) (string, string) {
	like := "%" + strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(query) + "%"
	quoted, _ := json.Marshal(regexp.QuoteMeta(query))
	match := string(quoted) + ` flag "i"`
	checks := make([]string, 0, 4)
	for _, path := range []string{"message.content", "payload.content", "content"} {
		// Lax [*] also wraps string content, preserving string/array adapters.
		checks = append(checks, `exists(@.`+path+`[*] ? ((@.type() == "string" && @ like_regex `+match+`) ||
 ((@.type == "text" || @.type == "input_text" || @.type == "output_text") && @.text like_regex `+match+`) ||
 (@.type == "tool_use" && @.name like_regex `+match+`)))`)
	}
	checks = append(checks, `exists(@.payload.name ? (@ like_regex `+match+`))`)
	return like, `$ ? (` + strings.Join(checks, ` || `) + `)`
}

func traceFlowSearchPredicateSQL(a, like, path string) string {
	return `(` + a + `.tool_name ILIKE ` + like + ` OR jsonb_path_exists(` + a + `.raw,` + path + `::jsonpath))`
}

// Only these rows can reach a root through the flow search below. Attachment
// and other stored context records are excluded before JSONPath is evaluated;
// on a large transcript they make the candidate scan several times larger.
func traceFlowSearchCandidateSQL(a string) string {
	return a + `.record_type IN ('user','assistant','system','message','developer','context','tool_call','tool_output','reasoning')`
}
