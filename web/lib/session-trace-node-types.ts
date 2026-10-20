import type { TraceCoverage, TraceScope } from './session-trace-types';
import type { SessionRecord } from './types';

type TraceNodesView = 'execution' | 'context' | 'metadata' | 'flow' | 'timing';
type TraceNodeKind = 'turn' | 'request' | 'response' | 'tool' | 'agent' | 'hook' | 'instructions' | 'context' | 'metadata' | 'usage' | 'event' | (string & {});
interface TraceVerifiedNumber { value?: number; state: string; definition: string; scope: string; revision: number }
interface TraceNode {
  node_id: string;
  parent_id: string | null;
  kind: TraceNodeKind;
  request_no?: TraceVerifiedNumber;
  tool_count?: TraceVerifiedNumber;
  group_id?: string;
  role?: string;
  semantic?: string;
  source_kind?: string;
  time_basis?: string;
  parent_relation?: string;
  grouping_basis?: string;
  title: string;
  timestamp: string | null;
  started_at: string | null;
  ended_at: string | null;
  status: string;
  has_children: boolean;
  child_count: number | null;
  source_ref: string;
  evidence: string[];
  availability: string;
}
interface AuditValue { state: string; value?: unknown; reason?: string }
interface TraceInstructionEntry { kind: string; source_path: string; path?: string; body: AuditValue }
interface TraceInstructionContext { instructions?: TraceInstructionEntry[]; settings?: AuditValue; provenance?: AuditValue }
interface TraceInputItem {
  request_path: string; kind: string; role?: string; message_ordinal?: number; block_ordinal?: number;
  value?: unknown; state: string; source_refs: string[]; offset_bytes?: number; total_bytes?: number; truncated?: boolean;
}
interface TraceInputPage { items: TraceInputItem[]; next_cursor: string | null; digest: { expected?: string; actual?: string; state: string }; coverage: string }
interface TraceProvenance {
  request_path: string; source_ref: string; raw_path: string; uuid?: string;
  block_start?: number; block_end?: number; message_ordinal?: number; block_ordinal?: number; state: string;
}
interface TraceSourceLocation { source_ref: string; node_id: string; raw_path: string; uuid?: string; block_start?: number; block_end?: number; state: string }
interface TraceComparison { compare_to: string; state: string; items: { request_path: string; compare_path?: string; change: string; reason?: string }[] }
interface TraceNavigation { child_cursors?: Record<string, string>; group_id?: string; ancestor_ids: string[]; root_node_id: string; root_cursor?: string; revision: number; epoch: number; state: string; reason?: string }
interface NativeRequest {
  state: string;
  input_page?: TraceInputPage;
  provenance?: TraceProvenance[];
  comparison?: TraceComparison;
  body?: Record<string, unknown>;
  params?: unknown;
  digest: { expected?: string; actual?: string; state: string };
  missing_refs: string[];
  source_refs: string[];
}
interface TraceNodesResponse {
  session_id: string;
  nodes: TraceNode[];
  next_cursor: string | null;
  prev_cursor?: string | null;
  revision?: number;
  epoch?: number;
  request_total?: TraceVerifiedNumber;
  search_scope?: string;
  search_coverage?: string;
  coverage: TraceCoverage;
}
interface TraceNodeDetail {
  session_id: string;
  node: TraceNode;
  navigation?: TraceNavigation;
  source_locations?: TraceSourceLocation[];
  input: AuditValue;
  output: AuditValue;
  context: AuditValue;
  usage: Record<string, unknown> | null;
  raw_records: SessionRecord[];
  request?: NativeRequest;
  notices: string[];
}
interface TraceNodesRequest {
  view?: TraceNodesView; parentId?: string; cursor?: string; limit?: number;
  requestNo?: number; groupId?: string; from?: string; to?: string; query?: string; revision?: number; epoch?: number;
}
interface TraceNodeDetailRequest { requestParts?: 'page'; inputCursor?: string; inputLimit?: number; compareTo?: string; sourceRef?: string }
interface TraceNodeQueryIdentity { userId: number; access: 'admin' | 'owner'; sessionId: string; scope?: TraceScope }

export type { TraceNodesView, TraceNodeKind, TraceNode, AuditValue, TraceInstructionEntry, TraceInstructionContext, NativeRequest, TraceNodesResponse, TraceNodeDetail, TraceNodesRequest, TraceNodeQueryIdentity, TraceNodeDetailRequest, TraceInputItem, TraceInputPage, TraceProvenance, TraceSourceLocation, TraceComparison, TraceNavigation };
