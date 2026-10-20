package desktop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

const (
	ProtocolVersion   = 1
	maxRequestBytes   = 1 << 20
	maxResponseBytes  = 1 << 20
	maxRequestIDBytes = 128
)

type Request struct {
	Version int             `json:"version"`
	Kind    string          `json:"kind"`
	ID      string          `json:"id"`
	Op      string          `json:"op"`
	Payload json.RawMessage `json:"payload"`
}

type ErrorObject struct {
	Code          string                `json:"code"`
	Message       string                `json:"message"`
	PartialResult *LifecycleApplyResult `json:"partial_result,omitempty"`
}

type Response struct {
	Version int          `json:"version"`
	Kind    string       `json:"kind"`
	ID      string       `json:"id"`
	Result  any          `json:"result,omitempty"`
	Error   *ErrorObject `json:"error,omitempty"`
}

type requestFrame struct {
	data       []byte
	tooLarge   bool
	terminated bool
}

func Serve(ctx context.Context, service *Service, input io.Reader, output io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	serveCtx, cancel := context.WithCancel(ctx)
	var handlers sync.WaitGroup
	var outputMu sync.Mutex
	var errorMu sync.Mutex
	var outputErr error
	var activeQuotaHandlers atomic.Int32
	var activeDaemonControlHandlers atomic.Int32
	write := func(response Response) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		errorMu.Lock()
		priorError := outputErr
		errorMu.Unlock()
		if priorError != nil {
			return priorError
		}
		if err := writeResponse(output, response); err != nil {
			errorMu.Lock()
			if outputErr == nil {
				outputErr = err
			}
			errorMu.Unlock()
			return err
		}
		return nil
	}
	finish := func(err error) error {
		cancel()
		service.cancelAllQuotaReads()
		service.cancelAllAccountFlights()
		service.waitForQuotaReads()
		service.waitForAccountReads()
		handlers.Wait()
		errorMu.Lock()
		defer errorMu.Unlock()
		if err == nil && outputErr != nil {
			return outputErr
		}
		return err
	}
	reader := bufio.NewReaderSize(input, 32*1024)
	quotaSelectionNegotiated := false
	quotaAccountsNegotiated := false
	profileDiscoveryNegotiated := false
	updateTrustNegotiated := false
	daemonControlNegotiated := false
	profileRegistryNegotiated := false
	multiHomeNegotiated := false
	for {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		frame, err := readFrame(reader)
		if errors.Is(err, io.EOF) {
			return finish(nil)
		}
		if err != nil {
			return finish(err)
		}

		var response Response
		asyncHandler := false
		switch {
		case frame.tooLarge:
			response = errorResponse("", "request_too_large", "request record exceeds the size limit")
		case !frame.terminated:
			response = errorResponse(extractRequestID(frame.data), "unterminated_record", "request record is missing its newline")
		default:
			request, protocolError, ok := decodeRequest(frame.data)
			if !ok {
				response = protocolError
			} else {
				switch {
				case request.Op == "hello":
					response = service.Handle(serveCtx, request)
					if response.Error != nil {
						multiHomeNegotiated = false
						updateTrustNegotiated = false
						quotaSelectionNegotiated = false
						quotaAccountsNegotiated = false
						profileDiscoveryNegotiated = false
						daemonControlNegotiated = false
						profileRegistryNegotiated = false
					} else if emptyPayload(request.Payload) {
						multiHomeNegotiated = false
						updateTrustNegotiated = false
						quotaSelectionNegotiated = false
						quotaAccountsNegotiated = false
						profileDiscoveryNegotiated = false
						daemonControlNegotiated = false
						profileRegistryNegotiated = false
					} else if isMultiHomeHello(request) {
						h, ok := response.Result.(helloResult)
						multiHomeNegotiated = ok && h.Capabilities.MultiHomeProfiles
					} else if isUpdateTrustHello(request) {
						updateTrustNegotiated = updateTrustAdvertised(response)
					} else if isHomeDiscoveryHello(request) {
						profileDiscoveryNegotiated = profileDiscoveryAdvertised(response)
					} else if isQuotaAccountsHello(request) {
						quotaAccountsNegotiated = true
					} else if isProfileRegistryHello(request) {
						profileRegistryNegotiated = profileRegistryAdvertised(response)
					} else if isSelectionCapabilityHello(request) {
						quotaSelectionNegotiated = true
					} else if isDaemonControlCapabilityHello(request) {
						daemonControlNegotiated = daemonControlAdvertised(response)
					}
				case (strings.HasPrefix(request.Op, "profiles.membership.") || strings.HasPrefix(request.Op, "profiles.homes.") || strings.HasPrefix(request.Op, "profiles.transmission.") || strings.HasPrefix(request.Op, "profiles.onboarding.")) && !multiHomeNegotiated:
					response = errorResponse(request.ID, "capability_not_negotiated", "multi-home capability was not negotiated")
				case request.Op == "update.trust.read" && !updateTrustNegotiated:
					response = errorResponse(request.ID, "capability_not_negotiated", "update trust capability was not negotiated")
				case strings.HasPrefix(request.Op, "profiles.discover") && !profileDiscoveryNegotiated:
					response = errorResponse(request.ID, "capability_not_negotiated", "profile discovery capability was not negotiated")
				case strings.HasPrefix(request.Op, "quota.account") && !quotaAccountsNegotiated:
					response = errorResponse(request.ID, "capability_not_negotiated", "account capability was not negotiated")
				case profileRegistryOperation(request, service) && !profileRegistryNegotiated:
					response = errorResponse(request.ID, "capability_not_negotiated", "profile registry capability was not negotiated")
				case request.Op == "daemon.restart" && !daemonControlNegotiated:
					response = errorResponse(request.ID, "capability_not_negotiated", "daemon control capability was not negotiated")
				case request.Op == "daemon.restart":
					if !activeDaemonControlHandlers.CompareAndSwap(0, 1) {
						response = errorResponse(request.ID, "daemon_busy", "another daemon control operation is in progress")
						break
					}
					asyncHandler = true
					handlers.Add(1)
					go func(request Request) {
						defer handlers.Done()
						defer activeDaemonControlHandlers.Store(0)
						if err := write(service.Handle(serveCtx, request)); err != nil {
							cancel()
							service.cancelAllQuotaReads()
							service.cancelAllAccountFlights()
						}
					}(request)
				case quotaRequestRequiresSelection(request) && !quotaSelectionNegotiated:
					response = errorResponse(request.ID, "capability_not_negotiated", "quota selection capability was not negotiated")
				case isAttributedQuotaRead(request) || request.Op == "quota.account.read" || request.Op == "quota.account.recover":
					for {
						active := activeQuotaHandlers.Load()
						if active >= maxConcurrentQuotaRequests {
							response = errorResponse(request.ID, "quota_busy", "too many quota requests are in flight")
							break
						}
						if activeQuotaHandlers.CompareAndSwap(active, active+1) {
							asyncHandler = true
							handlers.Add(1)
							go func(request Request) {
								defer handlers.Done()
								defer activeQuotaHandlers.Add(-1)
								if err := write(service.Handle(serveCtx, request)); err != nil {
									cancel()
									service.cancelAllQuotaReads()
									service.cancelAllAccountFlights()
								}
							}(request)
							break
						}
					}
				default:
					response = service.Handle(serveCtx, request)
				}
			}
		}
		if !asyncHandler {
			if err := write(response); err != nil {
				return finish(err)
			}
		}
		if !frame.terminated {
			return finish(nil)
		}
	}
}

