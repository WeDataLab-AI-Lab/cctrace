package store

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
)

type turnContextVersion struct {
	Revision, Epoch                 int64
	Mapping, State, Coverage, Agent string
}

func (v turnContextVersion) state() string {
	if v.Coverage == "truncated" || v.State == "truncated" || (v.Agent != "" && v.Agent != "claude" && v.Agent != "codex") {
		return "unavailable"
	}
	if v.State == "observed" || v.State == "undeterminable" {
		return "ready"
	}
	return "pending"
}

func (s *PgStore) turnContextMAC(purpose string, body []byte) ([]byte, error) {
	s.turnContextKeyOnce.Do(func() { _, s.turnContextKeyErr = rand.Read(s.turnContextKey[:]) })
	if s.turnContextKeyErr != nil {
		return nil, s.turnContextKeyErr
	}
	h := hmac.New(sha256.New, s.turnContextKey[:])
	h.Write([]byte(purpose + "\x00"))
	h.Write(body)
	return h.Sum(nil), nil
}

// Analysis and context share a session revision. The selected turn is fenced
// separately by the cursor and by the client cache key.
func (s *PgStore) turnContextRevision(id string, a SessionAnalysisAccess, v turnContextVersion) (string, error) {
	body, err := json.Marshal(struct {
		Session, Scope string
		Version        turnContextVersion
	}{id, traceScope(a), v})
	if err != nil {
		return "", err
	}
	mac, err := s.turnContextMAC("turn-context-revision-v1", body)
	return base64.RawURLEncoding.EncodeToString(mac), err
}

type turnContextCursor struct {
	Revision, Turn string
	Key            SessionTimelineKey
}

func (s *PgStore) encodeTurnContextCursor(c turnContextCursor) (string, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	mac, err := s.turnContextMAC("turn-context-cursor-v1", body)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

func (s *PgStore) decodeTurnContextCursor(raw, revision, turn string) (turnContextCursor, error) {
	var c turnContextCursor
	if len(raw) > 4096 {
		return c, ErrTurnContextInvalid
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return c, ErrTurnContextInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return c, ErrTurnContextInvalid
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, ErrTurnContextInvalid
	}
	expected, err := s.turnContextMAC("turn-context-cursor-v1", body)
	if err != nil {
		return c, err
	}
	if !hmac.Equal(mac, expected) || json.Unmarshal(body, &c) != nil || c.Key.Timestamp.IsZero() || c.Key.RecordID <= 0 {
		return c, ErrTurnContextInvalid
	}
	if c.Revision != revision || c.Turn != turn {
		return c, ErrTurnContextRevision
	}
	return c, nil
}
