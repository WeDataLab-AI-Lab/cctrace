package codexappserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"cctrace/internal/airuntime"
)

// A turn is two-way JSON-RPC: the client calls the server, and the server calls
// the client back (item/tool/call, approvals) while notifications stream in
// between. Both sides number their requests from 0, so an id alone does not say
// which side a message belongs to. The line shape does:
//
//	method + id  server request   -> Incoming
//	id only      reply to a Call  -> the waiting Call
//	method only  notification     -> Incoming

var (
	// errOutputLimit ends the stream when one line or the turn's running total
	// passes its byte cap. The caps replace Fetch's single 8 MiB budget, which
	// a turn of several minutes would exhaust legitimately.
	errOutputLimit = errors.New("codex app-server output exceeded its byte limit")
	// errServerExited is a clean EOF: the child closed stdout.
	errServerExited = errors.New("codex app-server exited")
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type conn struct {
	w   io.Writer
	wmu sync.Mutex

	mu      sync.Mutex
	cond    *sync.Cond
	nextID  int64
	pending map[int64]chan rpcMessage
	// queue is unbounded so the reader never waits on the consumer: a Call's
	// reply must get through while notifications sit unread. The byte caps
	// bound it.
	queue   []rpcMessage
	eof     bool
	stopped bool
	err     error

	incoming chan rpcMessage
	stop     chan struct{} // closed by Close
	done     chan struct{} // closed when the reader ends
}

func newConn(w io.Writer, r io.Reader, maxLine, maxStream int64) *conn {
	c := &conn{
		w:        w,
		pending:  map[int64]chan rpcMessage{},
		incoming: make(chan rpcMessage),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	c.cond = sync.NewCond(&c.mu)
	go c.read(r, maxLine, maxStream)
	go c.pump()
	return c
}

// Incoming yields server requests and notifications in arrival order. It is
// closed once the stream has ended and everything queued was delivered.
func (c *conn) Incoming() <-chan rpcMessage { return c.incoming }

// Err is why the stream ended, nil while it is open.
func (c *conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close stops delivery. The reader ends when the child's stdout closes.
func (c *conn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.stopped {
		c.stopped = true
		close(c.stop)
		c.cond.Broadcast()
	}
}

func (c *conn) read(r io.Reader, maxLine, maxStream int64) {
	br := bufio.NewReaderSize(r, 64<<10)
	var total int64
	var err error
	for {
		// A last line without a newline arrives together with EOF; it is
		// handled before the error ends the loop.
		line, readErr := readLine(br, maxLine)
		if len(line) > 0 {
			if total += int64(len(line)); total > maxStream {
				err = fmt.Errorf("%w: more than %d bytes in one turn", errOutputLimit, maxStream)
				break
			}
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				var m rpcMessage
				if jsonErr := json.Unmarshal(trimmed, &m); jsonErr != nil {
					err = fmt.Errorf("%w: unparseable line from codex app-server: %v", airuntime.ErrProtocol, jsonErr)
					break
				}
				c.dispatch(m)
			}
		}
		if readErr != nil {
			err = readErr
			break
		}
	}
	if errors.Is(err, io.EOF) {
		err = errServerExited
	}
	c.mu.Lock()
	c.eof = true
	c.err = err
	c.cond.Broadcast()
	c.mu.Unlock()
	close(c.done)
}

// readLine reads one newline-terminated line without holding more than max
// bytes of it.
func readLine(br *bufio.Reader, max int64) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if int64(len(line)+len(chunk)) > max {
			return nil, fmt.Errorf("%w: line longer than %d bytes", errOutputLimit, max)
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

func (c *conn) dispatch(m rpcMessage) {
	if m.Method == "" {
		id, err := strconv.ParseInt(string(m.ID), 10, 64)
		if err != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
		return
	}
	c.mu.Lock()
	c.queue = append(c.queue, m)
	c.cond.Signal()
	c.mu.Unlock()
}

func (c *conn) pump() {
	defer close(c.incoming)
	for {
		c.mu.Lock()
		for len(c.queue) == 0 && !c.eof && !c.stopped {
			c.cond.Wait()
		}
		if c.stopped || len(c.queue) == 0 {
			c.mu.Unlock()
			return
		}
		m := c.queue[0]
		c.queue[0] = rpcMessage{}
		c.queue = c.queue[1:]
		c.mu.Unlock()
		select {
		case c.incoming <- m:
		case <-c.stop:
			return
		}
	}
}

// Call sends a request and waits for its reply.
func (c *conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ch := make(chan rpcMessage, 1)
	c.mu.Lock()
	if c.eof {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	id := c.nextID
	c.nextID++
	c.pending[id] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}

	if err := c.write(request(id, method, params)); err != nil {
		forget()
		return nil, err
	}
	select {
	case m := <-ch:
		return replyResult(m)
	case <-c.done:
		select {
		case m := <-ch:
			return replyResult(m)
		default:
		}
		return nil, c.Err()
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

// Send issues a request whose reply nobody waits for; it is dropped on arrival.
func (c *conn) Send(method string, params any) error {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()
	return c.write(request(id, method, params))
}

func (c *conn) Notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	return c.write(msg)
}

// Respond answers a server request, echoing its id verbatim.
func (c *conn) Respond(id json.RawMessage, result any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *conn) RespondError(id json.RawMessage, code int, message string) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{Code: code, Message: message}})
}

func request(id int64, method string, params any) map[string]any {
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	return msg
}

func replyResult(m rpcMessage) (json.RawMessage, error) {
	if m.Error != nil {
		return nil, fmt.Errorf("codex app-server error %d: %s", m.Error.Code, m.Error.Message)
	}
	return m.Result, nil
}

func (c *conn) write(msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write to codex app-server: %w", err)
	}
	return nil
}
