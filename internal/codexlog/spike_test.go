//go:build spike

package codexlog_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestSpikeParseCodexJSONL reads actual ~/.codex/sessions/ files and dumps
// a field inventory. Run with: go test -tags spike ./internal/codexlog/ -v -run TestSpikeParseCodexJSONL
func TestSpikeParseCodexJSONL(t *testing.T) {
	codexDir := os.Getenv("CODEX_CONFIG_DIR")
	if codexDir == "" {
		home, _ := os.UserHomeDir()
		codexDir = filepath.Join(home, ".codex")
	}
	sessionsDir := filepath.Join(codexDir, "sessions")

	files := collectFiles(t, sessionsDir)
	if len(files) == 0 {
		t.Fatalf("no rollout-*.jsonl files found under %s", sessionsDir)
	}
	fmt.Printf("\n=== Codex JSONL Spike ===\n")
	fmt.Printf("Sessions dir: %s\n", sessionsDir)
	fmt.Printf("Files found: %d (analyzing up to 10)\n\n", len(files))

	// Sort newest first (by modification time)
	type fileInfo struct {
		path    string
		modTime int64
	}
	var fis []fileInfo
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			fis = append(fis, fileInfo{f, fi.ModTime().UnixNano()})
		}
	}
	sort.Slice(fis, func(i, j int) bool { return fis[i].modTime > fis[j].modTime })
	files = make([]string, 0, len(fis))
	for _, fi := range fis {
		files = append(files, fi.path)
	}
	fmt.Printf("Newest file: %s\n\n", files[0])

	allFiles := files // keep full list for rare-field search
	if len(files) > 10 {
		files = files[:10]
	}

	// Accumulators
	typeFreq := map[string]int{}              // type value → count ("(none)" if absent)
	typeFields := map[string]map[string]int{} // type → field → count
	roleFreq := map[string]int{}
	contentTypes := map[string]int{}
	recordTypeFreq := map[string]int{} // record_type field values

	for _, f := range files {
		parseFile(t, f, typeFreq, typeFields, roleFreq, contentTypes, recordTypeFreq)
	}

	// --- Report ---
	fmt.Println("=== Record type frequency ===")
	for _, k := range sortedKeys(typeFreq) {
		fmt.Printf("  %-20s %d\n", k, typeFreq[k])
	}

	fmt.Println("\n=== Fields per record type ===")
	for _, typ := range sortedKeys(typeFields) {
		fmt.Printf("  [type=%q]\n", typ)
		for _, field := range sortedKeys(typeFields[typ]) {
			fmt.Printf("    %-30s (seen %d times)\n", field, typeFields[typ][field])
		}
	}

	fmt.Println("\n=== Role frequency ===")
	for _, k := range sortedKeys(roleFreq) {
		fmt.Printf("  %-20s %d\n", k, roleFreq[k])
	}

	fmt.Println("\n=== content[].type frequency ===")
	for _, k := range sortedKeys(contentTypes) {
		fmt.Printf("  %-20s %d\n", k, contentTypes[k])
	}

	fmt.Println("\n=== record_type field values (for type=(none) records) ===")
	for _, k := range sortedKeys(recordTypeFreq) {
		fmt.Printf("  %-30s %d\n", k, recordTypeFreq[k])
	}

	fmt.Println("\n=== Key fields to confirm ===")
	checkField("cwd", typeFields)
	checkField("model", typeFields)
	checkField("usage", typeFields)
	checkField("session_id", typeFields)
	checkField("id", typeFields)
	checkField("timestamp", typeFields)
	checkField("created_at", typeFields)
	checkField("record_type", typeFields)
	checkField("git", typeFields)
	checkField("instructions", typeFields)

	// Show sample raw content of interesting records
	fmt.Println("\n=== Sample state records (first 3) ===")
	printSampleRecords(t, files, "state", 3)

	fmt.Println("\n=== Sample session init records (type=none, no record_type, first 3) ===")
	printSampleInitRecords(t, files, 3)

	fmt.Println("\n=== Records containing git or instructions field ===")
	printRecordsWithField(t, allFiles, "git", 2)
	printRecordsWithField(t, allFiles, "instructions", 2)

	// New format (2026-04+): payload-wrapped records
	fmt.Println("\n=== New format: payload field keys per type ===")
	printPayloadKeys(t, files)

	fmt.Println("\n=== New format: sample session_meta payload ===")
	printSamplePayload(t, files, "session_meta", 2)

	fmt.Println("\n=== New format: sample response_item payload ===")
	printSamplePayload(t, files, "response_item", 2)

	fmt.Println("\n=== New format: sample turn_context payload ===")
	printSamplePayload(t, files, "turn_context", 2)
}

func collectFiles(t *testing.T, sessionsDir string) []string {
	t.Helper()
	var files []string
	// root level: ~/.codex/sessions/rollout-*.jsonl
	root, _ := filepath.Glob(filepath.Join(sessionsDir, "rollout-*.jsonl"))
	files = append(files, root...)
	// date-nested: ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl
	nested, _ := filepath.Glob(filepath.Join(sessionsDir, "*", "*", "*", "rollout-*.jsonl"))
	files = append(files, nested...)
	return files
}

