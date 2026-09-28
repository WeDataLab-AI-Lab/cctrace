package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cctrace/internal/auth"
)

var tzRegexp = regexp.MustCompile(`^[A-Za-z/_]+$`)

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[api] json encode error: %v", err)
	}
}

func writeErr(w http.ResponseWriter, err error) {
	log.Printf("[api] error: %v", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

// queryCSV reads a comma-separated query param into a slice, dropping empty entries.
func queryCSV(r *http.Request, key string) []string {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func pathInt64(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	v := r.PathValue(key)
	if v == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": key + " required"})
		return 0, false
	}
	i, err := strconv.ParseInt(v, 10, 64)
	if err != nil || i <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid " + key})
		return 0, false
	}
	return i, true
}

func dashboardAuthor(r *http.Request) (email, userID string) {
	if user, ok := auth.UserFromContext(r.Context()); ok {
		return user.Email, user.CctraceUserID
	}
	return "", ""
}

func parseTimeRange(r *http.Request) (time.Time, time.Time) {
	since := time.Now().AddDate(0, 0, -7) // default: last 7 days
	until := time.Now()

	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t
		}
	}
	if v := r.URL.Query().Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			until = t
		}
	}
	return since, until
}

// lenientQueryTime parses one RFC3339 parameter, returning nil for absent or
// malformed input rather than an error -- the same contract
// lenientQueryTimeRange has, for callers that need a single bound.
func lenientQueryTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &parsed
}

func lenientQueryTimeRange(r *http.Request) (since, until *time.Time) {
	if value := r.URL.Query().Get("since"); value != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			since = &parsed
		}
	}
	if value := r.URL.Query().Get("until"); value != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			until = &parsed
		}
	}
	return since, until
}

func optionalQueryTime(r *http.Request, key string) (*time.Time, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: must be RFC3339", key)
	}
	return &t, nil
}

func optionalQueryTimeRange(r *http.Request) (since, until *time.Time, err error) {
	since, err = optionalQueryTime(r, "since")
	if err != nil {
		return nil, nil, err
	}
	until, err = optionalQueryTime(r, "until")
	if err != nil {
		return nil, nil, err
	}
	return since, until, nil
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		w.Header().Add("Vary", "Origin")
		if s.isAllowedOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) isAllowedOrigin(origin string) bool {
	if origin == "" || origin == "null" || origin == "*" {
		return false
	}
	for _, allowed := range s.allowedOrigins {
		if origin == strings.TrimSpace(allowed) {
			return true
		}
	}
	return false
}

// Limits are wire-byte contracts, not maxima of all scanner-accepted inputs.
// Bulk budgets cover the measured 200-record / 5000-sample fixtures; 8 MiB also
// fits a 1 MiB rule after control-character escaping. Quota is a single snapshot.
// The measured long-string fixture uses about 5.4x allocation after rounding.
// Eight 8 MiB requests of that shape use about 346 MiB; neither amplification
// for other JSON shapes nor concurrency is bounded here.
const (
	maxRequestBodyBytes             = 1 << 20
	maxSyncRequestBodyBytes         = 8 << 20
	maxQuotaRequestBodyBytes        = 64 << 10
	maxQuotaSamplesRequestBodyBytes = 4 << 20
	maxProjectRulesRequestBodyBytes = 8 << 20
	maxExclusionQueryBodyBytes      = 64 << 10
)

// MaxConfigurableSyncBodyBytes bounds what WithSyncBodyLimit will honour. The
// body is buffered whole and decoding roughly quintuples it, with no
// concurrency bound on this route, so an extra digit would be an
// out-of-memory switch rather than a looser limit. 256 MiB is well above the
// 33.9 MiB worst batch measured on the largest deployment. Exported so the
// daemon can name the same number when it warns about a rejected value.
const MaxConfigurableSyncBodyBytes = 256 << 20

// headroomWarnFraction is where an accepted body stops being routine and starts
// being a forecast. Three quarters, not half: at the default limit a batch can
// legitimately reach half, and a warning that fires on normal traffic is one
// nobody reads.
const headroomWarnFraction = 0.75

// nearBodyLimit reports whether an accepted body was close enough to the limit
// that the deployment is about to outgrow it.
//
// The limit is otherwise a cliff -- under it everything works, over it
// collection stops -- so the first signal an operator gets is the failure. A
// body that is refused is not a headroom problem: that is the limit doing its
// job, and a 413 already says so.
func nearBodyLimit(size, limit int64) bool {
	if limit <= 0 || size > limit {
		return false
	}
	return float64(size) >= float64(limit)*headroomWarnFraction
}

// withRequestBodyLimit follows the OTLP receiver's read-before-decode pattern.
// Reading to EOF is necessary: a decoder can accept a small JSON value without
// reading an oversized suffix, and Content-Length may be absent or untrusted.
//
// Untrusted in one direction only. A declared length below the limit proves
// nothing, so it can never grant acceptance -- the count below is what decides.
// A declared length above the limit is different: the body cannot turn out to be
// smaller than the sender says, so refusing on it is sound, and it is refused
// before a byte is read. That matters for a client too old to have learned to
// split its batches: it resends the same oversized body every sync interval, and
// without this the server accepts, buffers, and discards the limit's worth each
// time. Measured before this check: a 65 MiB request was uploaded in full and
// then refused.
func withRequestBodyLimit(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > limit {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		defer r.Body.Close()
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			} else {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
			}
			return
		}
		if nearBodyLimit(int64(len(body)), limit) {
			log.Printf("[api] %s body %d bytes is %.0f%% of the %d limit; raise CCTRACE_MAX_SYNC_BODY_BYTES before it is exceeded",
				r.URL.Path, len(body), float64(len(body))/float64(limit)*100, limit)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
