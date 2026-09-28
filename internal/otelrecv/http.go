package otelrecv

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"cctrace/internal/auth"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const maxRequestBodyBytes int64 = 16 << 20

// HTTPReceiver handles OTLP over HTTP (port 4318).
// Supports Content-Type: application/json (protojson) and application/x-protobuf (proto binary).
type HTTPReceiver struct {
	logs             *LogsReceiver
	metrics          *MetricsReceiver
	identityResolver IdentityResolver
}

func NewHTTPReceiver(logs *LogsReceiver, metrics *MetricsReceiver) *HTTPReceiver {
	return &HTTPReceiver{logs: logs, metrics: metrics}
}

// ClientIdentity is resolved from an OTLP/HTTP bearer token and attached to
// metrics that do not already carry user resource attributes.
type ClientIdentity struct {
	ProfileEmail string
	LoginEmail   string
	UserID       string
	UserTeam     string
}

type IdentityResolver func(ctx context.Context, token string) (*ClientIdentity, error)

func (r *HTTPReceiver) WithIdentityResolver(resolver IdentityResolver) *HTTPReceiver {
	r.identityResolver = resolver
	return r
}

// Handler returns an http.Handler for /v1/logs, /v1/metrics, /v1/traces.
func (r *HTTPReceiver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/logs", r.handleLogs)
	mux.HandleFunc("/v1/metrics", r.handleMetrics)
	mux.HandleFunc("/v1/traces", r.handleTraces)
	return mux
}

func (r *HTTPReceiver) handleLogs(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, ok := readRequestBody(w, req)
	if !ok {
		return
	}

	var pbReq collogspb.ExportLogsServiceRequest
	if err := unmarshalOTLP(req.Header.Get("Content-Type"), body, &pbReq); err != nil {
		log.Printf("[otelrecv/http] failed to unmarshal logs request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	identity, err := r.resolveIdentity(req)
	if err != nil {
		log.Printf("[otelrecv/http] identity resolve failed: %v", err)
		http.Error(w, "identity lookup unavailable", http.StatusServiceUnavailable)
		return
	}
	if _, err := r.logs.ExportWithIdentity(req.Context(), &pbReq, identity); err != nil {
		log.Printf("[otelrecv/http] logs export error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Logs report no partial_success, so an empty body is still accurate. Unlike
	// handleMetrics this does not echo the request encoding -- worth revisiting
	// if logs ever start reporting rejected records.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

func (r *HTTPReceiver) handleMetrics(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, ok := readRequestBody(w, req)
	if !ok {
		return
	}

	var pbReq colmetricspb.ExportMetricsServiceRequest
	if err := unmarshalOTLP(req.Header.Get("Content-Type"), body, &pbReq); err != nil {
		log.Printf("[otelrecv/http] failed to unmarshal metrics request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	identity, err := r.resolveIdentity(req)
	if err != nil {
		log.Printf("[otelrecv/http] identity resolve failed: %v", err)
		http.Error(w, "identity lookup unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx := withCodexAccount(req.Context(), req.Header.Get(CodexAccountHeader))
	resp, err := r.metrics.ExportWithIdentity(ctx, &pbReq, identity)
	if err != nil {
		log.Printf("[otelrecv/http] metrics export error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// The response carries partial_success when datapoints were dropped.
	// Answering a hardcoded "{}" would throw that away, and Codex sends all of
	// its metrics over this handler (codexconfig forces the otlp-http exporter),
	// so discarding it makes the loss invisible to every Codex client.
	writeOTLP(w, req.Header.Get("Content-Type"), resp)
}

// writeOTLP replies with msg encoded the same way the request was.
func writeOTLP(w http.ResponseWriter, contentType string, msg proto.Message) {
	var (
		body []byte
		err  error
		ct   string
	)
	if strings.Contains(contentType, "application/x-protobuf") {
		ct = "application/x-protobuf"
		body, err = proto.Marshal(msg)
	} else {
		ct = "application/json"
		body, err = protojson.Marshal(msg)
	}
	if err != nil {
		log.Printf("[otelrecv/http] failed to marshal response: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (r *HTTPReceiver) handleTraces(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.Error(w, "trace ingestion is not supported", http.StatusNotImplemented)
}

func unmarshalOTLP(contentType string, body []byte, msg proto.Message) error {
	if strings.Contains(contentType, "application/x-protobuf") {
		return proto.Unmarshal(body, msg)
	}
	return protojson.Unmarshal(body, msg)
}

func readRequestBody(w http.ResponseWriter, req *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxRequestBodyBytes))
	if err == nil {
		return body, true
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	http.Error(w, "failed to read body", http.StatusBadRequest)
	return nil, false
}

func (r *HTTPReceiver) resolveIdentity(req *http.Request) (*ClientIdentity, error) {
	if r.identityResolver == nil {
		return nil, nil
	}
	token := bearerToken(req.Header.Get("Authorization"))
	if token == "" {
		return nil, nil
	}
	return r.identityResolver(req.Context(), token)
}

// identityFromContext resolves the token the gRPC auth interceptor verified and
// left in the context. Returns nil when there is no token -- an unauthenticated
// path, or the global API key, which names no user -- or no resolver is
// installed. A failed lookup also returns nil: attribution falls back to the
// payload, which is where it was before any of this existed, rather than turning
// a lookup blip into lost telemetry.
func identityFromContext(ctx context.Context, resolver IdentityResolver) *ClientIdentity {
	if resolver == nil {
		return nil
	}
	token := auth.IngestTokenFromContext(ctx)
	if token == "" {
		return nil
	}
	identity, err := resolver(ctx, token)
	if err != nil {
		log.Printf("[otelrecv] identity resolve failed, attributing from payload: %v", err)
		return nil
	}
	return identity
}

func bearerToken(header string) string {
	if strings.HasPrefix(header, "Bearer ") {
		return strings.TrimPrefix(header, "Bearer ")
	}
	return header
}
