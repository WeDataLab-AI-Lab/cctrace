package codexappserver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fake app-server used to be a /bin/sh script written to a temp file. That
// cannot run on Windows -- there is no shebang handling -- so ten tests in this
// package failed there for the harness rather than for anything under test
// (#657). It matters because this package is client-only: cmd/cctrace imports
// it, cmd/cctraced does not, and the client is what runs on a user's Windows
// machine. Skipping would have hidden the platform these tests are for.
//
// The replacement is the test binary itself. The package under test starts the
// child as `<bin> app-server`, so the binary can recognise that argv and act as
// the server instead of running tests. No compilation step, no shell, portable
// by construction.
const fakeServerEnv = "CCTRACE_FAKE_APP_SERVER"

func TestMain(m *testing.M) {
	if behavior := os.Getenv(fakeServerEnv); behavior != "" && len(os.Args) > 1 && os.Args[1] == "app-server" {
		runFakeAppServer(behavior)
		return
	}
	os.Exit(m.Run())
}

// Behaviors. Each name is one shape of server the tests need; the old scripts
// are reproduced line for line so what each test pins is unchanged.
const (
	fakeReply       = "reply"        // initialize, then the reply, then exit
	fakeReplyNotify = "reply-notify" // same, with a notification before each response
	fakeReplyNoExit = "reply-noexit" // answers and keeps reading: only teardown ends it
	fakeRPCError    = "rpc-error"    // initialize, then a JSON-RPC error
	fakeGarbage     = "garbage"      // non-JSON first line, then reads forever
	fakeExit3       = "exit3"        // dies immediately with a non-zero status
	fakeHang        = "hang"         // never answers
	fakeSilent      = "silent"       // exits at once without a word
	fakeNoise       = "noise"        // unsolicited notifications without end
	fakeDiesMidway  = "dies-midway"  // answers initialize, then exits before the read
)

// Runtime behaviors play a whole turn: initialize, thread/start, turn/start,
// then the shape named below. They parse the client's JSON rather than
// matching substrings, because the runtime's requests carry user text.
const (
	fakeToolsHappy      = "tools-happy"      // two item/tool/call requests, completes with their answers
	fakeToolsHang       = "tools-hang"       // one tool call, then never completes and ignores interrupt
	fakeToolsStall      = "tools-stall"      // one tool call, then stops reading stdin for good
	fakeUnexpectedItem  = "unexpected-item"  // starts an mcpToolCall item, then a tool call; honors interrupt
	fakeSchemaGarbage   = "schema-garbage"   // completes with a final answer that is not JSON
	fakeApprovalRequest = "approval-request" // asks for command approval, completes after any answer
	fakeBigStream       = "big-stream"       // agentMessage deltas without end
	fakeTokenBudget     = "token-budget"     // reports usage past any small cap; honors interrupt
	fakeToolsFlood      = "tools-flood"      // seven tool calls at once, then waits; honors interrupt
	fakeModels          = "models"           // model/list in two pages, one hidden model
	fakeModelsError     = "models-error"     // model/list answers a JSON-RPC error
)

// Account behaviors answer the account/* methods. A device login writes a
// ChatGPT auth.json into CODEX_HOME when it succeeds, an API key login a key
// file, and logout removes the file, as the real server does.
const (
	fakeDeviceLoginOK   = "device-login-ok"   // code, then login/completed success
	fakeDeviceLoginWait = "device-login-wait" // code, then nothing; answers cancel
	fakeDeviceLoginFail = "device-login-fail" // code, then login/completed failure
	fakeDeviceLoginExit = "device-login-exit" // code, then exits
	fakeDeviceLoginLink = "device-login-link" // code, auth.json left as a symlink, then success
	fakeAPIKeyLogin     = "api-key-login"     // stores the key, login/completed success
	fakeAPIKeyReject    = "api-key-reject"    // JSON-RPC error that repeats the key
	fakeLogout          = "logout"            // removes auth.json
)

var fakeRuntimeBehaviors = map[string]bool{
	fakeToolsHappy: true, fakeToolsHang: true, fakeToolsStall: true, fakeUnexpectedItem: true, fakeSchemaGarbage: true,
	fakeApprovalRequest: true, fakeBigStream: true, fakeTokenBudget: true, fakeToolsFlood: true,
	fakeModels: true, fakeModelsError: true,
	fakeDeviceLoginOK: true, fakeDeviceLoginWait: true, fakeDeviceLoginFail: true, fakeDeviceLoginExit: true, fakeDeviceLoginLink: true,
	fakeAPIKeyLogin: true, fakeAPIKeyReject: true, fakeLogout: true,
}

