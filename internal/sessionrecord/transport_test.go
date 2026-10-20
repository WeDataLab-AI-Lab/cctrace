package sessionrecord

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type recordRoundTripper func(*http.Request) (*http.Response, error)

func (f recordRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransmissionAuthorizerReleasesAfterRequestBodyIsSent(t *testing.T) {
	var released atomic.Bool
	var calls atomic.Int32
	base := recordRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if string(body) != "synthetic request" {
			t.Fatalf("request body = %q", body)
		}
		if !released.Load() {
			return nil, errors.New("authorization gate remained held after request transmission")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	})
	transport := GatedRoundTripper{Base: base}
	ctx := WithRequestAuthorizer(context.Background(), func(context.Context, *http.Request) (func(), error) {
		return func() { released.Store(true) }, nil
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local.test", strings.NewReader("synthetic request"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls.Load() != 1 || !released.Load() {
		t.Fatalf("calls=%d released=%v", calls.Load(), released.Load())
	}
}

func TestTransmissionAuthorizerCanRefuseBeforeNetwork(t *testing.T) {
	want := errors.New("consent withdrawn")
	var calls atomic.Int32
	transport := GatedRoundTripper{Base: recordRoundTripper(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, nil
	})}
	ctx := WithRequestAuthorizer(context.Background(), func(context.Context, *http.Request) (func(), error) {
		return nil, want
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local.test", strings.NewReader("synthetic request"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); !errors.Is(err, want) {
		t.Fatalf("RoundTrip error = %v, want %v", err, want)
	}
	if calls.Load() != 0 {
		t.Fatalf("network transport called %d times", calls.Load())
	}
}
