package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// countingBody reports how much of an oversized body the server actually read.
type countingBody struct {
	remaining int
	read      int
}

func (b *countingBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > b.remaining {
		n = b.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = 'a'
	}
	b.remaining -= n
	b.read += n
	return n, nil
}

func (b *countingBody) Close() error { return nil }

// A client that cannot be updated keeps resending the batch the server refuses,
// once per sync interval. Reading it to the limit each time turns one stuck
// client into sustained upload the server pays for and then throws away. The
// declared length is enough to refuse: a body cannot be smaller than what the
// sender says it is, so trusting it to reject costs nothing even though trusting
// it to accept would.
func TestRequestBodyLimitRefusesOnDeclaredLength(t *testing.T) {
	const limit = 1 << 20
	body := &countingBody{remaining: 8 << 20}

	req := httptest.NewRequest(http.MethodPost, "/api/sync", body)
	req.ContentLength = int64(body.remaining)
	req.Header.Set("Content-Length", strconv.Itoa(body.remaining))
	rr := httptest.NewRecorder()

	reached := false
	withRequestBodyLimit(limit, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rr.Code)
	}
	if reached {
		t.Error("handler ran for a body the limit refuses")
	}
	if !strings.Contains(rr.Body.String(), "request body too large") {
		t.Errorf("unclear error: %s", rr.Body.String())
	}
	if body.read != 0 {
		t.Errorf("read %d bytes of a body whose declared length already exceeds the limit", body.read)
	}
}

// An absent or wrong Content-Length still has to be caught by counting, which is
// why the declared-length check can only reject and never accept.
func TestRequestBodyLimitStillCountsWhenLengthIsUnknown(t *testing.T) {
	const limit = 1 << 20
	req := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(strings.Repeat("a", 2<<20)))
	req.ContentLength = -1
	rr := httptest.NewRecorder()

	reached := false
	withRequestBodyLimit(limit, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge || reached {
		t.Fatalf("status = %d reached = %v, want 413 and no handler", rr.Code, reached)
	}
}
