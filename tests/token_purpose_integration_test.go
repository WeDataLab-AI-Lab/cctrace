package tests

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/api"
	"cctrace/internal/auth"
	"cctrace/internal/emailalias"
	"cctrace/internal/otelrecv"
	"cctrace/internal/store"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestTokenPurposeIngestionBoundaries(t *testing.T) {
	s := acquireTokenStore(t)
	truncateTokenTables(t, s)
	ctx := context.Background()
	user, err := s.CreateDashboardUser(ctx, &store.DashboardUser{Email: "purpose@example.com", PasswordHash: "hash", Role: "user", Name: "Purpose", CctraceUserID: "purpose"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDashboardUserApiToken(ctx, user.ID, "cct_cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDashboardUserAPIToken(ctx, user.ID, "Read", "cct_web", "web", nil); err != nil {
		t.Fatal(err)
	}
	validator := func(ctx context.Context, token string) (bool, error) {
		u, err := s.GetDashboardUserByIngestionToken(ctx, token)
		if errors.Is(err, store.ErrTokenNotIngestion) {
			return false, auth.ErrReadOnlyToken
		}
		if err != nil {
			return false, err
		}
		return u.IsActive, nil
	}
	a := auth.New("", auth.WithTokenValidator(validator))
	jwt, _ := auth.NewJWTManager("test-jwt-secret-that-is-at-least-32-bytes-long")
	syncHandler := api.NewServer(s, jwt, a.HTTPMiddleware).Handler()
	metrics := otelrecv.NewMetricsReceiver(nil, nil, emailalias.Aliases{})
	otlpHandler := a.HTTPMiddleware(otelrecv.NewHTTPReceiver(nil, metrics).Handler())
	lis := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer(grpc.UnaryInterceptor(a.GRPCUnaryInterceptor()))
	colmetricspb.RegisterMetricsServiceServer(gs, metrics)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, token := range []string{"cct_web", "cct_cli"} {
		t.Run(token, func(t *testing.T) {
			want := http.StatusOK
			wantGRPC := codes.OK
			if token == "cct_web" {
				want = http.StatusForbidden
				wantGRPC = codes.PermissionDenied
			}
			for _, route := range []struct {
				name, path, body string
				handler          http.Handler
			}{
				{"sync", "/api/sync", `{"profile_email":"purpose@example.com","user_id":"purpose","project_hash":"purpose","records":[]}`, syncHandler},
				{"otlp_http", "/v1/metrics", `{}`, otlpHandler},
			} {
				t.Run(route.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(route.body))
					req.Header.Set("Authorization", "Bearer "+token)
					req.Header.Set("Content-Type", "application/json")
					rec := httptest.NewRecorder()
					route.handler.ServeHTTP(rec, req)
					if rec.Code != want {
						t.Errorf("status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
					}
					if want == http.StatusForbidden && !strings.Contains(rec.Body.String(), "read-only") {
						t.Errorf("missing purpose diagnosis: %s", rec.Body.String())
					}
				})
			}
			t.Run("otlp_grpc", func(t *testing.T) {
				_, err := colmetricspb.NewMetricsServiceClient(conn).Export(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), &colmetricspb.ExportMetricsServiceRequest{})
				if status.Code(err) != wantGRPC {
					t.Errorf("code=%s want=%s error=%v", status.Code(err), wantGRPC, err)
				}
			})
		})
	}
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/events", nil)
	req.Header.Set("Authorization", "Bearer cct_web")
	rec := httptest.NewRecorder()
	syncHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("web read status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// CLI read tokens carry created_via cli_read: they read the Open API like a
// Settings token, never ingest, and stop reading the moment their owner is an
// administrator, since an administrator's token reads every user's data.
func TestCLIReadTokenPurpose(t *testing.T) {
	s := acquireTokenStore(t)
	truncateTokenTables(t, s)
	ctx := context.Background()
	user, err := s.CreateDashboardUser(ctx, &store.DashboardUser{Email: "cliread@example.com", PasswordHash: "hash", Role: "user", Name: "CLI Read", CctraceUserID: "cliread"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDashboardUserAPIToken(ctx, user.ID, "CLI read token (laptop)", "cct_cliread_a", "cli_read", nil); err != nil {
		t.Fatalf("migration must allow cli_read: %v", err)
	}
	if _, err := s.CreateDashboardUserAPIToken(ctx, user.ID, "CLI read token (laptop)", "cct_settings_same_name", "web", nil); err != nil {
		t.Fatal(err)
	}

	if u, err := s.GetDashboardUserByOpenAPIToken(ctx, "cct_cliread_a"); err != nil || u.ID != user.ID {
		t.Fatalf("regular user's CLI read token: %v %v", u, err)
	}
	if _, err := s.GetDashboardUserByIngestionToken(ctx, "cct_cliread_a"); !errors.Is(err, store.ErrTokenNotIngestion) {
		t.Fatalf("ingestion accepted a CLI read token: %v", err)
	}

	// Replacement deletes only the held CLI read token.
	if ok, err := s.DeleteCLIReadToken(ctx, user.ID, "cct_settings_same_name"); err != nil || ok {
		t.Fatalf("deleted a Settings token through the CLI replace path: %v %v", ok, err)
	}
	if ok, err := s.DeleteCLIReadToken(ctx, user.ID+1, "cct_cliread_a"); err != nil || ok {
		t.Fatalf("deleted another user's token: %v %v", ok, err)
	}

	admin := "admin"
	if err := s.UpdateDashboardUser(ctx, user.ID, store.UpdateDashboardUserParams{Role: &admin}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDashboardUserByOpenAPIToken(ctx, "cct_cliread_a"); !errors.Is(err, store.ErrCLIReadTokenAdmin) {
		t.Fatalf("promoted owner's CLI read token: %v, want ErrCLIReadTokenAdmin", err)
	}
	if u, err := s.GetDashboardUserByOpenAPIToken(ctx, "cct_settings_same_name"); err != nil || u.Role != "admin" {
		t.Fatalf("a Settings token stays usable for an administrator: %v %v", u, err)
	}

	if ok, err := s.DeleteCLIReadToken(ctx, user.ID, "cct_cliread_a"); err != nil || !ok {
		t.Fatalf("held CLI read token not deleted: %v %v", ok, err)
	}
	if _, err := s.GetDashboardUserByOpenAPIToken(ctx, "cct_cliread_a"); err == nil {
		t.Fatal("deleted token still authenticates")
	}
}
