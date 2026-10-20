package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
)

const maxConcurrentQuotaRequests = 64

type quotaReadFlight struct {
	selection selectedQuotaHome
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	waiters   int
	result    quotaResult
	err       *ErrorObject
}

func (s *Service) readSelectedQuota(ctx context.Context, provider, ref string, generation uint64) (any, *ErrorObject) {
	if ctx == nil {
		ctx = context.Background()
	}
	selection, err := s.snapshotQuotaSelection(provider, ref, generation)
	if err != nil {
		return nil, err
	}
	if !selection.enabled {
		result := quotaUnavailable(provider, "provider_disabled", safeSelectionError("provider_disabled").Message)
		return selectedQuotaResponse(result, selection), nil
	}
	result, err := s.awaitSelectedQuota(ctx, selection)
	if err != nil {
		return nil, err
	}
	return selectedQuotaResponse(result, selection), nil
}

func (s *Service) awaitSelectedQuota(ctx context.Context, selection selectedQuotaHome) (quotaResult, *ErrorObject) {
	for {
		if err := ctx.Err(); err != nil {
			return quotaUnavailable(selection.provider, "request_cancelled", safeSelectionError("request_cancelled").Message), nil
		}
		s.quotaFlightMu.Lock()
		flight := s.quotaFlights[selection.provider]
		if flight != nil {
			select {
			case <-flight.done:
				if s.quotaFlights[selection.provider] == flight {
					delete(s.quotaFlights, selection.provider)
				}
				s.quotaFlightMu.Unlock()
				continue
			default:
			}
			if flight.selection.ref == selection.ref && flight.selection.gen == selection.gen && flight.ctx.Err() == nil {
				flight.waiters++
				s.quotaFlightMu.Unlock()
				return s.waitForQuotaFlight(ctx, flight)
			}
			done := flight.done
			s.quotaFlightMu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return quotaUnavailable(selection.provider, "request_cancelled", safeSelectionError("request_cancelled").Message), nil
			}
		}
		flightCtx, cancel := context.WithTimeout(context.Background(), providerReadTimeout)
		flight = &quotaReadFlight{
			selection: selection,
			ctx:       flightCtx,
			cancel:    cancel,
			done:      make(chan struct{}),
			waiters:   1,
		}
		s.quotaFlights[selection.provider] = flight
		s.quotaFlightMu.Unlock()
		go s.runSelectedQuotaFlight(flight)
		return s.waitForQuotaFlight(ctx, flight)
	}
}

func (s *Service) waitForQuotaFlight(ctx context.Context, flight *quotaReadFlight) (quotaResult, *ErrorObject) {
	select {
	case <-flight.done:
		return flight.result, flight.err
	case <-ctx.Done():
		s.quotaFlightMu.Lock()
		if flight.waiters > 0 {
			flight.waiters--
		}
		if flight.waiters == 0 {
			select {
			case <-flight.done:
			default:
				flight.cancel()
			}
		}
		s.quotaFlightMu.Unlock()
		return quotaUnavailable(flight.selection.provider, "request_cancelled", safeSelectionError("request_cancelled").Message), nil
	}
}

func (s *Service) runSelectedQuotaFlight(flight *quotaReadFlight) {
	selection := flight.selection
	var result quotaResult
	var responseError *ErrorObject
	if !s.currentQuotaSelectionMatches(selection.provider, selection.ref, selection.gen) {
		responseError = selectionChangedError()
	} else if !s.providerMode {
		if flight.ctx.Err() != nil {
			result = quotaUnavailable(selection.provider, "request_cancelled", safeSelectionError("request_cancelled").Message)
		} else {
			result = s.readFixtureQuota(selection.provider)
		}
		if flight.ctx.Err() != nil {
			result = quotaUnavailable(selection.provider, "request_cancelled", safeSelectionError("request_cancelled").Message)
		} else if !s.currentQuotaSelectionMatches(selection.provider, selection.ref, selection.gen) {
			responseError = selectionChangedError()
		}
	} else {
		cacheKey := selection.provider + "\x00" + selection.ref
		var identityChanged bool
		result, identityChanged = s.readProviderQuotaAtHome(
			flight.ctx,
			selection.provider,
			selection.home,
			cacheKey,
			func(identity string) bool { return s.observeSelectedQuotaIdentity(selection, identity) },
		)
		if identityChanged || !s.currentQuotaSelectionMatches(selection.provider, selection.ref, selection.gen) {
			responseError = selectionChangedError()
		} else if !s.selectedQuotaHomeStillValid(selection) {
			responseError = &ErrorObject{Code: "selection_unavailable", Message: "selected provider home is unavailable"}
		}
	}
	s.quotaFlightMu.Lock()
	flight.result = result
	flight.err = responseError
	close(flight.done)
	if s.quotaFlights[selection.provider] == flight {
		delete(s.quotaFlights, selection.provider)
	}
	flight.cancel()
	s.quotaFlightMu.Unlock()
}

