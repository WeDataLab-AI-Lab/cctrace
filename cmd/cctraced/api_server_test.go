package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

const aliceToken = "tok-alice"

// tokenStore answers a token lookup the way Postgres does: alice's token names
// her account, and any other -- the global API key among them -- no one. It
// keeps what session observation ingest hands it.
type tokenStore struct {
	store.Store
	lookups int
	batches []store.SessionObservationBatch
}

func (s *tokenStore) GetDashboardUserByApiToken(_ context.Context, token string) (*store.DashboardUser, error) {
	s.lookups++
	if token != aliceToken {
		return nil, pgx.ErrNoRows
	}
	return &store.DashboardUser{Email: "alice@example.com", CctraceUserID: "u-alice", IsActive: true}, nil
}

func (s *tokenStore) IngestSessionObservations(_ context.Context, b store.SessionObservationBatch) (store.SessionObservationResult, error) {
	s.batches = append(s.batches, b)
	return store.SessionObservationResult{}, nil
}

// The session observation route binds rows to the account of the per-user token
// the ingest middleware accepted, and answers 503 when it cannot resolve one.
// It learns the token from the middleware, which alone knows the global API key:
// a global-key request keeps the payload's identity without a lookup -- looked
// up, the key would belong to no one and draw a 503 -- and a per-user token's
// request is written under the token's account, whatever the payload names.
// Built here as cctraced builds it: the real authenticator, with the global key
// and a token validator, in front of newAPIServer.
func TestNewAPIServer_bindsObservationsToTheAcceptedToken(t *testing.T) {
	const key = "global-key"
	for _, tc := range []struct {
		name        string
		token       string
		wantLookups int
		wantProfile string
		wantUser    string
	}{
		{"global key", key, 0, "p@example.com", "u-1"},
		{"per-user token", aliceToken, 1, "alice@example.com", "u-alice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &tokenStore{}
			validator := func(_ context.Context, token string) (bool, error) { return token == aliceToken, nil }
			srv := newAPIServer(st, nil, auth.New(key, auth.WithTokenValidator(validator)))

			req := httptest.NewRequest(http.MethodPost, "/api/session-observations",
				strings.NewReader(`{"profile_email":"p@example.com","user_id":"u-1","heartbeat_at":"2026-10-05T09:00:00Z"}`))
			req.Header.Set("Authorization", "Bearer "+tc.token)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			if st.lookups != tc.wantLookups {
				t.Errorf("token lookups = %d, want %d", st.lookups, tc.wantLookups)
			}
			if len(st.batches) != 1 || st.batches[0].Heartbeat == nil ||
				st.batches[0].Heartbeat.ProfileEmail != tc.wantProfile || st.batches[0].Heartbeat.UserID != tc.wantUser {
				t.Errorf("batches = %+v, want one heartbeat under %s/%s", st.batches, tc.wantProfile, tc.wantUser)
			}
		})
	}
}