func readFrame(reader *bufio.Reader) (requestFrame, error) {
	frame := requestFrame{}
	readAny := false
	for {
		part, err := reader.ReadSlice('\n')
		if len(part) > 0 {
			readAny = true
			terminated := part[len(part)-1] == '\n'
			if terminated {
				part = part[:len(part)-1]
				if len(part) > 0 && part[len(part)-1] == '\r' {
					part = part[:len(part)-1]
				}
			}
			if !frame.tooLarge {
				if len(part) > maxRequestBytes-len(frame.data) {
					frame.tooLarge = true
					frame.data = nil
				} else {
					frame.data = append(frame.data, part...)
				}
			}
			if terminated {
				frame.terminated = true
				return frame, nil
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if !readAny {
				return requestFrame{}, io.EOF
			}
			return frame, nil
		}
		if err != nil {
			return requestFrame{}, err
		}
	}
}

func decodeRequest(line []byte) (Request, Response, bool) {
	if !utf8.Valid(line) {
		return Request{}, errorResponse("", "invalid_utf8", "request record is not valid UTF-8"), false
	}
	requestID := extractRequestID(line)
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		code := "invalid_request"
		var syntaxError *json.SyntaxError
		if errors.As(err, &syntaxError) {
			code = "invalid_json"
		}
		return Request{}, errorResponse(requestID, code, "request JSON is invalid"), false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Request{}, errorResponse(requestID, "invalid_request", "request JSON must contain one object"), false
	}
	if !lifecycleJSONHasUniqueKeys(line) {
		return Request{}, errorResponse("", "invalid_request", "request JSON is ambiguous or too deeply nested"), false
	}
	if request.Version != ProtocolVersion {
		return Request{}, errorResponse(requestID, "unsupported_version", "protocol version is not supported"), false
	}
	if request.Kind != "request" {
		return Request{}, errorResponse(requestID, "invalid_kind", "request kind is not supported"), false
	}
	if !validRequestID(request.ID) {
		return Request{}, errorResponse("", "invalid_id", "request id is invalid"), false
	}
	if request.Op == "" {
		return Request{}, errorResponse(request.ID, "unknown_operation", "operation is not supported"), false
	}
	if len(request.Payload) == 0 || !isJSONObject(request.Payload) {
		return Request{}, errorResponse(request.ID, "invalid_payload", "payload must be a JSON object"), false
	}
	return request, Response{}, true
}