func (s *Service) selectedQuotaHomeStillValid(selection selectedQuotaHome) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.registry != nil && s.refreshSharedSelectionLocked(selection.provider) != nil {
		return false
	}
	state := s.quotaSelections[selection.provider]
	if state == nil || state.SelectionRef != selection.ref || state.Generation != selection.gen {
		return false
	}
	home, err := s.resolveQuotaSelectionHomeLocked(selection.provider, state.Profile)
	return err == nil && home == selection.home
}

func (s *Service) quotaCancel(raw json.RawMessage) (any, *ErrorObject) {
	var fields map[string]json.RawMessage
	if err := decodePayload(raw, &fields); err != nil || len(fields) != 3 {
		return nil, payloadError()
	}
	provider, providerOK := requiredString(fields, "provider")
	ref, refOK := requiredString(fields, "selection_ref")
	var generation uint64
	generationOK := json.Unmarshal(fields["generation"], &generation) == nil && generation > 0
	if !providerOK || !refOK || ref == "" || !generationOK || (provider != "claude" && provider != "codex") {
		return nil, payloadError()
	}
	cancelled := false
	s.quotaFlightMu.Lock()
	if flight := s.quotaFlights[provider]; flight != nil && flight.selection.ref == ref && flight.selection.gen == generation {
		select {
		case <-flight.done:
		default:
			flight.cancel()
			cancelled = true
		}
	}
	s.quotaFlightMu.Unlock()
	return struct {
		Cancelled bool `json:"cancelled"`
	}{Cancelled: cancelled}, nil
}

func (s *Service) cancelQuotaProviderFlight(provider string) {
	s.quotaFlightMu.Lock()
	if flight := s.quotaFlights[provider]; flight != nil {
		flight.cancel()
	}
	s.quotaFlightMu.Unlock()
}

func (s *Service) cancelAllQuotaReads() {
	s.quotaFlightMu.Lock()
	for _, flight := range s.quotaFlights {
		flight.cancel()
	}
	s.quotaFlightMu.Unlock()
}

func (s *Service) waitForQuotaReads() {
	for {
		s.quotaFlightMu.Lock()
		flights := make([]*quotaReadFlight, 0, len(s.quotaFlights))
		for _, flight := range s.quotaFlights {
			flights = append(flights, flight)
		}
		s.quotaFlightMu.Unlock()
		if len(flights) == 0 {
			return
		}
		for _, flight := range flights {
			<-flight.done
		}
	}
}

func (s *Service) clearQuotaCache(provider string) {
	s.quotaMu.Lock()
	for key := range s.lastQuota {
		if key == provider || strings.HasPrefix(key, provider+"\x00") {
			delete(s.lastQuota, key)
		}
	}
	s.quotaMu.Unlock()
}

func quotaRequestRequiresSelection(request Request) bool {
	switch request.Op {
	case "quota.selection.read", "quota.selection.set", "quota.cancel":
		return true
	case "quota.read":
		var fields map[string]json.RawMessage
		if json.Unmarshal(request.Payload, &fields) != nil {
			return false
		}
		_, hasRef := fields["selection_ref"]
		_, hasGeneration := fields["generation"]
		return hasRef || hasGeneration
	default:
		return false
	}
}

func isAttributedQuotaRead(request Request) bool {
	if request.Op != "quota.read" {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(request.Payload, &fields) != nil {
		return false
	}
	_, hasRef := fields["selection_ref"]
	_, hasGeneration := fields["generation"]
	return hasRef && hasGeneration
}

func isSelectionCapabilityHello(request Request) bool {
	if request.Op != "hello" {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(request.Payload, &fields) != nil || len(fields) != 1 {
		return false
	}
	return string(bytes.TrimSpace(fields["quota_selection"])) == "true"
}
