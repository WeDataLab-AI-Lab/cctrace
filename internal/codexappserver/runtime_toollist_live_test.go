//go:build codexlive

package codexappserver

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/airuntime"
)

type capturedRequest struct {
	path   string
	bearer string
	body   []byte
}

type toolSpec struct {
	Type        string     `json:"type"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Tools       []toolSpec `json:"tools"`
}

type modelRequest struct {
	Model string     `json:"model"`
	Tools []toolSpec `json:"tools"`
	Input []struct {
		Type  string     `json:"type"`
		Tools []toolSpec `json:"tools"`
	} `json:"input"`
}

var nestedToolHeader = regexp.MustCompile("### `([^`]+)`")

// toolSurface flattens what the model can call. Tools arrive either as the
// top-level list or, for code-mode models, as an additional_tools input item;
// code mode's exec lists the tools its script may call only in its description.
func toolSurface(req modelRequest) []string {
	var out []string
	var walk func(prefix string, tools []toolSpec)
	walk = func(prefix string, tools []toolSpec) {
		for _, tl := range tools {
			switch {
			case tl.Type == "namespace" && tl.Name == "functions":
				walk("", tl.Tools)
			case tl.Type == "namespace":
				walk(tl.Name+".", tl.Tools)
			case tl.Name == "exec":
				var nested []string
				for _, m := range nestedToolHeader.FindAllStringSubmatch(tl.Description, -1) {
					nested = append(nested, m[1])
				}
				out = append(out, prefix+"exec["+strings.Join(nested, ",")+"]")
			case tl.Name != "":
				out = append(out, prefix+tl.Name)
			default:
				out = append(out, prefix+tl.Type)
			}
		}
	}
	walk("", req.Tools)
	for _, item := range req.Input {
		if item.Type == "additional_tools" {
			walk("", item.Tools)
		}
	}
	sort.Strings(out)
	return out
}

// readWebSocketMessage accepts a WebSocket upgrade and returns the client's
// first text message. The built-in openai provider sends the model request as
// a Responses-over-WebSocket `response.create`, and go.mod carries no
// WebSocket library, so this is the handshake and unmasking by hand. No
// extension is accepted, so frames are never compressed.
func readWebSocketMessage(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	conn, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(sum[:]))
	if err := rw.Flush(); err != nil {
		return nil, err
	}
	defer func() {
		_, _ = rw.Write([]byte{0x88, 0x00}) // close, no status
		_ = rw.Flush()
	}()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var msg []byte
	for {
		var head [2]byte
		if _, err := io.ReadFull(rw, head[:]); err != nil {
			return msg, err
		}
		n := uint64(head[1] & 0x7f)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(rw, b[:]); err != nil {
				return msg, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(rw, b[:]); err != nil {
				return msg, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		var mask [4]byte
		masked := head[1]&0x80 != 0
		if masked {
			if _, err := io.ReadFull(rw, mask[:]); err != nil {
				return msg, err
			}
		}
		if n > 16<<20 {
			return msg, errors.New("websocket frame over 16 MiB")
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(rw, payload); err != nil {
			return msg, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch head[0] & 0x0f {
		case 0x0, 0x1: // continuation, text
			msg = append(msg, payload...)
			if head[0]&0x80 != 0 {
				return msg, nil
			}
		case 0x8:
			return msg, errors.New("client closed before sending a message")
		}
	}
}

// TestLiveModelRequestToolList records the tool list codex really sends the
// model with the home PrepareHome writes and the flags Run passes.
// openai_base_url points the model request at a local server that captures it
// and refuses it, so no model turn is spent:
//
//	CCTRACE_CODEX_LIVE_AUTH=$HOME/.codex/auth.json \
//	  go test -tags codexlive ./internal/codexappserver -run LiveModelRequestToolList -v
//
// Without CCTRACE_CODEX_LIVE_AUTH only the api_key case runs, with a dummy key.
// CCTRACE_CODEX_LIVE_MODEL picks another catalog model; CCTRACE_CODEX_CAPTURE_DIR
// keeps the captured request bodies.
func TestLiveModelRequestToolList(t *testing.T) {
	cases := []struct {
		name   string
		auth   string
		apiKey string
	}{
		{name: "api_key", apiKey: "sk-cctrace-capture-dummy"},
		{name: "chatgpt", auth: os.Getenv("CCTRACE_CODEX_LIVE_AUTH")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.apiKey == "" && tc.auth == "" {
				t.Skip("CCTRACE_CODEX_LIVE_AUTH not set")
			}
			var mu sync.Mutex
			var requests []capturedRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rq := capturedRequest{
					path:   r.Method + " " + r.URL.Path,
					bearer: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
				}
				if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					rq.path = "WS " + r.URL.Path
					msg, err := readWebSocketMessage(w, r)
					if err != nil {
						rq.path += " (" + err.Error() + ")"
					}
					rq.body = msg
					mu.Lock()
					requests = append(requests, rq)
					mu.Unlock()
					return
				}
				rq.body, _ = io.ReadAll(r.Body)
				mu.Lock()
				requests = append(requests, rq)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"captured by cctrace test","type":"invalid_request_error"}}`)
			}))
			defer srv.Close()

			home := t.TempDir()
			if tc.auth != "" {
				if err := os.Symlink(tc.auth, filepath.Join(home, "auth.json")); err != nil {
					t.Fatal(err)
				}
			}
			// The same RuntimeConfig shape cmd/cctraced builds.
			cfg := RuntimeConfig{
				Home:            home,
				Model:           os.Getenv("CCTRACE_CODEX_LIVE_MODEL"),
				ReasoningEffort: "low",
				APIKey:          tc.apiKey,
				Disable:         []string{"apps"},
			}
			if err := PrepareHome(cfg); err != nil {
				t.Fatalf("PrepareHome: %v", err)
			}
			// Top-level keys go before PrepareHome's tables; the added [features]
			// keeps request bodies plain JSON.
			path := filepath.Join(home, "config.toml")
			prepared, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			config := "openai_base_url = \"" + srv.URL + "\"\n" + string(prepared) + "\n[features]\nenable_request_compression = false\n"
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}

			tool := airuntime.Tool{
				Name:        "query_segments",
				Description: "Return the user's work segments for an ISO week.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"week":{"type":"string"}},"required":["week"],"additionalProperties":false}`),
				Handler: func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
					t.Error("the refused model request cannot call a tool")
					return airuntime.ToolOutput{}, nil
				},
			}
			_, err = NewRuntime(cfg).Run(context.Background(), airuntime.RunRequest{
				Instructions:   "You summarize fixture work segments.",
				Prompt:         "query_segments 도구로 week=2026-W37 을 조회해.",
				Tools:          []airuntime.Tool{tool},
				OutputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string"}}}`),
				ToolTimeout:    5 * time.Second,
				WallClock:      60 * time.Second,
				MaxToolCalls:   1,
				MaxTotalTokens: 10_000,
			}, nil)
			t.Logf("run error (expected, the endpoint refuses): %v", err)

			mu.Lock()
			defer mu.Unlock()
			var last *modelRequest
			for i, rq := range requests {
				t.Logf("captured %s bytes=%d", rq.path, len(rq.body))
				// Bodies hold prompts and codex's own instructions, never credentials.
				if dir := os.Getenv("CCTRACE_CODEX_CAPTURE_DIR"); dir != "" && len(rq.body) > 0 {
					name := fmt.Sprintf("%s-%02d.json", tc.name, i)
					if err := os.WriteFile(filepath.Join(dir, name), rq.body, 0o600); err != nil {
						t.Errorf("save %s: %v", name, err)
					}
				}
				var req modelRequest
				if json.Unmarshal(rq.body, &req) != nil || req.Input == nil {
					continue
				}
				if tc.apiKey != "" && rq.bearer != tc.apiKey {
					t.Errorf("%s: the API key did not reach the model request", rq.path)
				}
				last = &req
			}
			if last == nil {
				t.Fatalf("no model request reached the capture server (%d requests)", len(requests))
			}
			got := toolSurface(*last)
			t.Logf("model=%s tools=%v", last.Model, got)
			// request_user_input stays: no flag removes it, and Run refuses the
			// server request it would make. wait only resumes a running exec.
			codeMode := []string{"exec[query_segments]", "request_user_input", "request_user_input_async", "wait"}
			direct := []string{"query_segments", "request_user_input"}
			if !slices.Equal(got, codeMode) && !slices.Equal(got, direct) {
				t.Errorf("tools sent to the model = %v, want %v (code mode) or %v (direct)", got, codeMode, direct)
			}
		})
	}
}
