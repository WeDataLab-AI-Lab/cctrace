package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// dummyHandler is the next handler for HTTP middleware tests.
var dummyHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
})

func TestAuthenticator_Enabled(t *testing.T) {
	if a := New("secret"); !a.Enabled() {
		t.Fatal("expected Enabled()=true for non-empty key")
	}
	if a := New(""); a.Enabled() {
		t.Fatal("expected Enabled()=false for empty key")
	}
}

func TestHTTPMiddleware_ValidToken(t *testing.T) {
	a := New("my-key")
	handler := a.HTTPMiddleware(dummyHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/traces", nil)
	req.Header.Set("Authorization", "Bearer my-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHTTPMiddleware_InvalidToken(t *testing.T) {
	a := New("my-key")
	handler := a.HTTPMiddleware(dummyHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/traces", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHTTPMiddleware_TokenValidatorFailureIsUnavailable(t *testing.T) {
	a := New("", WithTokenValidator(func(context.Context, string) (bool, error) {
		return false, errors.New("database unavailable")
	}))
	handler := a.HTTPMiddleware(dummyHandler)
	req := httptest.NewRequest(http.MethodPost, "/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer per-user-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 so the telemetry client retries", rec.Code)
	}
}

func TestHTTPMiddleware_MissingHeader(t *testing.T) {
	a := New("my-key")
	handler := a.HTTPMiddleware(dummyHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/traces", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHTTPMiddleware_HealthPublic(t *testing.T) {
	a := New("my-key")
	handler := a.HTTPMiddleware(dummyHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for health endpoint, got %d", rec.Code)
	}
}

func TestHTTPMiddleware_Disabled(t *testing.T) {
	a := New("")
	handler := a.HTTPMiddleware(dummyHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/traces", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when auth disabled, got %d", rec.Code)
	}
}

func TestDashboardMiddleware_MissingManagerFailsClosed(t *testing.T) {
	handler := DashboardMiddleware(nil)(dummyHandler)
	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when dashboard auth is not configured, got %d", rec.Code)
	}
}

// `cctrace sessions` and `cctrace report` ship with --limit/--user/--json flags and
// have never worked: they send the per-user token the profile holds, and the read
// API only accepted the dashboard JWT cookie, so every invocation returned 401. The
// ingest middleware a few lines up already falls back to that same token; the
// dashboard one did not.
//
// The fallback is deliberately narrow. A per-user token is issued to mean "collect
// this person's telemetry", and letting it create or modify accounts would widen
// what the holder consented to without anyone seeing the change. Reads only.
func TestDashboardMiddleware_PerUserTokenFallback(t *testing.T) {
	// Deliberately not shaped like a real token. The leak gate blocks the cct_ +
	// 16 chars pattern anywhere in the export, and it is right to: a
	// credential-shaped literal in shipped code is indistinguishable from a real
	// one at scan time. The fallback does not care what the string looks like.
	const token = "a-per-user-token"
	// A configured manager, because an unconfigured one must keep failing closed —
	// the last subtest pins that. The fallback is an extra way in for a server whose
	// dashboard auth works, not a way around one whose auth was never set up.
	jwtMgr, err := NewJWTManager("test-jwt-secret-that-is-at-least-32-bytes-long")
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	resolve := func(_ context.Context, got string) (*DashboardUser, error) {
		if got != token {
			return nil, errors.New("unknown token")
		}
		return &DashboardUser{ID: 7, Email: "alice@ex.com", Role: "user", CctraceUserID: "alice"}, nil
	}

	t.Run("GET is accepted and carries the user", func(t *testing.T) {
		var seen *DashboardUser
		h := DashboardMiddleware(jwtMgr, WithDashboardTokenResolver(resolve))(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen, _ = UserFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			}))

		req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		// The identity has to reach the handler: access scoping downstream reads it
		// to decide whose rows this request may see.
		if seen == nil || seen.CctraceUserID != "alice" || seen.Role != "user" {
			t.Fatalf("user in context = %+v, want alice/user", seen)
		}
	})

	t.Run("writes are refused", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			h := DashboardMiddleware(jwtMgr, WithDashboardTokenResolver(resolve))(dummyHandler)
			req := httptest.NewRequest(method, "/api/admin/users", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code == http.StatusOK {
				t.Errorf("%s was accepted on a per-user token; reads only", method)
			}
		}
	})

	t.Run("an unknown token is still rejected", func(t *testing.T) {
		h := DashboardMiddleware(jwtMgr, WithDashboardTokenResolver(resolve))(dummyHandler)
		req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
		req.Header.Set("Authorization", "Bearer a-different-token")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("without a resolver nothing changes", func(t *testing.T) {
		h := DashboardMiddleware(nil)(dummyHandler)
		req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		// nil manager still fails closed — the fallback must not become a way to
		// reach data on a server whose dashboard auth was never configured.
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})
}

// grpc helpers

func dummyUnaryHandler(_ context.Context, _ interface{}) (interface{}, error) {
	return "ok", nil
}

func ctxWithAuth(token string) context.Context {
	md := metadata.Pairs("authorization", "Bearer "+token)
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestGRPCInterceptor_ValidToken(t *testing.T) {
	a := New("grpc-key")
	interceptor := a.GRPCUnaryInterceptor()

	resp, err := interceptor(ctxWithAuth("grpc-key"), nil, &grpc.UnaryServerInfo{}, dummyUnaryHandler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected handler to be called, got %v", resp)
	}
}

func TestGRPCInterceptor_InvalidToken(t *testing.T) {
	a := New("grpc-key")
	interceptor := a.GRPCUnaryInterceptor()

	_, err := interceptor(ctxWithAuth("wrong-key"), nil, &grpc.UnaryServerInfo{}, dummyUnaryHandler)
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestGRPCInterceptor_TokenValidatorFailureIsUnavailable(t *testing.T) {
	a := New("", WithTokenValidator(func(context.Context, string) (bool, error) {
		return false, errors.New("database unavailable")
	}))
	interceptor := a.GRPCUnaryInterceptor()

	_, err := interceptor(ctxWithAuth("per-user-token"), nil, &grpc.UnaryServerInfo{}, dummyUnaryHandler)
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable so the telemetry client retries, got %v", err)
	}
}

func TestGRPCInterceptor_MissingMetadata(t *testing.T) {
	a := New("grpc-key")
	interceptor := a.GRPCUnaryInterceptor()

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, dummyUnaryHandler)
	if err == nil {
		t.Fatal("expected error for missing metadata")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestGRPCInterceptor_Disabled(t *testing.T) {
	a := New("")
	interceptor := a.GRPCUnaryInterceptor()

	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, dummyUnaryHandler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected handler to be called, got %v", resp)
	}
}

// A valid read credential is forbidden, not an authentication outage. Neither
// transport may invoke the collector after receiving this purpose error.
func TestReadOnlyTokenDeniedBeforeCollector(t *testing.T) {
	a := New("", WithTokenValidator(func(context.Context, string) (bool, error) {
		return false, ErrReadOnlyToken
	}))
	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	req := httptest.NewRequest(http.MethodPost, "/api/sync", nil)
	req.Header.Set("Authorization", "Bearer read-secret")
	rec := httptest.NewRecorder()
	a.HTTPMiddleware(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "read_only_token") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer read-secret"))
	_, err := a.GRPCUnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Export"}, func(context.Context, interface{}) (interface{}, error) {
		called = true
		return nil, nil
	})
	if status.Code(err) != codes.PermissionDenied || !strings.Contains(status.Convert(err).Message(), "read-only") {
		t.Fatalf("error=%v", err)
	}
	if called {
		t.Fatal("read token reached collector")
	}
}

