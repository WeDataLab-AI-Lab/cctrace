package desktop

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"time"

	"cctrace/internal/profile"
)

type homeDiscoveryCandidate struct {
	CandidateRef string `json:"candidate_ref"`
	Provider     string `json:"provider"`
	Name         string `json:"name"`
	Home         string `json:"home"`
	Excluded     bool   `json:"excluded"`
}
type homeDiscoveryResult struct {
	ExclusionsSupported bool                         `json:"exclusions_supported"`
	ObservedAt          string                       `json:"observed_at"`
	Complete            bool                         `json:"complete"`
	Candidates          []homeDiscoveryCandidate     `json:"candidates"`
	Errors              []profile.HomeDiscoveryError `json:"errors"`
}

func (s *Service) discoverHomes() (any, *ErrorObject) {
	if s.registry == nil {
		return nil, &ErrorObject{Code: "profile_registry_unavailable", Message: "Shared profile storage is unavailable"}
	}
	home := s.homeDiscoveryRoot
	if home == "" {
		if s.fixtureRoot != "" {
			return nil, sharedProfileError(profile.ErrUnsafe)
		}
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, &ErrorObject{Code: "discovery_failed", Message: "Provider homes could not be discovered"}
		}
	}
	found, err := s.registry.DiscoverHomes(home)
	if err != nil {
		return nil, &ErrorObject{Code: "discovery_failed", Message: "Provider homes could not be discovered"}
	}
	result := homeDiscoveryResult{ExclusionsSupported: true, ObservedAt: s.now().UTC().Format(time.RFC3339Nano), Complete: found.Complete, Candidates: []homeDiscoveryCandidate{}, Errors: found.Errors}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]profile.HomeCandidate, len(found.Candidates))
	for _, candidate := range found.Candidates {
		ref := ""
		for previousRef, previous := range s.homeCandidates {
			if previous.Provider == candidate.Provider && previous.Name == candidate.Name && previous.Home == candidate.Home && previous.SameDirectory(candidate) {
				ref = previousRef
				break
			}
		}
		if ref == "" {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				return nil, &ErrorObject{Code: "discovery_failed", Message: "Provider homes could not be discovered"}
			}
			ref = hex.EncodeToString(random[:])
		}
		next[ref] = candidate
		result.Candidates = append(result.Candidates, homeDiscoveryCandidate{CandidateRef: ref, Provider: candidate.Provider, Name: candidate.Name, Home: candidate.Home, Excluded: candidate.Excluded})
	}
	s.homeCandidates = next
	return result, nil
}

func (s *Service) registerDiscoveredHome(raw json.RawMessage) (any, *ErrorObject) {
	var payload struct {
		CandidateRef      string `json:"candidate_ref"`
		CollectionEnabled *bool  `json:"collection_enabled"`
	}
	if decodePayload(raw, &payload) != nil || !accountOpaque.MatchString(payload.CandidateRef) || payload.CollectionEnabled == nil || *payload.CollectionEnabled {
		return nil, payloadError()
	}
	if s.registry == nil {
		return nil, &ErrorObject{Code: "profile_registry_unavailable", Message: "Shared profile storage is unavailable"}
	}
	s.mu.Lock()
	candidate, ok := s.homeCandidates[payload.CandidateRef]
	s.mu.Unlock()
	if !ok {
		return nil, &ErrorObject{Code: "candidate_changed", Message: "Discover homes again and select the current candidate"}
	}
	if _, err := s.registry.RegisterHomeCandidate(candidate, false); err != nil {
		code := "registration_failed"
		if errors.Is(err, profile.ErrConflict) || errors.Is(err, profile.ErrExists) {
			code = "candidate_changed"
		}
		return nil, &ErrorObject{Code: code, Message: "The selected home could not be registered; discover homes again"}
	}
	s.mu.Lock()
	delete(s.homeCandidates, payload.CandidateRef)
	s.mu.Unlock()
	return s.discoverHomes()
}

func (s *Service) excludeDiscoveredHome(raw json.RawMessage) (any, *ErrorObject) {
	var payload struct {
		CandidateRef string `json:"candidate_ref"`
		Excluded     *bool  `json:"excluded"`
	}
	if decodePayload(raw, &payload) != nil || !accountOpaque.MatchString(payload.CandidateRef) || payload.Excluded == nil {
		return nil, payloadError()
	}
	if s.registry == nil {
		return nil, &ErrorObject{Code: "profile_registry_unavailable", Message: "Shared profile storage is unavailable"}
	}
	s.mu.Lock()
	candidate, ok := s.homeCandidates[payload.CandidateRef]
	s.mu.Unlock()
	if !ok {
		return nil, &ErrorObject{Code: "candidate_changed", Message: "Discover homes again and select the current candidate"}
	}
	if err := s.registry.SetHomeExcluded(candidate, *payload.Excluded); err != nil {
		code := "exclusion_failed"
		if errors.Is(err, profile.ErrConflict) {
			code = "candidate_changed"
		}
		return nil, &ErrorObject{Code: code, Message: "The home choice could not be saved; discover homes again"}
	}
	return s.discoverHomes()
}
