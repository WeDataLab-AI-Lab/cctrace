package profile

import (
	"bytes"
	"encoding/json"
	"io"
)

// DecodeHomeDecisions is intentionally stricter than stored legacy JSON: every
// consent field is mandatory and duplicate, missing, null or unknown fields are
// rejected before a noninteractive request can acquire a default false value.
func DecodeHomeDecisions(data []byte) ([]HomeMembership, error) {
	if len(data) > 1<<20 {
		return nil, ErrUnsafe
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('[') {
		return nil, ErrUnsafe
	}
	result := []HomeMembership{}
	for dec.More() {
		token, err = dec.Token()
		if err != nil || token != json.Delim('{') {
			return nil, ErrUnsafe
		}
		seen := map[string]bool{}
		var h HomeMembership
		for dec.More() {
			token, err = dec.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] {
				return nil, ErrUnsafe
			}
			seen[key] = true
			var raw json.RawMessage
			if dec.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return nil, ErrUnsafe
			}
			switch key {
			case "provider":
				err = json.Unmarshal(raw, &h.Provider)
			case "home":
				err = json.Unmarshal(raw, &h.Home)
			case "transmission":
				err = json.Unmarshal(raw, &h.Transmission)
			case "quota_enabled":
				err = json.Unmarshal(raw, &h.QuotaEnabled)
			default:
				return nil, ErrUnsafe
			}
			if err != nil {
				return nil, ErrUnsafe
			}
		}
		token, err = dec.Token()
		if err != nil || token != json.Delim('}') || len(seen) != 4 || !seen["provider"] || !seen["home"] || !seen["transmission"] || !seen["quota_enabled"] {
			return nil, ErrUnsafe
		}
		result = append(result, h)
		if len(result) > 256 {
			return nil, ErrUnsafe
		}
	}
	token, err = dec.Token()
	if err != nil || token != json.Delim(']') {
		return nil, ErrUnsafe
	}
	if _, err = dec.Token(); err != io.EOF {
		return nil, ErrUnsafe
	}
	if (&Profile{HomesVersion: 1, Homes: result}).ValidateHomes() != nil {
		return nil, ErrUnsafe
	}
	return result, nil
}