// The interceptor is the only place that knows which token was accepted. If it
// does not carry that forward, the ingest handler downstream has nothing but the
// payload to attribute by, and one valid token can write telemetry under any
// address (#535).
func TestGRPCInterceptorCarriesTheVerifiedTokenForward(t *testing.T) {
	a := New("", WithTokenValidator(func(_ context.Context, token string) (bool, error) {
		return token == "good-token", nil
	}))

	var seen string
	_, err := a.GRPCUnaryInterceptor()(
		metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Bearer good-token")),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/Export"},
		func(ctx context.Context, _ any) (any, error) {
			seen = IngestTokenFromContext(ctx)
			return nil, nil
		})
	if err != nil {
		t.Fatalf("interceptor rejected a valid token: %v", err)
	}
	if seen != "good-token" {
		t.Errorf("handler saw token %q, want the one the interceptor verified", seen)
	}
}

// The global API key is shared and names no user, so substituting an identity
// from it would attribute everyone's data to whoever the key resolves to.
// Nothing to carry forward means the payload keeps deciding, which is correct
// for this path.
func TestGRPCInterceptorCarriesNothingForTheGlobalAPIKey(t *testing.T) {
	a := New("shared-key")

	var seen string
	_, err := a.GRPCUnaryInterceptor()(
		metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Bearer shared-key")),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/Export"},
		func(ctx context.Context, _ any) (any, error) {
			seen = IngestTokenFromContext(ctx)
			return nil, nil
		})
	if err != nil {
		t.Fatalf("interceptor rejected the api key: %v", err)
	}
	if seen != "" {
		t.Errorf("handler saw token %q, want none -- the api key names no user", seen)
	}
}

