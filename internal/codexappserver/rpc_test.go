package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"cctrace/internal/airuntime"
)

// pipeServer is the far end of a conn: it reads what the client wrote and
// writes what the client will read.
type pipeServer struct {
	in  *bufio.Reader
	out io.WriteCloser
}

func newPipeConn(t *testing.T, maxLine, maxStream int64) (*conn, *pipeServer) {
	t.Helper()
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	c := newConn(cw, cr, maxLine, maxStream)
	t.Cleanup(func() {
		c.Close()
		_ = sw.Close()
		_ = sr.Close()
	})
	return c, &pipeServer{in: bufio.NewReader(sr), out: sw}
}

func (s *pipeServer) read(t *testing.T) rpcMessage {
	t.Helper()
	line, err := s.in.ReadBytes('\n')
	if err != nil {
		t.Fatalf("server read: %v", err)
	}
	var m rpcMessage
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("client wrote non-JSON %q: %v", line, err)
	}
	return m
}

func (s *pipeServer) say(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(s.out, line+"\n"); err != nil {
		t.Fatalf("server write: %v", err)
	}
}

func nextIncoming(t *testing.T, c *conn) (rpcMessage, bool) {
	t.Helper()
	select {
	case m, ok := <-c.Incoming():
		return m, ok
	case <-time.After(5 * time.Second):
		t.Fatal("no incoming message within 5s")
		return rpcMessage{}, false
	}
}

// The server numbers its requests from 0 and so does the client. A server
// request with the client's pending id must go to Incoming, not answer Call.
func TestConnSeparatesServerRequestFromReplyWithSameID(t *testing.T) {
	c, srv := newPipeConn(t, 1<<20, 1<<20)

	type reply struct {
		raw json.RawMessage
		err error
	}
	done := make(chan reply, 1)
	go func() {
		raw, err := c.Call(context.Background(), "initialize", map[string]any{})
		done <- reply{raw, err}
	}()

	req := srv.read(t)
	if string(req.ID) != "0" || req.Method != "initialize" {
		t.Fatalf("client request = %+v, want id 0 initialize", req)
	}
	srv.say(t, `{"jsonrpc":"2.0","id":0,"method":"item/tool/call","params":{"tool":"x"}}`)
	srv.say(t, `{"jsonrpc":"2.0","id":0,"result":{"ok":true}}`)

	r := <-done
	if r.err != nil || string(r.raw) != `{"ok":true}` {
		t.Fatalf("Call = %s, %v; want the reply", r.raw, r.err)
	}
	m, ok := nextIncoming(t, c)
	if !ok || m.Method != "item/tool/call" || string(m.ID) != "0" {
		t.Fatalf("incoming = %+v, want the server request", m)
	}
}

// Notifications nobody is reading yet must not hold a reply back: the client
// makes its setup calls before it starts consuming the event stream.
func TestConnDeliversReplyPastUnreadNotifications(t *testing.T) {
	c, srv := newPipeConn(t, 1<<20, 1<<20)
	done := make(chan error, 1)
	go func() {
		_, err := c.Call(context.Background(), "thread/start", nil)
		done <- err
	}()
	srv.read(t)
	for i := 0; i < 200; i++ {
		srv.say(t, `{"jsonrpc":"2.0","method":"mcpServer/startupStatus/updated","params":{}}`)
	}
	srv.say(t, `{"jsonrpc":"2.0","id":0,"result":{}}`)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reply blocked behind unread notifications")
	}
}

func TestConnReturnsJSONRPCError(t *testing.T) {
	c, srv := newPipeConn(t, 1<<20, 1<<20)
	done := make(chan error, 1)
	go func() {
		_, err := c.Call(context.Background(), "thread/start", nil)
		done <- err
	}()
	srv.read(t)
	srv.say(t, `{"jsonrpc":"2.0","id":0,"error":{"code":-32600,"message":"requires experimentalApi"}}`)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "requires experimentalApi") {
		t.Fatalf("err = %v, want the server's message", err)
	}
}