// fakeCaptureEnv names a file the runtime fake appends to: first a line with
// its argv and whether CODEX_API_KEY reached it, then every line the client
// wrote. Tests read it to check what was actually sent, not what was built.
const fakeCaptureEnv = "CCTRACE_FAKE_CAPTURE"

// fakeFinalTool is one tool answer echoed back in the happy final answer.
type fakeFinalTool struct {
	ID      string `json:"id"`
	Success bool   `json:"success"`
	Text    string `json:"text"`
}

// fakeRunCounterEnv names a file the child appends a line to on every start.
// The tests that count spawns used to rewrite the shell script to do it; with
// the binary as the server there is nothing to rewrite, so the child is told
// where to tally instead.
const fakeRunCounterEnv = "CCTRACE_FAKE_RUN_COUNTER"

// fakeHomeSinkEnv names a file the child writes its CODEX_HOME into. The test
// that checks the home reaches the child used to overwrite the server file with
// a script that echoed it -- with the test binary as the server that would mean
// overwriting a running executable, which Windows refuses outright and which
// only ever worked here by luck.
const fakeHomeSinkEnv = "CCTRACE_FAKE_HOME_SINK"

// fakeEnvSinkEnv names a file the child writes, as JSON, whether daemon-only
// variables reached it -- for the Fetch path, which has no capture file.
const fakeEnvSinkEnv = "CCTRACE_FAKE_ENV_SINK"

const accountReply = `{"jsonrpc":"2.0","id":3,"result":{"account":{"type":"chatgpt","email":"login@example.test","planType":"pro"},"requiresOpenaiAuth":true}}`

func runFakeAppServer(behavior string) {
	if path := os.Getenv(fakeRunCounterEnv); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
			fmt.Fprintln(f, "run")
			_ = f.Close()
		}
	}

	if path := os.Getenv(fakeHomeSinkEnv); path != "" {
		_ = os.WriteFile(path, []byte(os.Getenv("CODEX_HOME")), 0600)
	}
	if path := os.Getenv(fakeEnvSinkEnv); path != "" {
		b, _ := json.Marshal(map[string]bool{"databaseURL": os.Getenv("DATABASE_URL") != "", "path": os.Getenv("PATH") != ""})
		_ = os.WriteFile(path, b, 0600)
	}

	out := bufio.NewWriter(os.Stdout)
	say := func(s string) {
		fmt.Fprintln(out, s)
		out.Flush()
	}

	if fakeRuntimeBehaviors[behavior] {
		runFakeRuntimeServer(behavior, say)
		return
	}

	switch behavior {
	case fakeExit3:
		os.Exit(3)
	case fakeSilent:
		return
	case fakeHang:
		select {}
	case fakeNoise:
		for {
			say(`{"jsonrpc":"2.0","method":"noise","params":{}}`)
		}
	case fakeGarbage:
		say("this is not json")
		// Read to EOF without answering, the way `exec cat >/dev/null` did.
		_, _ = os.Stdin.Read(make([]byte, 1<<16))
		select {}
	}

	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 1<<20), 1<<20)
	for scan.Scan() {
		line := scan.Text()
		switch {
		case strings.Contains(line, "initialize"):
			if behavior == fakeDiesMidway {
				say(`{"jsonrpc":"2.0","id":1,"result":{}}`)
				return
			}
			if behavior == fakeReplyNotify {
				say(`{"jsonrpc":"2.0","method":"account/updated","params":{}}`)
			}
			say(`{"jsonrpc":"2.0","id":1,"result":{}}`)
		case strings.Contains(line, "rateLimits"):
			if behavior == fakeRPCError {
				say(`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"not logged in"}}`)
				return
			}
			if behavior == fakeReplyNotify {
				say(`{"jsonrpc":"2.0","method":"thread/started","params":{}}`)
			}
			say(os.Getenv("CCTRACE_TEST_REPLY"))
			if behavior != fakeReplyNoExit {
				return
			}
		case strings.Contains(line, "account/read"):
			say(accountReply)
		}
	}
	// stdin closed with no exit rule of its own: linger so teardown is what ends
	// the process, which is the property several tests are checking.
	if behavior == fakeReplyNoExit {
		time.Sleep(time.Minute)
	}
}

