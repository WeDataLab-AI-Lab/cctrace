package otelrecv

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"cctrace/internal/buffer"
	"cctrace/internal/emailalias"
	"cctrace/internal/queue"
	"cctrace/internal/store"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// LogsReceiver implements the OTLP LogsService and enqueues to PGMQ.
type LogsReceiver struct {
	collogspb.UnimplementedLogsServiceServer
	queue   *queue.Queue
	buffer  *buffer.Ring
	aliases emailalias.Resolver

	// identityResolver turns a verified ingest token into the account it belongs
	// to, for the gRPC path. The HTTP receiver holds its own; this one is used
	// from Export, where the token arrives in the context rather than a header.
	identityResolver IdentityResolver

	// dropSession refuses telemetry for sessions the dashboard deleted. Dropping at
	// receive time rather than at insert keeps the row from ever existing, so a
	// deleted session cannot reappear on a dashboard between arrival and the next
	// sweep.
	dropSession SessionFilter
	// dropAccount refuses telemetry from excluded login addresses (#715).
	dropAccount AccountFilter

	// in-memory heuristic for user_id/login_email collision detection (ephemeral, resets on restart)
	uidEmailMu  sync.Mutex
	uidEmailMap map[string]string // user_id -> most recently seen login_email
}

func NewLogsReceiver(q *queue.Queue, buf *buffer.Ring, aliases emailalias.Resolver) *LogsReceiver {
	return &LogsReceiver{queue: q, buffer: buf, aliases: aliases, uidEmailMap: make(map[string]string)}
}

// SessionFilter reports whether a session's telemetry must be dropped. It is a
// plain function so this package stays free of any storage dependency -- the
// receivers should not know that a "deleted session" is a row somewhere.
type SessionFilter func(sessionID string) bool

// WithSessionFilter installs the drop test. Without it every session is accepted,
// which is the correct default for a receiver constructed without a store.
func (r *LogsReceiver) WithSessionFilter(f SessionFilter) *LogsReceiver {
	r.dropSession = f
	return r
}

// AccountFilter reports whether telemetry from this login address must be
// dropped: the address belongs to an account excluded from collection (#715).
// A plain function for the same reason SessionFilter is.
type AccountFilter func(loginEmail string) bool

// WithAccountFilter installs the account test. Without it every address is
// accepted.
func (r *LogsReceiver) WithAccountFilter(f AccountFilter) *LogsReceiver {
	r.dropAccount = f
	return r
}

// noteLoginEmail records the login account currently seen for userID and reports
// the account it replaced, if any. It returns switched=true exactly once per
// change: the stored value is updated, so the following exports on the new
// account are the steady state rather than repeats of the same switch.
//
// Keeping the first-seen value instead would pin every later comparison to an
// account the user had already left, turning one switch into a log line per
// event and making "prev_email" mean "first email".
//
// The map is an ephemeral heuristic for logging and resets on restart; the
// durable record of switches is otel_events itself, which carries user_id and
// login_email on every row.
func (r *LogsReceiver) noteLoginEmail(userID, loginEmail string) (prev string, switched bool) {
	if userID == "" || loginEmail == "" {
		return "", false
	}
	r.uidEmailMu.Lock()
	defer r.uidEmailMu.Unlock()
	prev, seen := r.uidEmailMap[userID]
	r.uidEmailMap[userID] = loginEmail
	return prev, seen && prev != loginEmail
}