func TestConnRespondWritesResultAndError(t *testing.T) {
	c, srv := newPipeConn(t, 1<<20, 1<<20)
	go func() {
		_ = c.Respond(json.RawMessage(`0`), map[string]any{"success": true})
		_ = c.RespondError(json.RawMessage(`"a"`), -32601, "unsupported")
	}()
	ok := srv.read(t)
	if string(ok.ID) != "0" || ok.Method != "" || !strings.Contains(string(ok.Result), `"success":true`) {
		t.Fatalf("respond = %+v", ok)
	}
	bad := srv.read(t)
	if string(bad.ID) != `"a"` || bad.Error == nil || bad.Error.Code != -32601 {
		t.Fatalf("respond error = %+v", bad)
	}
}

func TestConnFailsOnUnparseableLine(t *testing.T) {
	c, srv := newPipeConn(t, 1<<20, 1<<20)
	done := make(chan error, 1)
	go func() {
		_, err := c.Call(context.Background(), "initialize", nil)
		done <- err
	}()
	srv.read(t)
	srv.say(t, "this is not json")
	if err := <-done; !errors.Is(err, airuntime.ErrProtocol) {
		t.Fatalf("Call err = %v, want ErrProtocol", err)
	}
	if _, ok := nextIncoming(t, c); ok {
		t.Fatal("Incoming should close after a protocol error")
	}
	if !errors.Is(c.Err(), airuntime.ErrProtocol) {
		t.Fatalf("Err = %v, want ErrProtocol", c.Err())
	}
}

// A server that writes its last message without a newline and exits must not
// lose it: that message is often turn/completed.
func TestConnDeliversFinalLineWithoutNewline(t *testing.T) {
	c, srv := newPipeConn(t, 1<<20, 1<<20)
	go func() {
		_, _ = io.WriteString(srv.out, `{"jsonrpc":"2.0","method":"turn/completed","params":{}}`)
		_ = srv.out.Close()
	}()
	m, ok := nextIncoming(t, c)
	if !ok || m.Method != "turn/completed" {
		t.Fatalf("incoming = %+v, %v; want the unterminated last line", m, ok)
	}
	if _, ok := nextIncoming(t, c); ok {
		t.Fatal("Incoming should close at EOF")
	}
	if !errors.Is(c.Err(), errServerExited) {
		t.Fatalf("Err = %v, want errServerExited", c.Err())
	}
}

// One line past the per-line cap ends the stream instead of being buffered.
func TestConnBoundsLineLength(t *testing.T) {
	c, srv := newPipeConn(t, 128, 1<<20)
	go func() {
		_, _ = io.WriteString(srv.out, `{"jsonrpc":"2.0","method":"x","params":{"pad":"`+strings.Repeat("a", 4096)+`"}}`+"\n")
	}()
	if _, ok := nextIncoming(t, c); ok {
		t.Fatal("oversized line was delivered")
	}
	if !errors.Is(c.Err(), errOutputLimit) {
		t.Fatalf("Err = %v, want errOutputLimit", c.Err())
	}
}

// Many lines that are each small still hit the cumulative cap: a long turn's
// total is bounded, not only its largest message.
func TestConnBoundsCumulativeBytes(t *testing.T) {
	c, srv := newPipeConn(t, 1<<20, 4096)
	go func() {
		for i := 0; i < 1000; i++ {
			if _, err := io.WriteString(srv.out, fmt.Sprintf(`{"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"%d"}}`+"\n", i)); err != nil {
				return
			}
		}
	}()
	n := 0
	for {
		_, ok := nextIncoming(t, c)
		if !ok {
			break
		}
		n++
	}
	if n == 0 || n >= 1000 {
		t.Fatalf("delivered %d messages, want some but not all", n)
	}
	if !errors.Is(c.Err(), errOutputLimit) {
		t.Fatalf("Err = %v, want errOutputLimit", c.Err())
	}
}