func extractRequestID(line []byte) string {
	var candidate struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(line, &candidate); err != nil || !validRequestID(candidate.ID) {
		return ""
	}
	return candidate.ID
}

func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDBytes || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func errorResponse(id, code, message string) Response {
	return Response{
		Version: ProtocolVersion,
		Kind:    "response",
		ID:      id,
		Error:   &ErrorObject{Code: code, Message: message},
	}
}

func writeResponse(output io.Writer, response Response) error {
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		response = errorResponse(response.ID, "response_too_large", "response record exceeds the size limit")
		data, err = json.Marshal(response)
		if err != nil {
			return err
		}
	}
	if len(data) > maxResponseBytes {
		return errors.New("protocol error record exceeds the size limit")
	}
	data = append(data, '\n')
	written, err := output.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func decodePayload(raw json.RawMessage, target any) error {
	if !isJSONObject(raw) {
		return errors.New("payload is not an object")
	}
	if !lifecycleJSONHasUniqueKeys(raw) {
		return errors.New("payload is ambiguous or too deeply nested")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("payload has trailing data")
	}
	return nil
}

func isUpdateTrustHello(request Request) bool {
	var fields map[string]json.RawMessage
	return request.Op == "hello" && decodePayload(request.Payload, &fields) == nil && len(fields) == 1 && bytes.Equal(bytes.TrimSpace(fields["update_trust"]), []byte("true"))
}

func updateTrustAdvertised(response Response) bool {
	hello, ok := response.Result.(helloResult)
	return ok && response.Error == nil && hello.Capabilities.UpdateTrust
}

func isProfileRegistryHello(request Request) bool {
	var fields map[string]json.RawMessage
	return request.Op == "hello" && decodePayload(request.Payload, &fields) == nil && len(fields) == 1 && bytes.Equal(bytes.TrimSpace(fields["profile_registry"]), []byte("true"))
}
func profileRegistryAdvertised(response Response) bool {
	hello, ok := response.Result.(helloResult)
	return ok && response.Error == nil && hello.Capabilities.ProfileRegistry
}
func profileRegistryOperation(request Request, service *Service) bool {
	switch request.Op {
	case "profiles.update", "profiles.resolve", "profiles.remove.preview":
		return true
	case "profiles.remove":
		return service.registry != nil
	default:
		return false
	}
}

func isQuotaAccountsHello(r Request) bool {
	var fields map[string]json.RawMessage
	return r.Op == "hello" && json.Unmarshal(r.Payload, &fields) == nil && len(fields) == 1 && bytes.Equal(bytes.TrimSpace(fields["quota_accounts"]), []byte("true"))
}

func isHomeDiscoveryHello(r Request) bool {
	var fields map[string]json.RawMessage
	return r.Op == "hello" && json.Unmarshal(r.Payload, &fields) == nil && len(fields) == 1 && bytes.Equal(bytes.TrimSpace(fields["profile_discovery"]), []byte("true"))
}

func profileDiscoveryAdvertised(response Response) bool {
	h, ok := response.Result.(helloResult)
	return ok && h.Capabilities.ProfileDiscovery
}

func isMultiHomeHello(r Request) bool {
	var fields map[string]json.RawMessage
	return r.Op == "hello" && decodePayload(r.Payload, &fields) == nil && len(fields) == 1 && bytes.Equal(bytes.TrimSpace(fields["multi_home_profiles"]), []byte("true"))
}
