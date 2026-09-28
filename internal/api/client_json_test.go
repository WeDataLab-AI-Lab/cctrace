package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

type jsonWireStore struct {
	store.Store
	value string
}

func (m *jsonWireStore) InsertSessionRecords(_ context.Context, records []*store.SessionRecord) error {
	m.value = records[0].ProfileEmail
	return nil
}
func (m *jsonWireStore) UpsertQuotaSnapshot(_ context.Context, q *store.QuotaSnapshot) error {
	m.value = q.ProfileEmail
	return nil
}
func (m *jsonWireStore) InsertQuotaSamples(_ context.Context, samples []*store.QuotaSample) (int, error) {
	m.value = samples[0].AccountID
	return len(samples), nil
}
func (m *jsonWireStore) IngestProjectRules(_ context.Context, req *store.ProjectRuleIngestRequest) (*store.ProjectRuleIngestResponse, error) {
	m.value = req.Rules[0].Content
	return &store.ProjectRuleIngestResponse{}, nil
}

func TestClientJSONRoundTripThroughHandlers(t *testing.T) {
	const value = "<>&\"\\\x01"
	m := &jsonWireStore{Store: &mockStore{}}
	srv := httptest.NewServer(newTestServer(m, nil).Handler())
	defer srv.Close()
	client := syncer.NewClient(srv.URL, "", "")
	for _, tc := range []struct {
		name string
		send func() error
	}{
		{"sync", func() error {
			_, err := client.Send(context.Background(), "claude", value, "u", "p", "", syncer.ProjectIdentity{}, []*store.SessionRecord{{SessionID: "json-wire"}})
			return err
		}},
		{"quota", func() error { return client.SendQuota(context.Background(), &syncer.QuotaPayload{ProfileEmail: value}) }},
		{"quota-samples", func() error {
			return client.SendQuotaSamples(context.Background(), []*store.QuotaSample{{AccountID: value}})
		}},
		{"project-rules", func() error {
			_, err := client.SendProjectRules(context.Background(), &store.ProjectRuleIngestRequest{Rules: []*store.ProjectRuleSnapshot{{Content: value}}})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.value = ""
			if err := tc.send(); err != nil {
				t.Fatal(err)
			}
			if m.value != value {
				t.Fatalf("stored=%q want=%q", m.value, value)
			}
		})
	}
}
