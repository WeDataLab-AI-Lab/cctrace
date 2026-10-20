import { apiFetch, buildUrl } from './api';
import type { SessionRecordScope } from './session-record-scope';
import type { SessionRecord } from './types';

interface InspectionTarget { recordId: string; contentPath?: string; sourceFile?: string; callId?: string }
type InspectionKind = 'all' | 'context';
type InspectionEventKind = 'message' | 'tool_call' | 'tool_result' | 'instructions' | 'hook' | 'reasoning' | 'record';
interface InspectionEvent {
  id: string;
  target: InspectionTarget;
  kind: InspectionEventKind;
  label: string;
  text: string;
  callId?: string;
  role?: string;
  record: SessionRecord;
  notices: string[];
  conversationAnchor?: string;
}
interface InspectionPage { records: SessionRecord[]; next_offset?: number; selected_record?: SessionRecord }
interface InspectionRequest { sessionId: string; scope?: SessionRecordScope; kind?: InspectionKind; offset?: number; target?: InspectionTarget; toolUseId?: string; signal?: AbortSignal }

const object = (value: unknown): Record<string, unknown> | undefined => value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
const string = (value: unknown): string => typeof value === 'string' ? value : '';
const formatted = (value: unknown): string => value == null ? '' : typeof value === 'string' ? value : JSON.stringify(value, null, 2);
const contentText = (value: unknown): string => {
  if (typeof value === 'string') return value;
  if (!Array.isArray(value)) return formatted(object(value)?.text ?? value);
  return value.map(part => {
    const block = object(part);
    return string(block?.text) || formatted(block?.content ?? part);
  }).join('\n\n');
};
const sourceNotices = (raw: Record<string, unknown>): string[] => {
  const notices: string[] = [];
  const hasRedaction = (value: unknown): boolean => value === '[redacted by cctrace client]' || (Array.isArray(value) ? value.some(hasRedaction) : !!object(value) && Object.values(object(value)!).some(hasRedaction));
  if (hasRedaction(raw)) notices.push('수집 시 가림 처리됨');
  for (const [key, label] of [['cctraceWithheld', '본문 미수집'], ['cctraceDropped', '수집에서 제외됨'], ['cctraceTruncated', '원자료 잘림']]) {
    const values = raw[key];
    if (Array.isArray(values)) for (const field of values) {
      const path=String(field).replace(/\[(\d+)\]/g, '.$1').split('.');
      const current=path.reduce<unknown>((value, part) => Array.isArray(value) ? value[Number(part)] : object(value)?.[part],raw);
      // Re-enrichment preserves historic markers; a now-present field is not missing.
      if (key !== 'cctraceTruncated' && current !== undefined && current !== null) continue;
      notices.push(`${label}: ${String(field)}`);
    }
  }
  return notices;
};