// buildEventsWithIdentity turns one OTLP request into the events it describes,
// applying the identity a bearer token resolved to over what the payload claimed.
//
// Separated from Export so the attribution it produces can be examined
// directly: the dispatch below hands each event to a queue, and asserting on
// what a queue swallowed is not the same as asserting on what was built.
//
// The token decides whether a request is accepted; until #535 it had no say in
// whose data the request became. Attribution came only from resource attributes
// the client wrote, so one valid token could write telemetry under any address --
// and cost aggregation and every per-user chart stand on that field.
//
// It overrides rather than fills a gap. Filling only what is empty leaves the
// claimed value winning whenever there is one, which is the case that matters:
// an empty field is a client that did not say, not a client that said someone
// else. Measured against thirty days of production, no token had ever carried
// another user's address, so nothing legitimate relays on that shape.
//
// login_email is deliberately untouched. It names the Anthropic account, and a
// cctrace token proves a cctrace dashboard account -- overwriting it would
// assert something the server never verified.
func (r *LogsReceiver) buildEventsWithIdentity(req *collogspb.ExportLogsServiceRequest, identity *ClientIdentity) []*store.OtelEvent {
	var events []*store.OtelEvent

	for _, rl := range req.ResourceLogs {
		res := extractResource(rl.Resource)
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				e := &store.OtelEvent{
					Ts:             toTime(lr.TimeUnixNano, lr.ObservedTimeUnixNano),
					UserID:         res.userID,
					UserName:       res.userName,
					UserTeam:       res.userTeam,
					OrgID:          res.orgID,
					ServiceVersion: res.serviceVersion,
				}

				attrs := flattenAttrs(lr.Attributes)

				// login_email = Anthropic login account (user.email), never overridden
				e.LoginEmail = res.userEmail
				if e.LoginEmail == "" {
					e.LoginEmail = strVal(attrs, "user.email")
				}

				// audit: detect a user_id whose login account changed mid-stream
				if prev, switched := r.noteLoginEmail(e.UserID, e.LoginEmail); switched {
					log.Printf("[audit] action=user_id_email_mismatch user_id=%s prev_email=%s new_email=%s", e.UserID, prev, e.LoginEmail)
				}

				// profile_email = org/team profile email (user.profile.email only)
				e.ProfileEmail = res.userProfileEmail
				if e.ProfileEmail == "" {
					e.ProfileEmail = strVal(attrs, "user.profile.email")
				}
				e.ProfileEmail = r.aliases.Resolve(e.ProfileEmail)
				if e.UserID == "" {
					e.UserID = strVal(attrs, "user.id")
				}
				if e.OrgID == "" {
					e.OrgID = strVal(attrs, "organization.id")
				}

				e.Agent, e.BillingProvider = classifyAgent(attrs, res)
				e.EventName = strVal(attrs, "event.name")
				e.SessionID = strVal(attrs, "session.id")
				e.PromptID = strVal(attrs, "prompt.id")
				e.Model = strVal(attrs, "model")
				e.Speed = strVal(attrs, "speed")
				// Support both dot-notation and underscore-notation keys
				e.ToolName = strValAny(attrs, "tool.name", "tool_name")
				e.ToolDecision = strValAny(attrs, "tool.decision", "tool_decision", "decision")

				if v, ok := attrsGetAny(attrs, "cost.usd", "cost_usd"); ok {
					f := toFloat64(v)
					e.CostUSD = &f
				}
				if v, ok := attrsGetAny(attrs, "input.tokens", "input_tokens"); ok {
					i := toInt(v)
					e.InputTokens = &i
				}
				if v, ok := attrsGetAny(attrs, "output.tokens", "output_tokens"); ok {
					i := toInt(v)
					e.OutputTokens = &i
				}
				if v, ok := attrsGetAny(attrs, "cache.read.tokens", "cache_read_tokens"); ok {
					i := toInt(v)
					e.CacheReadTokens = &i
				}
				if v, ok := attrsGetAny(attrs, "cache.creation.tokens", "cache_creation_tokens"); ok {
					i := toInt(v)
					e.CacheCreateTokens = &i
				}
				if v, ok := attrsGetAny(attrs, "duration.ms", "duration_ms"); ok {
					i := toInt(v)
					e.DurationMs = &i
				}
				if v, ok := attrsGetAny(attrs, "tool.success", "success"); ok {
					b := toBool(v)
					e.ToolSuccess = &b
				}

				// Remaining attrs go into the JSONB overflow
				known := map[string]bool{
					"event.name": true, "session.id": true, "prompt.id": true,
					"model": true, "speed": true,
					"tool.name": true, "tool_name": true,
					"tool.decision": true, "tool_decision": true, "decision": true,
					"cost.usd": true, "cost_usd": true,
					"input.tokens": true, "input_tokens": true,
					"output.tokens": true, "output_tokens": true,
					"cache.read.tokens": true, "cache_read_tokens": true,
					"cache.creation.tokens": true, "cache_creation_tokens": true,
					"duration.ms": true, "duration_ms": true,
					"tool.success": true, "success": true,
					"user.email": true, "user.id": true, "organization.id": true,
				}
				overflow := make(map[string]interface{})
				for k, v := range attrs {
					if !known[k] {
						overflow[k] = v
					}
				}
				if len(overflow) > 0 {
					e.Attrs = overflow
				}

				if r.dropSession != nil && r.dropSession(e.SessionID) {
					continue
				}
				// The payload's own address: identity never overwrites login_email
				// on logs, so this is what the client sent.
				if r.dropAccount != nil && r.dropAccount(e.LoginEmail) {
					continue
				}
				events = append(events, e)
			}
		}
	}

	if identity != nil {
		for _, e := range events {
			if identity.ProfileEmail != "" {
				// No alias resolution: the token's address is already the
				// canonical one, which is what the alias table maps toward.
				e.ProfileEmail = identity.ProfileEmail
			}
			if identity.UserID != "" {
				e.UserID = identity.UserID
			}
		}
	}

	return events
}

func (r *LogsReceiver) Export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	// The gRPC path arrives here. Its interceptor puts the token it verified in
	// the context, so the same override the HTTP path applies is available --
	// without it gRPC would keep attributing purely from the payload (#535).
	return r.ExportWithIdentity(ctx, req, r.identityFromContext(ctx))
}

func (r *LogsReceiver) identityFromContext(ctx context.Context) *ClientIdentity {
	return identityFromContext(ctx, r.identityResolver)
}

// WithIdentityResolver installs the lookup used by the gRPC path.
func (r *LogsReceiver) WithIdentityResolver(resolver IdentityResolver) *LogsReceiver {
	r.identityResolver = resolver
	return r
}