// A handler that must refuse the CLI token has to know which credential signed
// the request in: both paths put the same user in the context.
func TestDashboardMiddleware_RecordsTokenAuth(t *testing.T) {
	const token = "a-per-user-token"
	jwtMgr, err := NewJWTManager("test-jwt-secret-that-is-at-least-32-bytes-long")
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	resolve := func(_ context.Context, got string) (*DashboardUser, error) {
		if got != token {
			return nil, errors.New("unknown token")
		}
		return &DashboardUser{ID: 7, Email: "admin@ex.com", Role: "admin"}, nil
	}
	var viaToken, sawUser bool
	h := DashboardMiddleware(jwtMgr, WithDashboardTokenResolver(resolve))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, sawUser = UserFromContext(r.Context())
			viaToken = IsTokenAuth(r.Context())
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/admin/ai/account", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !sawUser || !viaToken {
		t.Fatalf("token request: user %v, token auth %v", sawUser, viaToken)
	}

	access, err := jwtMgr.GenerateAccessToken(7, "admin@ex.com", "admin", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/admin/ai/account", nil)
	req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: access})
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !sawUser || viaToken {
		t.Fatalf("cookie request: user %v, token auth %v", sawUser, viaToken)
	}
}

// A resolver refusal names a different token as the fix, so a CLI caller without
// a cookie hears it instead of a bare "unauthorized". A browser is unaffected: its
// cookie still signs it in even if a refused bearer token rode along.
func TestDashboardMiddleware_SurfacesTokenRefusal(t *testing.T) {
	jwtMgr, err := NewJWTManager("test-jwt-secret-that-is-at-least-32-bytes-long")
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(context.Context, string) (*DashboardUser, error) {
		return nil, &TokenRefusal{Status: http.StatusUnauthorized, Code: "ingestion_token", Message: "use a read token"}
	}
	h := DashboardMiddleware(jwtMgr, WithDashboardTokenResolver(resolve))(dummyHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	req.Header.Set("Authorization", "Bearer an-upload-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"code":"ingestion_token"`) ||
		!strings.Contains(rec.Body.String(), "use a read token") {
		t.Fatalf("bearer only: status=%d body=%s", rec.Code, rec.Body.String())
	}

	access, err := jwtMgr.GenerateAccessToken(7, "admin@ex.com", "admin", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	req.Header.Set("Authorization", "Bearer an-upload-token")
	req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: access})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie with refused bearer: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
