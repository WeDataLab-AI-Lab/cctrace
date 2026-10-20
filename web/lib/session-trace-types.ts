import type { SessionRecord } from './types';

type TraceKind = 'input' | 'response' | 'tool' | 'agent';
type TraceState = 'building' | 'ready' | 'stale' | 'unavailable';
interface TraceScope { profileEmail?: string; loginEmail?: string; userID?: string }
interface TraceRange { from: string; to: string }
interface TraceIdentity { userId: number; access: 'admin' | 'owner'; sessionId: string; scope?: TraceScope }
interface TraceCoverage {
  state: 'complete' | 'partial' | 'truncated' | 'unavailable';
  from: string | null; to: string | null; processed_rows: number; row_limit: number; reason: string | null;
}
interface TraceDemand { mode: 'overview' | 'range'; from?: string; to?: string; lease_id?: string }
interface TraceLease { session_id: string; lease_id: string; expires_at: string; snapshot_id: string; state: TraceState }
interface TraceBin { kind: TraceKind; from: string; to: string; first_ts: string; last_ts: string; count: number }
interface TraceOverview {
  session_id: string; snapshot_id: string; source_revision: string; state: TraceState; coverage: TraceCoverage;
  from: string | null; to: string | null; bins: TraceBin[]; events_count: number;
}
interface TraceEvent {
  event_id: string; kind: TraceKind; timestamp: string; started_at: string | null; ended_at: string | null;
  turn_key: string | null; parent_event_id: string | null; agent_id: string | null; label: string;
  status: 'running' | 'completed' | 'error' | 'interrupted' | 'unknown'; evidence: string[]; source_ref: string;
}
interface TraceEvents {
  session_id: string; snapshot_id: string; source_revision: string; state: TraceState; coverage: TraceCoverage;
  events: TraceEvent[]; next_cursor: string | null;
}
interface TraceContext { session_id: string; snapshot_id: string; event_id: string | null; records: SessionRecord[]; next_cursor: string | null }
interface TraceEventsRequest extends TraceRange { snapshot_id: string; cursor?: string }
interface TraceContextRequest { snapshot_id: string; event_id?: string; turn_key?: string; cursor?: string }

export type { TraceKind, TraceState, TraceScope, TraceRange, TraceIdentity, TraceCoverage, TraceDemand, TraceLease, TraceBin, TraceOverview, TraceEvent, TraceEvents, TraceContext, TraceEventsRequest, TraceContextRequest };
