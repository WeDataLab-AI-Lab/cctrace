package sessionrecord

import (
	"context"
	"io"
	"net/http"
	"sync"
)

type RequestAuthorizer func(context.Context, *http.Request) (release func(), err error)

type requestAuthorizerKey struct{}

func WithRequestAuthorizer(ctx context.Context, authorize RequestAuthorizer) context.Context {
	return context.WithValue(ctx, requestAuthorizerKey{}, authorize)
}

// GatedRoundTripper holds a caller-provided authorization lease only until the
// request body has been consumed by the underlying transport. The provider's
// response wait happens after the lease is released.
type GatedRoundTripper struct {
	Base http.RoundTripper
}

func (t GatedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	authorize, _ := req.Context().Value(requestAuthorizerKey{}).(RequestAuthorizer)
	if authorize == nil {
		return base.RoundTrip(req)
	}
	release, err := authorize(req.Context(), req)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return base.RoundTrip(req)
	}
	if req.Body == nil || req.Body == http.NoBody {
		defer release()
		return base.RoundTrip(req)
	}
	req.Body = &releaseBody{ReadCloser: req.Body, release: release}
	resp, err := base.RoundTrip(req)
	if err != nil {
		req.Body.Close()
	}
	return resp, err
}

type releaseBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *releaseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.release)
	}
	return n, err
}

func (b *releaseBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}
