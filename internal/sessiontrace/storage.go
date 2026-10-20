package sessiontrace

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

type storageEnvelope struct {
	Version int           `json:"version"`
	Events  []storedEvent `json:"events"`
}

type storedEvent struct {
	Event        Event               `json:"event"`
	Observations []storedObservation `json:"observations"`
}

type storedObservation struct {
	Event       Event  `json:"event"`
	Source      Source `json:"source"`
	Index       int    `json:"index"`
	CallID      string `json:"call_id"`
	Phase       string `json:"phase"`
	Scope       string `json:"scope"`
	ParentID    string `json:"parent_id"`
	ParentScope string `json:"parent_scope"`
}

// MarshalStorage encodes only the Events' original metadata observations. Raw
// records are neither accepted nor reachable from this persistence boundary.
func MarshalStorage(events []Event) ([]byte, error) {
	envelope := storageEnvelope{Version: 1, Events: make([]storedEvent, 0, len(events))}
	for _, e := range events {
		if len(e.observations) == 0 {
			return nil, errors.New("sessiontrace: event has no source observations")
		}
		s := storedEvent{Event: cloneEvent(e)}
		for _, o := range e.observations {
			if err := validateObservation(o); err != nil {
				return nil, err
			}
			s.Observations = append(s.Observations, storedObservation{Event: cloneEvent(o.event), Source: o.source, Index: o.index, CallID: o.callID, Phase: o.phase, Scope: o.scope, ParentID: o.parentID, ParentScope: o.parentScope})
		}
		envelope.Events = append(envelope.Events, s)
	}
	return json.Marshal(envelope)
}

// UnmarshalStorage rejects wire-only payloads rather than silently publishing a
// cache with lost correlations. Use this codec only on the internal cache.
func UnmarshalStorage(data []byte) ([]Event, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope storageEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("sessiontrace: trailing storage data")
	}
	if envelope.Version != 1 || envelope.Events == nil {
		return nil, errors.New("sessiontrace: unsupported storage version")
	}
	out := make([]Event, 0, len(envelope.Events))
	for _, s := range envelope.Events {
		if len(s.Observations) == 0 {
			return nil, errors.New("sessiontrace: cache has no source observations")
		}
		e := cloneEvent(s.Event)
		for _, p := range s.Observations {
			o := observation{event: p.Event, source: p.Source, index: p.Index, callID: p.CallID, phase: p.Phase, scope: p.Scope, parentID: p.ParentID, parentScope: p.ParentScope}
			if err := validateObservation(o); err != nil {
				return nil, err
			}
			e.observations = append(e.observations, o)
		}
		if e.EventID != e.observations[0].event.EventID || e.SourceRef != e.observations[0].event.SourceRef {
			return nil, errors.New("sessiontrace: cache event identity mismatch")
		}
		out = append(out, e)
	}
	return out, nil
}

func validateObservation(o observation) error {
	if o.event.Timestamp.IsZero() || o.source.TS.IsZero() || o.index < 0 || !o.event.Timestamp.Equal(o.source.TS) {
		return errors.New("sessiontrace: invalid source timestamp or ordinal")
	}
	switch o.event.Kind {
	case "input", "response", "tool", "agent":
	default:
		return errors.New("sessiontrace: invalid event kind")
	}
	if o.event.SourceRef != sourceRef(o.source) || o.event.EventID != digest("event", o.event.SourceRef, strconv.Itoa(o.index), o.event.Kind) {
		return errors.New("sessiontrace: invalid source identity")
	}
	if o.phase != "" && o.phase != "start" && o.phase != "end" {
		return errors.New("sessiontrace: invalid observation phase")
	}
	return nil
}
