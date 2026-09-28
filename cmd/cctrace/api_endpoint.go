package main

import (
	"strings"

	"cctrace/internal/profile"
)

func profileHTTPAPIEndpoint(p *profile.Profile) string {
	if p == nil {
		return ""
	}
	endpoint := p.Server.SyncEndpoint
	if endpoint == "" {
		endpoint = p.Server.Endpoint
	}
	return strings.TrimRight(endpoint, "/")
}

// openAPIToken picks the token for /api/open/v1 reads: the dedicated read token
// when one is stored, otherwise auth_token, where a dashboard-created read token
// had to be placed before read_token existed.
func openAPIToken(p *profile.Profile) string {
	if p.Server.ReadToken != "" {
		return p.Server.ReadToken
	}
	return p.Server.AuthToken
}
