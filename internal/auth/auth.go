package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ErrReadOnlyToken distinguishes a valid read credential from an ingestion credential.
var ErrReadOnlyToken = errors.New("token was issued for read-only API access; run cctrace init to obtain a CLI ingestion token")

// TokenValidator checks whether a per-user API token is valid.
// Returns (true, nil) if the token is valid and the user is active.
type TokenValidator func(ctx context.Context, token string) (bool, error)

// Authenticator validates API keys for both gRPC and HTTP.
// Empty apiKey disables authentication (development mode).
type Authenticator struct {
	apiKey         string
	tokenValidator TokenValidator
}

// Option configures an Authenticator.
type Option func(*Authenticator)

// WithTokenValidator adds per-user token validation as a fallback after global API key check.
func WithTokenValidator(tv TokenValidator) Option {
	return func(a *Authenticator) {
		a.tokenValidator = tv
	}
}

func New(apiKey string, opts ...Option) *Authenticator {
	a := &Authenticator{apiKey: apiKey}
	for _, o := range opts {
		o(a)
	}
	return a
}

func (a *Authenticator) Enabled() bool {
	return a.apiKey != "" || a.tokenValidator != nil
}

// HTTPMiddleware protects REST API endpoints. Health endpoint is always public.
// Checks global API key first, then falls back to per-user token validation.
func (a *Authenticator) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Enabled() || r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}

		token := extractBearerToken(r.Header.Get("Authorization"))
		if a.apiKey != "" && token == a.apiKey {
			next.ServeHTTP(w, r)
			return
		}

		// Fallback: per-user API token
		if a.tokenValidator != nil && token != "" {
			ok, err := a.tokenValidator(r.Context(), token)
			if errors.Is(err, ErrReadOnlyToken) {
				log.Printf("[auth] ingestion denied: read-only token method=%s path=%s", r.Method, r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":"read_only_token","error":"` + ErrReadOnlyToken.Error() + `"}`))
				return
			}
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"authentication unavailable"}`))
				return
			}
			if ok {
				next.ServeHTTP(w, r)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	})
}

// GRPCUnaryInterceptor protects gRPC OTLP endpoints.
func (a *Authenticator) GRPCUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !a.Enabled() {
			return handler(ctx, req)
		}

		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}

		vals := md.Get("authorization")
		if len(vals) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization header")
		}

		token := extractBearerToken(vals[0])
		if a.apiKey != "" && token == a.apiKey {
			return handler(ctx, req)
		}

		// Fallback: per-user API token
		if a.tokenValidator != nil && token != "" {
			ok, err := a.tokenValidator(ctx, token)
			if errors.Is(err, ErrReadOnlyToken) {
				log.Printf("[auth] ingestion denied: read-only token method=%s", info.FullMethod)
				return nil, status.Error(codes.PermissionDenied, ErrReadOnlyToken.Error())
			}
			if err != nil {
				return nil, status.Error(codes.Unavailable, "token validation unavailable")
			}
			if ok {
				// The handler needs to know WHOSE token this was, not only that
				// it was valid. Without it the gRPC ingest path attributes data
				// purely from the payload, so one valid token can write
				// telemetry under any address (#535). The validator already
				// looked the user up; this carries the token forward so the
				// receiver can resolve it, rather than widening the validator's
				// contract for every caller.
				return handler(ContextWithIngestToken(ctx, token), req)
			}
		}

		return nil, status.Error(codes.Unauthenticated, "invalid api key")
	}
}

type ingestTokenKey struct{}

// ContextWithIngestToken carries a token the interceptor already verified.
// Only the interceptor should call it: a token in this context means
// authentication accepted it, and anything downstream may rely on that.
func ContextWithIngestToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, ingestTokenKey{}, token)
}

// IngestTokenFromContext returns the verified token, or "" when the request
// arrived without one -- an unauthenticated path, or the global API key, which
// names no user.
func IngestTokenFromContext(ctx context.Context) string {
	t, _ := ctx.Value(ingestTokenKey{}).(string)
	return t
}

func extractBearerToken(header string) string {
	if strings.HasPrefix(header, "Bearer ") {
		return strings.TrimPrefix(header, "Bearer ")
	}
	return header
}

// DashboardUser represents an authenticated dashboard user in request context.
type DashboardUser struct {
	ID                 int64
	Email              string
	Role               string // "admin" | "user"
	Name               string
	CctraceUserID      string
	MustChangePassword bool
}

type contextKey string

const userContextKey contextKey = "dashboard_user"

// UserFromContext extracts the authenticated dashboard user from context.
func UserFromContext(ctx context.Context) (*DashboardUser, bool) {
	u, ok := ctx.Value(userContextKey).(*DashboardUser)
	return u, ok
}

// WithUser adds a DashboardUser to the context.
func WithUser(ctx context.Context, u *DashboardUser) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}

type tokenAuthKey struct{}

// WithTokenAuth marks a request as signed in with the per-user API token
// rather than the dashboard cookie. Only DashboardMiddleware should call it.
func WithTokenAuth(ctx context.Context) context.Context {
	return context.WithValue(ctx, tokenAuthKey{}, true)
}

// IsTokenAuth reports whether the request's user came from the API token. A
// route whose answer must stay in the browser session refuses such requests.
func IsTokenAuth(ctx context.Context) bool {
	v, _ := ctx.Value(tokenAuthKey{}).(bool)
	return v
}

// DashboardTokenResolver turns a per-user API token into the account that holds it.
// Returning an error means "no such active token" — it is not propagated to the
// caller, so a lookup failure and an unknown token are answered identically.
type DashboardTokenResolver func(ctx context.Context, token string) (*DashboardUser, error)

// TokenRefusal is an error for a token that exists and is active but is not
// accepted for this kind of request. Unlike "no such token" it is worth telling
// the caller, because the fix is a different token, not a corrected one.
type TokenRefusal struct {
	Status  int
	Code    string
	Message string
}

func (e *TokenRefusal) Error() string { return e.Message }

// DashboardOption configures DashboardMiddleware.
type DashboardOption func(*dashboardConfig)

type dashboardConfig struct {
	resolveToken DashboardTokenResolver
}

// WithDashboardTokenResolver lets read requests authenticate with a per-user read
// token instead of the dashboard cookie.
//
// The CLI has no way to obtain a JWT, so `cctrace sessions` and `cctrace report`
// read with a token. Which tokens qualify is the resolver's decision; the server
// passes one that refuses the upload token (#702). A refusal returned as a
// *TokenRefusal is shown to a caller without a cookie.
//
// Whoever holds the token is the account it was issued to,
// and downstream access scoping applies exactly as it would after a browser login.
// CSRF is not weakened either: that defence exists because browsers attach cookies
// on their own, and a request that carries an explicit Authorization header was
// never the thing it guards against.
func WithDashboardTokenResolver(resolve DashboardTokenResolver) DashboardOption {
	return func(c *dashboardConfig) { c.resolveToken = resolve }
}

// dashboardTokenReadable reports whether a request may authenticate with a per-user
// token. Reads only: the token means "read this person's data", and the holder
// never agreed to it also being able to create or modify accounts. Widening
// that silently is how a credential ends up with authority nobody granted it.
func dashboardTokenReadable(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// DashboardMiddleware validates JWT cookies and injects the user into context.
func DashboardMiddleware(jwtMgr *JWTManager, opts ...DashboardOption) func(http.Handler) http.Handler {
	var cfg dashboardConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if jwtMgr == nil {
				http.Error(w, `{"error":"dashboard authentication unavailable"}`, http.StatusServiceUnavailable)
				return
			}

			// Tried before the cookie so a CLI request is never asked for one it
			// cannot have.
			var refusal *TokenRefusal
			if cfg.resolveToken != nil && dashboardTokenReadable(r.Method) {
				if token := extractBearerToken(r.Header.Get("Authorization")); token != "" {
					user, err := cfg.resolveToken(r.Context(), token)
					if err == nil && user != nil {
						next.ServeHTTP(w, r.WithContext(WithTokenAuth(WithUser(r.Context(), user))))
						return
					}
					errors.As(err, &refusal)
				}
			}

			cookie, err := r.Cookie("cctrace_token")
			if err != nil || cookie.Value == "" {
				w.Header().Set("Content-Type", "application/json")
				// A caller with no cookie is a CLI or script: tell it which token
				// to use instead, as the Open API does.
				if refusal != nil {
					w.WriteHeader(refusal.Status)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": refusal.Message, "code": refusal.Code})
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}

			claims, err := jwtMgr.ValidateToken(cookie.Value)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid or expired token"}`))
				return
			}

			// Reject refresh tokens used as access tokens
			if claims.Type == "refresh" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid token type"}`))
				return
			}

			user := &DashboardUser{
				ID:                 claims.UserID,
				Email:              claims.Subject,
				Role:               claims.Role,
				Name:               claims.Name,
				CctraceUserID:      claims.CctraceUserID,
				MustChangePassword: claims.MustChangePassword,
			}

			ctx := WithUser(r.Context(), user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
