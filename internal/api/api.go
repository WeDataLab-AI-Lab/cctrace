package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/auth"
	"cctrace/internal/emailalias"
	"cctrace/internal/ingestblock"
	"cctrace/internal/store"
)

// HealthExtraFunc optionally provides extra fields for the health response.
type HealthExtraFunc func(ctx context.Context) (map[string]interface{}, error)

// Server provides REST API endpoints for querying OTEL data.
type Server struct {
	setupToken             string
	store                  store.Store
	mux                    *http.ServeMux
	authMW                 AuthMiddleware
	healthExtra            HealthExtraFunc
	aliases                emailalias.Resolver
	jwtMgr                 *auth.JWTManager
	dashboardMW            AuthMiddleware
	useUserIDAccessControl bool
	cookieSecure           bool
	version                string
	dataPath               string
	allowedOrigins         []string
	// ingestBlock is consulted on the ingest path to refuse sessions the dashboard
	// deleted and projects it blocked. Nil in tests that never exercise it, so every
	// read is nil-guarded.
	ingestBlock *ingestblock.Cache
	// ingestBlockLoad reloads ingestBlock from the database. Exclusion changes
	// call it so ingest follows at once, links included (#715).
	ingestBlockLoad ingestblock.Loader
	// syncBodyLimit overrides maxSyncRequestBodyBytes for /api/sync. Zero means
	// the compiled default; it is read per request because routes() registers
	// handlers inside newServer, before any builder can run.
	syncBodyLimit int64
	// aiReports runs weekly AI reports. Nil answers every AI report route with
	// 503 runtime_unconfigured.
	aiReports *aireport.Service
}

// WithSyncBodyLimit raises the /api/sync request body ceiling for one deployment,
// up to MaxConfigurableSyncBodyBytes. Only this route is configurable: the other
// limits have measured headroom on real traffic. The value is stored as given and
// validated in one place below, so there is a single answer to "what does an
// unusable limit mean" no matter which caller supplied it.
func (s *Server) WithSyncBodyLimit(limit int64) *Server {
	s.syncBodyLimit = limit
	return s
}

// effectiveSyncBodyLimit is the sole enforcement point: anything not positive,
// or above MaxConfigurableSyncBodyBytes, falls back to the compiled ceiling, so
// a misconfigured limit fails closed rather than open to none.
func (s *Server) effectiveSyncBodyLimit() int64 {
	if s.syncBodyLimit > 0 && s.syncBodyLimit <= MaxConfigurableSyncBodyBytes {
		return s.syncBodyLimit
	}
	return maxSyncRequestBodyBytes
}

// syncReadDeadline is how long the server will spend reading one /api/sync body,
// sized to the limit it is willing to accept rather than fixed.
//
// The listener's ReadTimeout is a constant, so a raised body limit was an
// acceptance the server would not wait for: the operator allows 64 MiB and the
// read still ends at 30 s, and the client sees a dropped connection instead of a
// verdict. The client sizes its own wait the same way -- see
// internal/syncer/transferDeadline -- and both assume the same floor link speed
// of 1 MiB/s. That floor is an assumption, not a measurement; a slower link
// still fails, only later.
//
// The exposure this opens is stated by two numbers rather than left open: a
// sender holds one connection for at most this long having sent at most the
// limit, and MaxConfigurableSyncBodyBytes caps the limit.
func syncReadDeadline(limit int64) time.Duration {
	if limit < 0 {
		limit = 0
	}
	const floorBytesPerSecond = 1 << 20
	return 30*time.Second + time.Duration(limit/floorBytesPerSecond)*time.Second
}

// syncWriteDeadline bounds the whole exchange.
//
// That is what Go's WriteTimeout actually measures. net/http arms it when the
// request *header* has been read (server.go, readRequest defers
// SetWriteDeadline), so the one clock covers reading the body, doing the work,
// and writing the reply. Raising only the read deadline therefore bought
// nothing past 30 s: production ran POST /api/sync 500 at 30,000 ms between 90
// and 1,146 times a day while CCTRACE_MAX_SYNC_BODY_BYTES said 192 MiB and
// syncReadDeadline said 222 s (#621).
//
// It is the read budget plus the same allowance again for the insert, on the
// same assumed 1 MiB/s floor -- a floor, not a measurement: production inserts
// a 200-record batch in single-digit milliseconds, so this is the shape of a
// bound rather than a forecast.
//
// Extended, not cleared. downloadHandler clears its write deadline because a
// release binary is a fixed, known object; an upload is attacker-shaped, so it
// gets a longer bound rather than none.
func syncWriteDeadline(limit int64) time.Duration {
	if limit < 0 {
		limit = 0
	}
	const floorBytesPerSecond = 1 << 20
	return syncReadDeadline(limit) + time.Duration(limit/floorBytesPerSecond)*time.Second
}

