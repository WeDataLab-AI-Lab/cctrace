package localapi

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListenerCloseRevokesKeepAliveConnection(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	s, err := New(Options{Address: listener.Addr().String(), Token: token, AllowedOrigins: []string{"http://localhost:3000"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, listener) }()
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	reader := bufio.NewReader(conn)
	send := func() error {
		_, err := fmt.Fprintf(conn, "GET /v1/health HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\n\r\n", listener.Addr().String(), token)
		return err
	}
	if err := send(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	listener.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Serve did not return")
	}
	if err := send(); err == nil {
		if resp, err := http.ReadResponse(reader, nil); err == nil {
			resp.Body.Close()
			t.Fatal("closed listener still authenticated existing connection")
		}
	}
}

func TestTokenHashAuthentication(t *testing.T) {
	t.Parallel()
	raw := make([]byte, 32)
	raw[0] = 7
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256(raw)
	opts := Options{Address: "127.0.0.1:0", AllowedOrigins: []string{"http://localhost:3000"}, TokenHash: hex.EncodeToString(sum[:])}
	for _, restart := range []bool{false, true} {
		s, err := New(opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			token  string
			status int
		}{{token, 200}, {"wrong", 401}, {"", 401}} {
			req := httptest.NewRequest("GET", "http://"+opts.Address+"/v1/health", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("restart=%v: status=%d want=%d", restart, rec.Code, tc.status)
			}
		}
	}
	for _, hash := range []string{"bad", hex.EncodeToString(make([]byte, 31))} {
		opts.TokenHash = hash
		if _, err := New(opts); err == nil {
			t.Fatal("invalid hash accepted")
		}
	}
}
