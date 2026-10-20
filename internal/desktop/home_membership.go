package desktop

import (
	"encoding/json"
	"fmt"
	"slices"

	"cctrace/internal/profile"
)

// Membership moves preserve transmission, quota preference and both profile
// execution flags. An allowed home can become eligible when its explicitly
// confirmed destination is collecting; unknown homes cannot be enrolled here.
func (s *Service) previewMembership(raw json.RawMessage) (any, *ErrorObject) {
	var p struct {
		Provider       string `json:"provider"`
		Home           string `json:"home"`
		Source         string `json:"source"`
		SourceRevision string `json:"source_revision"`
		Target         string `json:"target"`
		TargetRevision string `json:"target_revision"`
	}
	if decodePayload(raw, &p) != nil || p.Source == p.Target || p.SourceRevision == "" || p.TargetRevision == "" {
		return nil, payloadError()
	}
	from, err := s.registry.Load(p.Source)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	to, err := s.registry.Load(p.Target)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	if from.Revision != p.SourceRevision || to.Revision != p.TargetRevision || from.Profile.HomesVersion != 1 || to.Profile.HomesVersion != 1 {
		return nil, sharedProfileError(profile.ErrConflict)
	}
	source := slices.Clone(from.Profile.Homes)
	target := slices.Clone(to.Profile.Homes)
	at := -1
	for i, h := range source {
		if h.Provider == p.Provider && h.Home == p.Home {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, accountError("account_mismatch")
	}
	for _, h := range target {
		if h.Provider == p.Provider && h.Home == p.Home {
			return nil, sharedProfileError(profile.ErrExists)
		}
	}
	member := source[at]
	enabled := to.Profile.CollectionEnabled(p.Provider)
	if enabled && (!to.ConfigurationComplete || to.Profile.RegistrationOnly) {
		return nil, &ErrorObject{Code: "server_setup_required", Message: "Configure the target collection destination before moving an active home."}
	}
	target = append(target, member)
	source = slices.Delete(source, at, at+1)
	return s.storeConsentPlan(consentPlan{kind: "membership", Provider: p.Provider, Home: p.Home, homes: []string{p.Home}, Profiles: []profile.HomeUpdate{{Name: from.Name, Revision: from.Revision, Homes: source}, {Name: to.Name, Revision: to.Revision, Homes: target}}, Effect: fmt.Sprintf("Move this enrolled home from %s to %s. Transmission=%s; source collection=%t; destination collection=%t. Profile execution flags and usage preference stay unchanged. An allowed home becomes eligible when its configured destination is collecting.", from.Name, to.Name, member.Transmission, from.Profile.CollectionEnabled(p.Provider), enabled)})
}