func parseFile(t *testing.T, path string, typeFreq map[string]int, typeFields map[string]map[string]int, roleFreq map[string]int, contentTypes map[string]int, recordTypeFreq map[string]int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Logf("open %s: %v", path, err)
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	lineNum := 0
	for sc.Scan() && lineNum < 200 {
		lineNum++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw map[string]interface{}
		if err := json.Unmarshal(line, &raw); err != nil {
			t.Logf("%s:%d unmarshal error: %v", path, lineNum, err)
			continue
		}

		typ := "(none)"
		if v, ok := raw["type"]; ok {
			if s, ok := v.(string); ok {
				typ = s
			}
		}
		typeFreq[typ]++

		if _, ok := typeFields[typ]; !ok {
			typeFields[typ] = map[string]int{}
		}
		for k := range raw {
			typeFields[typ][k]++
		}

		if v, ok := raw["role"]; ok {
			if s, ok := v.(string); ok {
				roleFreq[s]++
			}
		}

		// record_type field values
		if v, ok := raw["record_type"]; ok {
			if s, ok := v.(string); ok {
				recordTypeFreq[s]++
			}
		}

		// content[].type
		if v, ok := raw["content"]; ok {
			if arr, ok := v.([]interface{}); ok {
				for _, item := range arr {
					if m, ok := item.(map[string]interface{}); ok {
						if ct, ok := m["type"].(string); ok {
							contentTypes[ct]++
						}
					}
				}
			}
		}
	}
}

func checkField(field string, typeFields map[string]map[string]int) {
	found := false
	for typ, fields := range typeFields {
		if n, ok := fields[field]; ok {
			fmt.Printf("  %-15s found in type=%-12q (%d times)\n", field, typ, n)
			found = true
		}
	}
	if !found {
		fmt.Printf("  %-15s NOT FOUND in any record type\n", field)
	}
}

func printSampleRecords(t *testing.T, files []string, recordType string, max int) {
	t.Helper()
	count := 0
	for _, path := range files {
		if count >= max {
			break
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
		for sc.Scan() && count < max {
			var raw map[string]interface{}
			if json.Unmarshal(sc.Bytes(), &raw) != nil {
				continue
			}
			if v, ok := raw["record_type"]; ok {
				if s, _ := v.(string); s == recordType {
					b, _ := json.MarshalIndent(raw, "  ", "  ")
					fmt.Printf("  %s\n", b)
					count++
				}
			}
		}
		f.Close()
	}
}

func printSampleInitRecords(t *testing.T, files []string, max int) {
	t.Helper()
	count := 0
	for _, path := range files {
		if count >= max {
			break
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
		for sc.Scan() && count < max {
			var raw map[string]interface{}
			if json.Unmarshal(sc.Bytes(), &raw) != nil {
				continue
			}
			_, hasType := raw["type"]
			_, hasRecordType := raw["record_type"]
			if !hasType && !hasRecordType {
				b, _ := json.MarshalIndent(raw, "  ", "  ")
				fmt.Printf("  %s\n", b)
				count++
			}
		}
		f.Close()
	}
}

func printRecordsWithField(t *testing.T, files []string, field string, max int) {
	t.Helper()
	count := 0
	for _, path := range files {
		if count >= max {
			break
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
		for sc.Scan() && count < max {
			var raw map[string]interface{}
			if json.Unmarshal(sc.Bytes(), &raw) != nil {
				continue
			}
			if _, ok := raw[field]; ok {
				b, _ := json.MarshalIndent(raw, "  ", "  ")
				fmt.Printf("  [field=%q in file %s]\n  %s\n", field, filepath.Base(path), b)
				count++
			}
		}
		f.Close()
	}
	if count == 0 {
		fmt.Printf("  (no records with field %q found)\n", field)
	}
}

func printPayloadKeys(t *testing.T, files []string) {
	t.Helper()
	payloadFields := map[string]map[string]int{} // type → payload key → count
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
		for sc.Scan() {
			var raw map[string]interface{}
			if json.Unmarshal(sc.Bytes(), &raw) != nil {
				continue
			}
			typ, _ := raw["type"].(string)
			if typ == "" {
				continue
			}
			payload, ok := raw["payload"]
			if !ok {
				continue
			}
			if m, ok := payload.(map[string]interface{}); ok {
				if _, ok := payloadFields[typ]; !ok {
					payloadFields[typ] = map[string]int{}
				}
				for k := range m {
					payloadFields[typ][k]++
				}
			}
		}
		f.Close()
	}
	for _, typ := range sortedKeys(payloadFields) {
		fmt.Printf("  [type=%q payload keys]\n", typ)
		for _, k := range sortedKeys(payloadFields[typ]) {
			fmt.Printf("    %-35s %d\n", k, payloadFields[typ][k])
		}
	}
}

func printSamplePayload(t *testing.T, files []string, recType string, max int) {
	t.Helper()
	count := 0
	for _, path := range files {
		if count >= max {
			break
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
		for sc.Scan() && count < max {
			var raw map[string]interface{}
			if json.Unmarshal(sc.Bytes(), &raw) != nil {
				continue
			}
			if typ, _ := raw["type"].(string); typ != recType {
				continue
			}
			b, _ := json.MarshalIndent(raw["payload"], "  ", "  ")
			fmt.Printf("  payload: %s\n", b)
			count++
		}
		f.Close()
	}
	if count == 0 {
		fmt.Printf("  (no %q records in selected files)\n", recType)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