/** Each event keeps its collected record and JSON pointer; presentation never deduplicates sources. */
const inspectionEvents = (record: SessionRecord): InspectionEvent[] => {
  const raw = object(record.raw) ?? {};
  const anchor = record.view?.anchor_id || record.uuid || `${record.session_id}:${record.ts}:${record.record_type}`;
  const notices = sourceNotices(raw);
  const events: InspectionEvent[] = [];
  const add = (kind: InspectionEventKind, label: string, text: string, path: string, callId?: string, role?: string) => {
    events.push({ id: `${record.session_id}:${record.ts}:${anchor}:${record.source_file ?? ''}:${path}`, target: { recordId: anchor, contentPath: path, sourceFile: record.source_file, callId }, kind, label, text, callId, role, record, notices,
      conversationAnchor: record.view && record.view.kind !== 'hidden' && record.view.kind !== 'reasoning' ? anchor : undefined });
  };
  const attachment = object(raw.attachment);
  const attachmentType = string(attachment?.type);
  if (attachmentType === 'instructions') {
    const files = attachment?.files;
    if (Array.isArray(files) && files.length > 0) {
      files.forEach((file, index) => { const f = object(file); add('instructions', string(f?.path) || '주입된 지시문', contentText(f?.content), `/attachment/files/${index}`); });
    } else add('instructions', '주입된 지시문', contentText(attachment?.content ?? attachment?.rendered), '/attachment');
    return events;
  }
  if (attachmentType.startsWith('hook_') || attachmentType === 'async_hook_response') {
    add('hook', string(attachment?.hookName) || attachmentType, formatted(attachment), '/attachment', string(attachment?.toolUseID) || undefined);
    return events;
  }
  const payload = object(raw.payload);
  if (record.record_type === 'context') {
    for (const key of ['base_instructions', 'developer_instructions', 'user_instructions']) {
      if (payload?.[key] !== undefined) add('instructions', key, contentText(payload[key]), `/payload/${key}`);
    }
	 const collaborationInstructions = object(object(payload?.collaboration_mode)?.settings)?.developer_instructions;
	 if (collaborationInstructions !== undefined) add('instructions', 'Collaboration developer 지시문', contentText(collaborationInstructions), '/payload/collaboration_mode/settings/developer_instructions');
    if (events.length) return events;
  }
  const topLevelMessage = raw.role || (record.record_type === 'developer' && raw.content !== undefined) ? raw : undefined;
  const message = object(raw.message) ?? (payload?.type === 'message' || payload?.role ? payload : topLevelMessage);
  if (message) {
    const path = message === raw ? '' : message === payload ? '/payload' : '/message';
    const role = string(message.role) || record.record_type;
    const body = message.content;
    const isInstruction = role === 'developer' || (role === 'user' && contentText(body).includes('# AGENTS.md instructions'));
    if (isInstruction) add('instructions', role === 'developer' ? 'Developer 지시문' : '주입된 AGENTS.md', contentText(body), `${path}/content`, undefined, role);
    else if (Array.isArray(body)) {
      body.forEach((item, index) => {
        const block = object(item);
        const type = string(block?.type);
        const pointer = `${path}/content/${index}`;
        if (type === 'tool_use' || type === 'toolCall') add('tool_call', string(block?.name) || '도구 호출', formatted(block?.input ?? block?.arguments), pointer, string(block?.id) || undefined, role);
        else if (type === 'tool_result' || type === 'toolResult') add('tool_result', '도구 결과', contentText(block?.content), pointer, string(block?.tool_use_id ?? block?.toolCallId) || undefined, role);
        else if (type === 'thinking' || type === 'reasoning') add('reasoning', '추론 기록', contentText(block?.thinking ?? block?.text ?? item), pointer, undefined, role);
        else add('message', role === 'assistant' ? '어시스턴트' : role === 'user' ? '입력 메시지' : role, contentText(block?.text ?? item), pointer, undefined, role);
      });
    } else add('message', role === 'assistant' ? '어시스턴트' : role === 'user' ? '입력 메시지' : role, contentText(body), `${path}/content`, undefined, role);
  } else if (payload && ['function_call', 'custom_tool_call', 'function_call_output', 'custom_tool_call_output'].includes(string(payload.type))) {
    const result = string(payload.type).endsWith('_output');
    add(result ? 'tool_result' : 'tool_call', result ? '도구 결과' : string(payload.name) || '도구 호출', contentText(result ? payload.output : payload.arguments ?? payload.input), '/payload', string(payload.call_id) || undefined);
  } else if (record.view && record.view.kind !== 'hidden') {
    const view = record.view;
    if (view.text) add(view.kind === 'reasoning' ? 'reasoning' : 'message', view.role === 'assistant' ? '어시스턴트' : '메시지', view.text, '/view/text', undefined, view.role);
    view.tool_calls?.forEach((call, index) => add('tool_call', call.name || '도구 호출', call.input ?? '', `/view/tool_calls/${index}`, call.id));
    view.tool_results?.filter(result => !result.inferred).forEach((result, index) => add('tool_result', '도구 결과', result.output ?? '', `/view/tool_results/${index}`, result.id));
  }
  if (!events.length) add('record', record.record_type || '수집 기록', formatted(record.raw), '/');
  return events;
};

const fetchInspectionPage = async ({ sessionId, scope, kind, offset, target, toolUseId, signal }: InspectionRequest): Promise<InspectionPage> => {
  const response = await apiFetch(buildUrl(`/api/sessions/${encodeURIComponent(sessionId)}/inspection`, {
    kind: kind ?? 'all', offset: String(offset ?? 0), limit: '100', record_id: target?.recordId ?? '', source_file: target?.sourceFile ?? '', tool_use_id: toolUseId ?? '',
    profile_email: scope?.profileEmail ?? '', login_email: scope?.loginEmail ?? '', user_id: scope?.userID ?? '',
  }), { signal });
  if (!response.ok) throw new Error(response.status === 404 ? '선택한 기록을 찾을 수 없습니다. 접근 권한과 수집 상태를 확인해 주세요.' : response.status === 403 ? '이 기록에 접근할 권한이 없습니다.' : `기록을 읽지 못했습니다 (${response.status}).`);
  return response.json();
};

export { inspectionEvents, fetchInspectionPage };
export type { InspectionTarget, InspectionKind, InspectionEventKind, InspectionEvent, InspectionPage };