// ExportWithIdentity is Export with the identity a bearer token resolved to.
// Callers that authenticate the request use this; Export remains for the paths
// that have no token to offer.
func (r *LogsReceiver) ExportWithIdentity(ctx context.Context, req *collogspb.ExportLogsServiceRequest, identity *ClientIdentity) (*collogspb.ExportLogsServiceResponse, error) {
	events := r.buildEventsWithIdentity(req, identity)

	if len(events) > 0 {
		for _, e := range events {
			if _, err := r.queue.Send(ctx, queue.QueueOtelLogs, e); err != nil {
				log.Printf("[otelrecv] failed to enqueue event, buffering: %v", err)
				data, merr := json.Marshal(e)
				if merr != nil {
					log.Printf("[otelrecv] failed to marshal event for buffer: %v", merr)
					continue
				}
				r.buffer.Push(data)
				continue
			}
		}
		log.Printf("[otelrecv] enqueued %d events", len(events))
	}

	return &collogspb.ExportLogsServiceResponse{}, nil
}

// resourceInfo holds extracted resource attributes.
type resourceInfo struct {
	userID           string
	userEmail        string // from user.email (Anthropic login account)
	userProfileEmail string // from user.profile.email (org/team profile email)
	userName         string // from user.name
	userTeam         string
	orgID            string
	serviceName      string
	serviceVersion   string
	sessionID        string
}

func extractResource(r interface{ GetAttributes() []*commonpb.KeyValue }) resourceInfo {
	if r == nil {
		return resourceInfo{}
	}
	attrs := flattenAttrs(r.GetAttributes())
	return resourceInfo{
		userID:           strVal(attrs, "user.id"),
		userEmail:        strVal(attrs, "user.email"),
		userProfileEmail: strVal(attrs, "user.profile.email"),
		userName:         strVal(attrs, "user.name"),
		userTeam:         strVal(attrs, "user.team"),
		orgID:            strVal(attrs, "org.id"),
		serviceName:      strVal(attrs, "service.name"),
		serviceVersion:   strVal(attrs, "service.version"),
		sessionID:        strVal(attrs, "session.id"),
	}
}

// classifyAgent detects whether the OTEL event comes from Claude or Codex.
// For Claude events, billing_provider distinguishes anthropic-native models
// (claude-*) from compatible third-party models (kimi, gemini, qwen, ...)
// served via the Anthropic-compatible API surface.
// Returns (agent, billingProvider).
func classifyAgent(attrs map[string]interface{}, res resourceInfo) (string, string) {
	if isCodexServiceName(res.serviceName) || isCodexServiceName(strValAny(attrs, "service.name", "service_name")) {
		return "codex", "openai"
	}
	for k := range attrs {
		if len(k) > 6 && k[:6] == "codex." {
			return "codex", "openai"
		}
	}
	model := strings.ToLower(strVal(attrs, "model"))
	if model != "" && !strings.HasPrefix(model, "claude") {
		return "claude", "other"
	}
	return "claude", "anthropic"
}

func isCodexServiceName(serviceName string) bool {
	serviceName = strings.ToLower(strings.TrimSpace(serviceName))
	return serviceName == "codex" ||
		serviceName == "openai-codex" ||
		strings.HasPrefix(serviceName, "codex_") ||
		strings.HasPrefix(serviceName, "codex-")
}

// flattenAttrs converts OTLP KeyValue slice to a map.
func flattenAttrs(kvs []*commonpb.KeyValue) map[string]interface{} {
	m := make(map[string]interface{}, len(kvs))
	for _, kv := range kvs {
		if kv.Value == nil {
			continue
		}
		switch v := kv.Value.Value.(type) {
		case *commonpb.AnyValue_StringValue:
			m[kv.Key] = v.StringValue
		case *commonpb.AnyValue_IntValue:
			m[kv.Key] = v.IntValue
		case *commonpb.AnyValue_DoubleValue:
			m[kv.Key] = v.DoubleValue
		case *commonpb.AnyValue_BoolValue:
			m[kv.Key] = v.BoolValue
		default:
			m[kv.Key] = fmt.Sprintf("%v", kv.Value.Value)
		}
	}
	return m
}

func strValAny(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if s := strVal(m, k); s != "" {
			return s
		}
	}
	return ""
}

func attrsGetAny(m map[string]interface{}, keys ...string) (interface{}, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v, true
		}
	}
	return nil, false
}

func strVal(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func toFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int64:
		return float64(val)
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	}
	return 0
}

func toInt(v interface{}) int {
	switch val := v.(type) {
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		i, _ := strconv.Atoi(val)
		return i
	}
	return 0
}

func toBool(v interface{}) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return val == "true" || val == "1"
	case int64:
		return val != 0
	}
	return false
}

func toTime(timeNano, observedNano uint64) time.Time {
	if timeNano > 0 {
		return time.Unix(0, int64(timeNano))
	}
	if observedNano > 0 {
		return time.Unix(0, int64(observedNano))
	}
	return time.Now()
}
