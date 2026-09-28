package syncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/store"
)

func TestClientJSONPreservesHTMLCharacters(t *testing.T) {
	const value = "<>&\"\\\x01"
	for _, path := range []string{"/api/sync", "/api/quota", "/api/quota-samples", "/api/project-rules"} {
		t.Run(path, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if !strings.Contains(string(body), "<>&") {
					t.Errorf("HTML characters expanded on wire: %s", body)
				}
				var got string
				switch path {
				case "/api/sync":
					var p SyncPayload
					err = json.Unmarshal(body, &p)
					got = p.ProfileEmail
				case "/api/quota":
					var p store.QuotaSnapshot
					err = json.Unmarshal(body, &p)
					got = p.ProfileEmail
				case "/api/quota-samples":
					var p QuotaSamplesPayload
					err = json.Unmarshal(body, &p)
					if len(p.Samples) > 0 {
						got = p.Samples[0].AccountID
					}
				case "/api/project-rules":
					var p store.ProjectRuleIngestRequest
					err = json.Unmarshal(body, &p)
					if len(p.Rules) > 0 {
						got = p.Rules[0].Content
					}
				}
				if err != nil || got != value {
					t.Errorf("decoded value=%q err=%v", got, err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			c := NewClient(srv.URL, "", "")
			var err error
			switch path {
			case "/api/sync":
				_, err = c.Send(context.Background(), "claude", value, "u", "p", "", ProjectIdentity{}, nil)
			case "/api/quota":
				err = c.SendQuota(context.Background(), &QuotaPayload{ProfileEmail: value})
			case "/api/quota-samples":
				err = c.SendQuotaSamples(context.Background(), []*store.QuotaSample{{AccountID: value}})
			case "/api/project-rules":
				_, err = c.SendProjectRules(context.Background(), &store.ProjectRuleIngestRequest{Rules: []*store.ProjectRuleSnapshot{{Content: value}}})
			}
			if err != nil || requests != 1 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}