func runFakeRuntimeServer(behavior string, say func(string)) {
	var capture *os.File
	if path := os.Getenv(fakeCaptureEnv); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
			capture = f
			head, _ := json.Marshal(map[string]any{
				"argv": os.Args[1:], "codexApiKey": os.Getenv("CODEX_API_KEY") != "",
				"databaseURL": os.Getenv("DATABASE_URL") != "", "path": os.Getenv("PATH") != "", "home": os.Getenv("HOME"),
			})
			fmt.Fprintln(f, string(head))
		}
	}
	msg := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	reply := func(id json.RawMessage, result any) {
		say(msg(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}))
	}
	note := func(method string, params any) {
		say(msg(map[string]any{"jsonrpc": "2.0", "method": method, "params": params}))
	}
	request := func(id int, method string, params any) {
		say(msg(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}))
	}
	itemStarted := func(item map[string]any) {
		note("item/started", map[string]any{"threadId": "th1", "turnId": "tu1", "startedAtMs": 1, "item": item})
	}
	completed := func(status string, finalText string) {
		items := []any{}
		if status == "completed" {
			items = append(items,
				map[string]any{"type": "agentMessage", "id": "m0", "text": "looking", "phase": "commentary"},
				map[string]any{"type": "agentMessage", "id": "m1", "text": finalText, "phase": "final_answer"})
		}
		note("turn/completed", map[string]any{"threadId": "th1", "turn": map[string]any{
			"id": "tu1", "items": items, "status": status, "error": nil,
		}})
	}
	usage := func(input, cached, output int) {
		note("thread/tokenUsage/updated", map[string]any{"threadId": "th1", "turnId": "tu1", "tokenUsage": map[string]any{
			"total": map[string]any{"inputTokens": input, "cachedInputTokens": cached, "outputTokens": output, "totalTokens": input + output},
			"last":  map[string]any{"inputTokens": input, "cachedInputTokens": cached, "outputTokens": output, "totalTokens": input + output},
		}})
	}
	toolCall := func(id int, q string) {
		itemStarted(map[string]any{"type": "dynamicToolCall", "id": fmt.Sprintf("call-%d", id), "tool": "echo", "status": "inProgress"})
		request(id, "item/tool/call", map[string]any{
			"threadId": "th1", "turnId": "tu1", "callId": fmt.Sprintf("call-%d", id),
			"tool": "echo", "arguments": map[string]any{"q": q},
		})
	}

	answers := map[string]fakeFinalTool{}
	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 1<<20), 1<<20)
	for scan.Scan() {
		line := scan.Bytes()
		if capture != nil {
			fmt.Fprintln(capture, string(line))
		}
		var in struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Model   string `json:"model"`
				Cursor  string `json:"cursor"`
				Type    string `json:"type"`
				APIKey  string `json:"apiKey"`
				LoginID string `json:"loginId"`
			} `json:"params"`
			Result *struct {
				Success      bool `json:"success"`
				ContentItems []struct {
					Text string `json:"text"`
				} `json:"contentItems"`
			} `json:"result"`
		}
		if json.Unmarshal(line, &in) != nil {
			continue
		}
		authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
		switch in.Method {
		case "initialize":
			reply(in.ID, map[string]any{})
		case "account/login/start":
			switch {
			case in.Params.Type == "apiKey" && behavior == fakeAPIKeyReject:
				say(msg(map[string]any{"jsonrpc": "2.0", "id": in.ID, "error": map[string]any{"code": -32600, "message": "invalid api key " + in.Params.APIKey}}))
			case in.Params.Type == "apiKey":
				_ = os.WriteFile(authPath, []byte(msg(map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": in.Params.APIKey})), 0o600)
				reply(in.ID, map[string]any{"type": "apiKey"})
				note("account/login/completed", map[string]any{"loginId": nil, "success": true, "error": nil})
			case in.Params.Type == "chatgptDeviceCode":
				reply(in.ID, map[string]any{"type": "chatgptDeviceCode", "loginId": "login-1",
					"verificationUrl": "https://auth.openai.com/codex/device", "userCode": "ABCD-1234"})
				switch behavior {
				case fakeDeviceLoginOK:
					time.Sleep(50 * time.Millisecond)
					_ = os.WriteFile(authPath, []byte(`{"auth_mode":"chatgpt","tokens":{}}`), 0o600)
					note("account/login/completed", map[string]any{"loginId": "login-1", "success": true, "error": nil})
				case fakeDeviceLoginLink:
					// The file swapped for a link between the guard and completion.
					time.Sleep(50 * time.Millisecond)
					_ = os.WriteFile(authPath+".elsewhere", []byte(`{"auth_mode":"chatgpt","tokens":{}}`), 0o600)
					_ = os.Symlink(authPath+".elsewhere", authPath)
					note("account/login/completed", map[string]any{"loginId": "login-1", "success": true, "error": nil})
				case fakeDeviceLoginFail:
					note("account/login/completed", map[string]any{"loginId": "login-1", "success": false, "error": "authorization denied"})
				case fakeDeviceLoginExit:
					return
				}
			}
		case "account/login/cancel":
			reply(in.ID, map[string]any{"status": "canceled"})
		case "account/logout":
			_ = os.Remove(authPath)
			reply(in.ID, map[string]any{})
			note("account/updated", map[string]any{"authMode": nil, "planType": nil})
		case "model/list":
			if behavior == fakeModelsError {
				say(msg(map[string]any{"jsonrpc": "2.0", "id": in.ID, "error": map[string]any{"code": -32000, "message": "catalog down"}}))
				continue
			}
			efforts := []any{
				map[string]any{"reasoningEffort": "medium", "description": "balanced"},
				map[string]any{"reasoningEffort": "high", "description": "deeper"},
			}
			model := func(id, name string, hidden, isDefault bool) map[string]any {
				return map[string]any{"id": id, "model": id, "displayName": name, "description": name + " model",
					"hidden": hidden, "isDefault": isDefault, "defaultReasoningEffort": "medium", "supportedReasoningEfforts": efforts}
			}
			if in.Params.Cursor == "" {
				reply(in.ID, map[string]any{"data": []any{model("gpt-5.6-terra", "GPT-5.6 Terra", false, true), model("gpt-secret", "Secret", true, false)}, "nextCursor": "p2"})
			} else {
				reply(in.ID, map[string]any{"data": []any{model("gpt-5.4-mini", "GPT-5.4 Mini", false, false)}, "nextCursor": nil})
			}
		case "thread/start":
			// Like the real server, a thread started with a model answers with it.
			model := "gpt-fake-default"
			if in.Params.Model != "" {
				model = in.Params.Model
			}
			reply(in.ID, map[string]any{"thread": map[string]any{"id": "th1"}, "model": model})
			note("thread/started", map[string]any{})
		case "turn/start":
			reply(in.ID, map[string]any{"turn": map[string]any{"id": "tu1", "status": "inProgress", "items": []any{}}})
			note("turn/started", map[string]any{})
			itemStarted(map[string]any{"type": "userMessage", "id": "u1"})
			switch behavior {
			case fakeToolsHappy:
				toolCall(0, "alpha")
				toolCall(1, "beta")
			case fakeToolsHang:
				toolCall(0, "alpha")
			case fakeToolsStall:
				toolCall(0, "alpha")
				// Sleep, not select{}: a goroutine-free block is a fatal
				// deadlock in Go, which would exit instead of stalling.
				time.Sleep(time.Hour)
			case fakeUnexpectedItem:
				itemStarted(map[string]any{"type": "mcpToolCall", "id": "mcp1", "server": "codex_apps", "tool": "google_drive.get_document_text"})
				toolCall(0, "alpha")
			case fakeSchemaGarbage:
				completed("completed", "this is not json {")
			case fakeApprovalRequest:
				request(0, "item/commandExecution/requestApproval", map[string]any{"threadId": "th1", "turnId": "tu1", "command": "cat auth.json"})
			case fakeBigStream:
				for {
					note("item/agentMessage/delta", map[string]any{"threadId": "th1", "turnId": "tu1", "itemId": "m1", "delta": strings.Repeat("x", 512)})
				}
			case fakeTokenBudget:
				usage(900, 0, 200)
			case fakeToolsFlood:
				for i := 0; i < 7; i++ {
					toolCall(i, "flood")
				}
			}
		case "turn/interrupt":
			reply(in.ID, map[string]any{})
			if behavior != fakeToolsHang {
				completed("interrupted", "")
			}
		case "":
			// A response to one of this server's requests.
			if in.Result == nil {
				if behavior == fakeApprovalRequest {
					completed("completed", "done")
				}
				continue
			}
			if behavior != fakeToolsHappy {
				continue
			}
			ans := fakeFinalTool{ID: string(in.ID), Success: in.Result.Success}
			if len(in.Result.ContentItems) > 0 {
				ans.Text = in.Result.ContentItems[0].Text
			}
			answers[ans.ID] = ans
			if len(answers) == 2 {
				usage(100, 40, 20)
				final, _ := json.Marshal([]fakeFinalTool{answers["0"], answers["1"]})
				completed("completed", string(final))
			}
		}
	}
}

// fakeServer points the package at this test binary and tells the child which
// behavior to play. It returns the path so tests that assert on the child's
// argv or environment can still reach it.
func fakeServer(t *testing.T, behavior string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	t.Setenv(fakeServerEnv, behavior)
	prev := lookPathFn
	lookPathFn = func(string) (string, error) { return self, nil }
	t.Cleanup(func() { lookPathFn = prev })
	// The runtime passes the child an allowlisted environment; the fake is
	// steered by CCTRACE_* variables, so those are let through in tests.
	prevPrefix := childEnvTestPrefix
	childEnvTestPrefix = "CCTRACE_"
	t.Cleanup(func() { childEnvTestPrefix = prevPrefix })
	clearCache()
	t.Cleanup(clearCache)
	return self
}
