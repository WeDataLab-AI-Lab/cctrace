package sessiontrace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// CanonicalJSON sorts object keys, preserves json.Number and does not HTML
// escape. It never removes fields; message normalization is explicit below.
func CanonicalJSON(value any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeCanonical(&b, value); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeCanonical(b *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeCanonicalString(b, v)
	case json.Number:
		text := string(v)
		if strings.ContainsAny(text, ".eE") {
			f, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(f)
			if err != nil {
				return err
			}
			text = string(raw)
		}
		if text == "-0" {
			text = "0"
		}
		if !json.Valid([]byte(text)) {
			return fmt.Errorf("invalid number")
		}
		b.WriteString(text)
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, z := utf16.Encode([]rune(keys[i])), utf16.Encode([]rune(keys[j]))
			for n := 0; n < len(a) && n < len(z); n++ {
				if a[n] != z[n] {
					return a[n] < z[n]
				}
			}
			return len(a) < len(z)
		})
		b.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonicalString(b, key)
			b.WriteByte(':')
			if err := writeCanonical(b, v[key]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	default:
		var encoded bytes.Buffer
		e := json.NewEncoder(&encoded)
		e.SetEscapeHTML(false)
		if err := e.Encode(value); err != nil {
			return err
		}
		var normalized any
		d := json.NewDecoder(&encoded)
		d.UseNumber()
		if err := d.Decode(&normalized); err != nil {
			return err
		}
		return writeCanonical(b, normalized)
	}
	return nil
}

func writeCanonicalString(b *bytes.Buffer, value string) {
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

func CanonicalHash(value any) (string, error) {
	b, err := CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// DecodeObject retains the original precision of integer and decimal tokens.
func DecodeObject(raw []byte) (map[string]any, error) {
	var object map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, fmt.Errorf("expected object")
	}
	return object, nil
}

func copyValue(value any, stripCache bool) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			if !stripCache || key != "cache_control" {
				out[key] = copyValue(child, stripCache)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = copyValue(v[i], stripCache)
		}
		return out
	default:
		return value
	}
}

func NormalizeMessage(value any) (map[string]any, error) {
	m, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected message object")
	}
	role, ok := m["role"].(string)
	if !ok || role == "" {
		return nil, fmt.Errorf("missing role")
	}
	content, exists := m["content"]
	if !exists {
		return nil, fmt.Errorf("missing content")
	}
	if text, ok := content.(string); ok {
		content = []any{map[string]any{"type": "text", "text": text}}
	}
	if _, ok := content.([]any); !ok {
		return nil, fmt.Errorf("unreadable content")
	}
	out := map[string]any{"role": role, "content": copyValue(content, true)}
	// Native protocol blobs can carry a system-role message with output_config.
	// This is source payload, not an assistant transcript's model/usage metadata.
	if role == "system" {
		if config, ok := m["output_config"]; ok {
			out["output_config"] = copyValue(config, true)
		}
	}
	return out, nil
}

func MessageHash(value any) (string, error) {
	m, err := NormalizeMessage(value)
	if err != nil {
		return "", err
	}
	return CanonicalHash(m)
}

// TranscriptPrefixes keeps each observed prefix immutable. Consecutive user
// rows combine; assistant fragments combine only under an equal message id.
func TranscriptPrefixes(messages []map[string]any) ([]map[string]any, error) {
	var out []map[string]any
	var current map[string]any
	var lastID string
	for _, message := range messages {
		normal, err := NormalizeMessage(message)
		if err != nil {
			return nil, err
		}
		id, _ := message["id"].(string)
		merge := current != nil && current["role"] == normal["role"] && (normal["role"] == "user" || (id != "" && id == lastID))
		if merge {
			current = copyValue(current, false).(map[string]any)
			current["content"] = append(current["content"].([]any), normal["content"].([]any)...)
		} else {
			current = normal
		}
		out = append(out, copyValue(current, false).(map[string]any))
		lastID = id
	}
	return out, nil
}
