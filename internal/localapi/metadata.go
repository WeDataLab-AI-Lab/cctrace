package localapi

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"cctrace/internal/projecthash"
)

// metadata reads bounded complete lines at either end; absent observations remain nil.
func metadata(e fileEntry) (project, entrypoint, model, title *string, start, end *time.Time) {
	f, err := os.Open(e.path)
	if err != nil {
		return
	}
	defer f.Close()
	const window int64 = 32768
	read := func(offset, limit int64) []byte {
		if limit <= 0 {
			return nil
		}
		buf := make([]byte, limit)
		n, err := f.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			return nil
		}
		return buf[:n]
	}
	size := e.info.Size()
	front := read(0, min(size, window))
	back := []byte(nil)
	if size > window {
		back = read(max(0, size-window), min(size, window))
		if p := bytes.IndexByte(back, '\n'); p >= 0 {
			back = back[p+1:]
		} else {
			back = nil
		}
	}
	inspect := func(lines []byte) {
		for _, line := range bytes.Split(lines, []byte{'\n'}) {
			if !json.Valid(line) {
				continue
			}
			var rec struct {
				Type       string    `json:"type"`
				Timestamp  time.Time `json:"timestamp"`
				CWD        string    `json:"cwd"`
				Entrypoint string    `json:"entrypoint"`
				Payload    struct {
					Type       string `json:"type"`
					CWD        string `json:"cwd"`
					Originator string `json:"originator"`
					Model      string `json:"model"`
					Message    struct {
						Content json.RawMessage `json:"content"`
					} `json:"message"`
				} `json:"payload"`
				Message struct {
					Model   string          `json:"model"`
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &rec) != nil {
				continue
			}
			if !rec.Timestamp.IsZero() {
				if start == nil || rec.Timestamp.Before(*start) {
					t := rec.Timestamp
					start = &t
				}
				if end == nil || rec.Timestamp.After(*end) {
					t := rec.Timestamp
					end = &t
				}
			}
			if rec.CWD != "" && project == nil {
				name := projecthash.NameFromPath(rec.CWD)
				project = &name
			}
			if rec.Payload.CWD != "" && project == nil {
				name := projecthash.NameFromPath(rec.Payload.CWD)
				project = &name
			}
			if rec.Entrypoint != "" && entrypoint == nil {
				v := rec.Entrypoint
				entrypoint = &v
			}
			if rec.Payload.Originator != "" && entrypoint == nil {
				v := rec.Payload.Originator
				entrypoint = &v
			}
			if rec.Message.Model != "" && rec.Message.Model != "<synthetic>" {
				v := rec.Message.Model
				model = &v
			}
			if rec.Payload.Model != "" {
				v := rec.Payload.Model
				model = &v
			}
			content := rec.Message.Content
			if e.agent == "codex" {
				content = rec.Payload.Message.Content
			}
			if title == nil && (rec.Type == "user" || rec.Payload.Type == "user_message") {
				var text string
				if json.Unmarshal(content, &text) == nil && strings.TrimSpace(text) != "" {
					text = strings.TrimSpace(text)
					if len(text) > 160 {
						text = text[:160]
					}
					title = &text
				}
			}
		}
	}
	if len(front) > 0 {
		if p := bytes.LastIndexByte(front, '\n'); p >= 0 {
			inspect(front[:p+1])
		}
	}
	if len(back) > 0 {
		if p := bytes.LastIndexByte(back, '\n'); p >= 0 {
			inspect(back[:p+1])
		}
	}
	return
}

// observedNativeID reads only complete header lines; it makes no ownership claim.
func observedNativeID(e fileEntry) string {
	f, err := openEntry(e)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 32768))
	if err != nil {
		return ""
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return ""
	}
	for _, line := range bytes.Split(data[:end+1], []byte{'\n'}) {
		var header struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
			Payload   struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &header) != nil {
			continue
		}
		if e.agent == "claude" && header.SessionID != "" {
			return header.SessionID
		}
		if e.agent == "codex" && header.Type == "session_meta" && header.Payload.ID != "" {
			return header.Payload.ID
		}
	}
	return ""
}