// withSyncBodyLimit resolves the ceiling at request time so a builder that runs
// after newServer still governs the already-registered handler.
func (s *Server) withSyncBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := s.effectiveSyncBodyLimit()
		// Both, because they bound different things and only one of them was
		// raised. The read deadline governs the body transfer; WriteTimeout
		// governs the whole exchange and stayed at the 30 s the server sets for
		// every route.
		rc := http.NewResponseController(w)
		now := time.Now()
		_ = rc.SetReadDeadline(now.Add(syncReadDeadline(limit)))
		_ = rc.SetWriteDeadline(now.Add(syncWriteDeadline(limit)))
		withRequestBodyLimit(limit, next).ServeHTTP(w, r)
	})
}

// WithIngestBlocklist attaches the shared blocklist cache. The same cache is given
// to the OTLP receivers in cmd/cctraced, so both ingest paths refuse the same set.
func (s *Server) WithIngestBlocklist(c *ingestblock.Cache) *Server {
	s.ingestBlock = c
	return s
}

// WithIngestBlocklistLoader lets exclusion changes reload the blocklist from the
// database instead of waiting for the periodic refresh (#715).
func (s *Server) WithIngestBlocklistLoader(l ingestblock.Loader) *Server {
	s.ingestBlockLoad = l
	return s
}

// reloadIngestBlocklist makes ingest follow an exclusion change that just
// committed. Excluding an address also excludes the billing accounts linked to
// it, which only the store knows, so the cache is reloaded rather than patched.
// A failed reload is logged: the periodic refresh catches up within 30 seconds.
func (s *Server) reloadIngestBlocklist(ctx context.Context) {
	if s.ingestBlock == nil || s.ingestBlockLoad == nil {
		return
	}
	if err := s.ingestBlock.Refresh(ctx, s.ingestBlockLoad); err != nil {
		log.Printf("[ingestblock] reload after exclusion change: %v", err)
	}
}

// AuthMiddleware is a function that wraps an http.Handler with authentication.
type AuthMiddleware func(http.Handler) http.Handler

func NewServer(s store.Store, jwtMgr *auth.JWTManager, authMW ...AuthMiddleware) *Server {
	// Read endpoints also accept a per-user read token, so the CLI can reach them.
	// The rule is the Open API's (#702): tokens made for reading (web, cli_read),
	// never the upload token `cctrace init` keeps in plaintext, and no cli_read
	// token of an administrator. Writes stay cookie-only — see
	// WithDashboardTokenResolver for why the grant stops at reads.
	resolve := func(ctx context.Context, token string) (*auth.DashboardUser, error) {
		user, err := s.GetDashboardUserByOpenAPIToken(ctx, token)
		if refusal := readTokenRefusal(err); refusal != nil {
			return nil, refusal
		}
		if err != nil {
			return nil, err
		}
		if user == nil || !user.IsActive {
			return nil, errInactiveToken
		}
		return &auth.DashboardUser{
			ID:            user.ID,
			Email:         user.Email,
			Role:          user.Role,
			Name:          user.Name,
			CctraceUserID: user.CctraceUserID,
		}, nil
	}
	return newServer(s, jwtMgr, auth.DashboardMiddleware(jwtMgr, auth.WithDashboardTokenResolver(resolve)), authMW...)
}

// errInactiveToken keeps a deactivated account's token from behaving like a valid
// one. The middleware treats any error as "no such token", so this never reaches a
// response body.
var errInactiveToken = errors.New("token belongs to no active user")

func newServer(s store.Store, jwtMgr *auth.JWTManager, dashboardMW AuthMiddleware, authMW ...AuthMiddleware) *Server {
	srv := &Server{
		store:                  s,
		mux:                    http.NewServeMux(),
		aliases:                emailalias.Aliases{},
		jwtMgr:                 jwtMgr,
		dashboardMW:            dashboardMW,
		useUserIDAccessControl: os.Getenv("CCTRACE_USERID_ACCESS_CONTROL") != "false",
	}
	if len(authMW) > 0 {
		srv.authMW = authMW[0]
	}
	srv.routes()
	return srv
}

// WithCookieSecure forces Secure authentication cookies behind HTTPS proxies.
func (s *Server) WithCookieSecure(secure bool) *Server {
	s.cookieSecure = secure
	return s
}

// WithAliases sets email alias resolution for the server.
func (s *Server) WithAliases(a emailalias.Resolver) *Server {
	s.aliases = a
	return s
}

// WithHealthExtra registers a function that adds extra fields to GET /api/health.
func (s *Server) WithHealthExtra(fn HealthExtraFunc) *Server {
	s.healthExtra = fn
	return s
}

// WithVersion sets the server version string reported by GET /api/version.
func (s *Server) WithVersion(v string) *Server {
	s.version = v
	return s
}

// WithDataPath sets the filesystem path used to report data-volume free space
// on the admin storage endpoint. Empty disables the volume panel.
func (s *Server) WithDataPath(p string) *Server {
	s.dataPath = p
	return s
}

func (s *Server) Handler() http.Handler {
	return s.withCORS(s.mux)
}

// WithAllowedOrigins sets the exact origins permitted to use credentialed CORS.
// Empty entries and the wildcard never grant access.
func (s *Server) WithAllowedOrigins(origins []string) *Server {
	s.allowedOrigins = append([]string(nil), origins...)
	return s
}

// WithSetupToken enables initial setup only for requests carrying this token.
func (s *Server) WithSetupToken(token string) *Server {
	s.setupToken = token
	return s
}
