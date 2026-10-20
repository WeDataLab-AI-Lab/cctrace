'use client';

import { useId, type ReactNode } from 'react';
import { cn } from '@/lib/utils';
import type { AuditValue, NativeRequest, TraceNodeDetail, TraceSourceLocation } from '@/lib/session-trace-node-types';
import type { SessionRecord } from '@/lib/types';
import { formatObservedDuration } from './trace-node-format';
import { traceNodeAvailabilityLabel, traceNodeKindLabel, traceNodeStatusLabel, traceNodeStatusTone, traceNodeTimeLabel } from './trace-node-format';
import { extractTraceInstructions, readableContentValue } from './trace-node-instructions';
import { RequestInputView } from './request-input-view';
import { traceNodePresentation, type TraceNodeContentKind } from './trace-node-presentation';

const contentLabels: Record<TraceNodeContentKind, string> = {
  turn: '입력', response: '응답', tool: '입력·결과', request: 'API 요청', instructions: '지시문', context: '실행 설정',
  metadata: '기록 내용', event: '기록 내용', usage: '사용량 연결 기록', hook: '훅 실행', agent: '하위 에이전트',
};
const noticeLabels: Record<string, string> = {
  encrypted_reasoning_content_not_decrypted: '기록된 추론 요약을 표시합니다. 암호화된 본문은 읽을 수 없습니다.',
};

const jsonText = (value: unknown): string => {
  if (typeof value === 'string') return value;
  if (value === undefined) return '';
  return JSON.stringify(value, null, 2) ?? String(value);
};

const stateLabel = (state: string): string => ({
  observed: '수집됨', collected: '수집됨', available: '수집됨',
  not_collected: '미수집', missing: '미수집',
  redacted: '가림', withheld: '가림',
  truncated: '잘림',
  not_queried: '미조회', unqueried: '미조회',
  unavailable: '확인 불가', unknown: '확인 불가', error: '오류', pending: '준비 중', match: '일치', matched: '일치', mismatch: '불일치', different: '불일치',
  not_checked: '미검증',
}[state] ?? `상태: ${state}`);

interface AuditSectionProps { label: string; audit: AuditValue }
const AuditSection = ({ label, audit }: AuditSectionProps) => {
  if (audit.state === 'not_applicable') return null;
  const exceptional = !['observed', 'collected', 'available'].includes(audit.state);
  return <section className="min-w-0 border-b border-border-subtle py-3 last:border-b-0">
    <div className="mb-1 flex flex-wrap items-center gap-2">
      <h3 className="text-xs font-medium text-ink">{label}</h3>
      {exceptional && <span className={cn('rounded border border-border px-1.5 py-0.5 text-[10px]', audit.state === 'error' ? 'text-danger-strong' : 'text-ink-3')}>{stateLabel(audit.state)}</span>}
    </div>
    {audit.reason && <p className="mb-2 break-words text-[11px] text-ink-3">{audit.reason}</p>}
    {audit.value !== undefined ? <pre className="max-h-[32rem] min-w-0 overflow-auto whitespace-pre-wrap break-words rounded bg-surface-sunk p-2 font-mono text-[11px] text-ink-2">{jsonText(audit.value)}</pre>
      : <p className="text-[11px] text-ink-3">{exceptional ? stateLabel(audit.state) : '이 기록에 값이 없습니다.'}</p>}
  </section>;
};

const MessageAuditSection = ({ label, audit }: AuditSectionProps) => {
  const messageValue = audit.value === undefined ? undefined : readableContentValue(audit.value);
  return <AuditSection label={label} audit={{ ...audit, ...(messageValue === undefined ? {} : { value: messageValue }) }} />;
};

interface RawRecordProps { record: SessionRecord; index: number; actions?: ReactNode }
const RawRecord = ({ record, index, actions }: RawRecordProps) => {
  const title = [record.record_type, record.source_file, traceNodeTimeLabel(record.ts)].filter(Boolean).join(' · ');
  return <details className="rounded border border-border bg-surface">
    <summary className="cursor-pointer px-3 py-2 text-[11px] text-ink-2 focus-visible:ring-1 focus-visible:ring-accent">
      <span className="mr-2 font-mono text-ink-3">{index + 1}</span>{title || '원자료 기록'}
    </summary>
    {actions && <div className="border-t border-border-subtle px-3 py-2">{actions}</div>}
    {record.raw !== undefined ? <pre className="max-h-[28rem] overflow-auto border-t border-border-subtle bg-surface-sunk p-3 font-mono text-[11px] text-ink-2">{jsonText(record.raw)}</pre>
      : <p className="border-t border-border-subtle px-3 py-2 text-[11px] text-ink-3">원자료 본문은 수집되지 않았습니다.</p>}
  </details>;
};

interface UsageField { key: string; value: number }
const usageFieldLabels: Record<string, string> = {
  input_tokens: '입력', output_tokens: '출력', cache_read_tokens: '캐시 읽기', cache_read_input_tokens: '캐시 읽기', cached_input_tokens: '캐시 입력',
  cache_create_tokens: '캐시 생성', cache_creation_input_tokens: '캐시 생성', cache_creation_tokens: '캐시 생성',
  cache_write_input_tokens: '캐시 쓰기 입력', reasoning_output_tokens: '추론 출력', cached_tokens: '캐시된 입력',
  'cache_creation.ephemeral_5m_input_tokens': '5분 임시 캐시 생성 입력',
  'cache_creation.ephemeral_1h_input_tokens': '1시간 임시 캐시 생성 입력',
};

const recordValue = (value: unknown): Record<string, unknown> | undefined => value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
const numericUsageFields = (value: unknown): UsageField[] => {
  const object = recordValue(value) ?? {};
  const fields = Object.entries(object).filter((entry): entry is [string, number] => typeof entry[1] === 'number' && Number.isFinite(entry[1]))
    .map(([key, number]) => ({ key, value: number }));
  const cacheCreation = recordValue(object.cache_creation);
  for (const key of ['ephemeral_5m_input_tokens', 'ephemeral_1h_input_tokens']) {
    const number = cacheCreation?.[key];
    if (typeof number === 'number' && Number.isFinite(number)) fields.push({ key: `cache_creation.${key}`, value: number });
  }
  return fields;
};

const usageLabel = (key: string, agent: unknown): string => {
  if (key === 'input_tokens' && agent === 'claude') return '일반 입력 (input_tokens)';
  return usageFieldLabels[key] ?? key;
};

const formatCount = (value: number): string => new Intl.NumberFormat('ko-KR', { maximumFractionDigits: 0 }).format(value);
const formatDifference = (value: number): string => `${value > 0 ? '+' : ''}${formatCount(value)}`;

interface UsageComparisonProps { usage: Record<string, unknown>; standalone: boolean }
const UsageComparison = ({ usage, standalone }: UsageComparisonProps) => {
  const current = numericUsageFields(usage?.values);
  const previous = usage?.previous && typeof usage.previous === 'object' ? usage.previous as Record<string, unknown> : undefined;
  const previousValues = numericUsageFields(previous?.values);
  const previousByKey = new Map(previousValues.map(item => [item.key, item.value]));
  const hasPrevious = !!previous;
  const currentByKey = new Map(current.map(item => [item.key, item.value]));
  const comparisonKeys = [...new Set([...currentByKey.keys(), ...previousByKey.keys()])];
  const otelRecords = Array.isArray(usage?.otel) ? usage.otel : usage?.otel ? [usage.otel] : [];
  return <section className="space-y-3 border-b border-border-subtle py-3">
    <div className="flex flex-wrap items-center gap-2">
      <h3 className="text-xs font-medium text-ink">{standalone ? '사용량·캐시 기록' : '응답별 사용량·캐시'}</h3>
      {typeof usage?.source === 'string' && <span className="rounded border border-border px-1.5 py-0.5 text-[10px] text-ink-3">원천 · {usage.source}</span>}
      {typeof usage?.scope === 'string' && <span className="rounded border border-border px-1.5 py-0.5 text-[10px] text-ink-3">범위 · {usage.scope}</span>}
    </div>
    {current.length > 0 ? <dl className="grid grid-cols-2 gap-x-4 gap-y-2 sm:grid-cols-3">
      {current.map(item => <div key={item.key} className="min-w-0">
        <dt className="break-words text-[10px] text-ink-3">{usageLabel(item.key, usage?.agent)} <code className="font-mono">({item.key})</code></dt>
        <dd className="font-mono text-xs tabular-nums text-ink">{formatCount(item.value)}</dd>
      </div>)}
    </dl> : <p role="status" className="text-[11px] text-ink-3">응답 사용량 수치가 수집되지 않았습니다.</p>}
    {hasPrevious && <section aria-label="직전 응답 비교" className="space-y-1">
      <h4 className="text-[11px] font-medium text-ink-2">직전 관측 응답과 비교</h4>
      <p className="text-[10px] text-ink-3">{previous.timestamp ? traceNodeTimeLabel(String(previous.timestamp)) : '직전 응답 시각 미수집'} · {previous.evidence ? String(previous.evidence) : '연결 근거 미표시'}</p>
      <div className="overflow-x-auto rounded border border-border">
        <table className="w-full min-w-80 text-left text-[10px]">
          <thead className="bg-surface-2 text-ink-3"><tr><th className="px-2 py-1 font-medium">값</th><th className="px-2 py-1 font-medium">이번 응답</th><th className="px-2 py-1 font-medium">직전 응답</th><th className="px-2 py-1 font-medium">차이</th></tr></thead>
          <tbody>{comparisonKeys.map(key => {
            const value = currentByKey.get(key), prior = previousByKey.get(key);
            const difference = value !== undefined && prior !== undefined ? value - prior : undefined;
            return <tr key={key} className="border-t border-border-subtle">
              <th className="px-2 py-1 font-medium text-ink-2">{usageLabel(key, usage?.agent)} <code className="font-mono text-ink-3">({key})</code></th>
              <td className="px-2 py-1 font-mono tabular-nums">{value === undefined ? '미수집' : formatCount(value)}</td>
              <td className="px-2 py-1 font-mono tabular-nums">{prior === undefined ? '미수집' : formatCount(prior)}</td>
              <td className="px-2 py-1 font-mono tabular-nums">{difference === undefined ? '비교 불가' : formatDifference(difference)}</td>
            </tr>;
          })}</tbody>
        </table>
      </div>
    </section>}
    {otelRecords.length > 0 && <details className="rounded border border-border">
      <summary className="cursor-pointer px-2 py-1 text-[10px] text-ink-3 focus-visible:ring-1 focus-visible:ring-accent">연결된 OTEL 사용량 근거 {otelRecords.length}건</summary>
      <pre className="max-h-64 overflow-auto border-t border-border-subtle bg-surface-sunk p-2 font-mono text-[10px] text-ink-2">{jsonText(usage?.otel)}</pre>
    </details>}
    <details className="rounded border border-border">
      <summary className="cursor-pointer px-2 py-1 text-[10px] text-ink-3 focus-visible:ring-1 focus-visible:ring-accent">사용량 원자료 보기</summary>
      <pre className="max-h-64 overflow-auto border-t border-border-subtle bg-surface-sunk p-2 font-mono text-[10px] text-ink-2">{jsonText(usage)}</pre>
    </details>
  </section>;
};

interface StructuredFieldProps { label: string; value: unknown; missingLabel: string }
const StructuredField = ({ label, value, missingLabel }: StructuredFieldProps) => <div className="min-w-0 border-b border-border-subtle py-2 last:border-b-0">
  <h4 className="mb-1 text-[10px] font-medium text-ink-3">{label}</h4>
  {value === undefined ? <p className="text-[11px] text-ink-3">{missingLabel}</p>
    : <pre className="max-h-48 min-w-0 overflow-auto whitespace-pre-wrap break-words rounded bg-surface-sunk p-2 font-mono text-[11px] text-ink-2">{value === '' ? '(빈 문자열)' : jsonText(value)}</pre>}
</div>;

interface StructuredContextProps { audit: AuditValue }
const contextMissingLabel = (audit: AuditValue): string => audit.state === 'not_collected' || audit.state === 'missing' ? '미수집'
  : audit.state === 'redacted' || audit.state === 'withheld' ? '가림'
    : audit.state === 'truncated' ? '잘림'
      : audit.state === 'not_queried' || audit.state === 'unqueried' ? '미조회'
        : audit.state === 'error' ? '오류'
          : '이 기록에 없음';
const fieldFrom = (objects: Array<Record<string, unknown> | undefined>, key: string): unknown => {
  for (const object of objects) if (object && Object.prototype.hasOwnProperty.call(object, key)) return object[key];
  return undefined;
};
const formatDuration = (value: unknown): unknown => typeof value === 'number' && Number.isFinite(value) ? `${formatCount(value)} ms` : value;

const HookContextSection = ({ audit }: StructuredContextProps) => {
  const root = recordValue(audit.value);
  const attachment = recordValue(root?.attachment);
  const sources = [attachment, root];
  const infos = fieldFrom(sources, 'hookInfos');
  const firstInfo = Array.isArray(infos) ? recordValue(infos[0]) : undefined;
  const getField = (key: string, fallbackKey?: string): unknown => {
    const value = fieldFrom(sources, key);
    return value !== undefined ? value : fallbackKey ? firstInfo?.[fallbackKey] : undefined;
  };
  const missingLabel = contextMissingLabel(audit);
  const rawInfos = Array.isArray(infos) ? infos.map(recordValue).filter((item): item is Record<string, unknown> => !!item) : [];
  return <section aria-label="훅 실행 문맥" className="border-b border-border-subtle py-3">
    <div className="mb-1 flex flex-wrap items-center gap-2">
      <h3 className="text-xs font-medium text-ink">훅 실행</h3>
      <span className="rounded border border-border px-1.5 py-0.5 text-[10px] text-ink-3">{stateLabel(audit.state)}</span>
    </div>
    {audit.reason && <p className="mb-2 break-words text-[10px] text-ink-3">{audit.reason}</p>}
    {getField('command', 'command') !== undefined && <StructuredField label="실행 명령" value={getField('command', 'command')} missingLabel={missingLabel} />}
    {getField('hookName') !== undefined && <StructuredField label="훅 이름" value={getField('hookName')} missingLabel={missingLabel} />}
    {getField('hookEvent') !== undefined && <StructuredField label="훅 이벤트" value={getField('hookEvent')} missingLabel={missingLabel} />}
    {getField('exitCode') !== undefined && <StructuredField label="종료 코드" value={getField('exitCode')} missingLabel={missingLabel} />}
    {getField('durationMs', 'durationMs') !== undefined && <StructuredField label="실행 시간" value={formatDuration(getField('durationMs', 'durationMs'))} missingLabel={missingLabel} />}
    {getField('stdout') !== undefined && <StructuredField label="stdout" value={getField('stdout')} missingLabel={missingLabel} />}
    {getField('stderr') !== undefined && <StructuredField label="stderr" value={getField('stderr')} missingLabel={missingLabel} />}
    {getField('hookAdditionalContext') !== undefined && <StructuredField label="추가 문맥" value={getField('hookAdditionalContext')} missingLabel={missingLabel} />}
    {getField('hookAdditionalContext') === undefined && getField('content') !== undefined && <StructuredField label="추가 문맥" value={getField('content')} missingLabel={missingLabel} />}
    {getField('hookAdditionalContext') === undefined && getField('content') === undefined && getField('response') !== undefined && <StructuredField label="추가 문맥" value={getField('response')} missingLabel={missingLabel} />}
    {getField('hookErrors') !== undefined && <StructuredField label="훅 오류" value={getField('hookErrors')} missingLabel={missingLabel} />}
    {rawInfos.length > 0 && <div className="mt-2 space-y-1">
      <h4 className="text-[10px] font-medium text-ink-3">훅별 기록</h4>
      {rawInfos.map((info, index) => <details key={`${String(info.command ?? 'hook')}:${index}`} className="rounded border border-border">
        <summary className="cursor-pointer px-2 py-1 text-[10px] text-ink-2 focus-visible:ring-1 focus-visible:ring-accent">{String(info.command ?? `훅 ${index + 1}`)}</summary>
        <div className="border-t border-border-subtle px-2">
          <StructuredField label="실행 명령" value={info.command} missingLabel={missingLabel} />
          {info.durationMs !== undefined && <StructuredField label="실행 시간" value={formatDuration(info.durationMs)} missingLabel={missingLabel} />}
          {info.promptText !== undefined && <StructuredField label="훅 프롬프트" value={info.promptText} missingLabel={missingLabel} />}
        </div>
      </details>)}
    </div>}
  </section>;
};

interface InstructionBodyProps { title: string; path?: string; body: AuditValue; expanded?: boolean }
const InstructionBody = ({ title, path, body, expanded = false }: InstructionBodyProps) => {
  const label = path ? `${title} · ${path}` : title;
  const exceptional = !['observed', 'collected', 'available'].includes(body.state);
  return <details open={expanded} className="rounded border border-border bg-surface">
    <summary className="cursor-pointer break-all px-3 py-2 text-[11px] text-ink-2 focus-visible:ring-1 focus-visible:ring-accent">{label}{exceptional && <span className="ml-2 text-ink-3">{stateLabel(body.state)}</span>}</summary>
    {body.reason && <p className="border-t border-border-subtle px-3 py-2 text-[11px] text-ink-3">{body.reason}</p>}
    {body.value !== undefined ? <pre className="max-h-[min(60vh,48rem)] overflow-auto whitespace-pre-wrap break-words border-t border-border-subtle bg-surface-sunk p-3 font-mono text-[11px] text-ink-2">{jsonText(body.value)}</pre>
      : <p className="border-t border-border-subtle px-3 py-2 text-[11px] text-ink-3">{exceptional ? stateLabel(body.state) : '본문은 이 기록에 없습니다.'}</p>}
  </details>;
};

const InstructionContextSection = ({ audit }: StructuredContextProps) => {
  const entries = extractTraceInstructions(audit);
  return <section aria-label="지시문" className="space-y-2 py-2">
    {entries.map((entry, index) => {
      const title = ({
        base_instructions: '기본 지시문', developer_instructions: '개발자 지시문', user_instructions: '사용자 지시문',
        agents_md: 'AGENTS.md', instruction_file: '지시문 파일', file: '지시문 파일', project: '프로젝트 지시문',
        developer: '개발자 지시문', session: '세션 지시문', user: '사용자 지시문', recorded: '기록된 지시문',
      } as Record<string, string>)[entry.kind] ?? `${entry.kind} 지시문`;
      return <InstructionBody key={`${entry.source_path}:${entry.path ?? index}`} title={title} path={entry.path} body={entry.body} expanded={index === 0} />;
    })}
    {entries.length === 0 && <AuditSection label="지시문" audit={audit} />}
  </section>;
};

const nestedAudit = (value: unknown, state: string): AuditValue | undefined => {
  const object = recordValue(value);
  return typeof object?.state === 'string' ? object as unknown as AuditValue : value === undefined ? undefined : { state, value };
};

const ExecutionSettingsSection = ({ audit }: StructuredContextProps) => {
  const root = recordValue(audit.value);
  const settings = nestedAudit(root?.settings, audit.state) ?? audit;
  const provenance = nestedAudit(root?.provenance, audit.state);
  return <div className="space-y-2 py-2">
    <AuditSection label="실행 설정" audit={settings} />
    {provenance && <details className="rounded border border-border">
      <summary className="cursor-pointer px-3 py-2 text-[11px] text-ink-2 focus-visible:ring-1 focus-visible:ring-accent">출처와 연결 근거</summary>
      <div className="border-t border-border-subtle px-3"><AuditSection label="기록 출처" audit={provenance} /></div>
    </details>}
  </div>;
};

const MetadataSection = ({ audit }: StructuredContextProps) => <AuditSection label="기록 내용" audit={audit} />;

interface EventContentProps { input: AuditValue; output: AuditValue; context: AuditValue }
const showEventAudit = (audit: AuditValue, includeMissing: boolean): boolean => {
  if (audit.state === 'not_applicable') return false;
  if (audit.value !== undefined) return true;
  if (['redacted', 'withheld', 'truncated', 'error'].includes(audit.state)) return true;
  return includeMissing && ['not_collected', 'missing', 'not_queried', 'unqueried'].includes(audit.state) && !!audit.reason;
};
const EventContentSection = ({ input, output, context }: EventContentProps) => {
  const hasInput = showEventAudit(input, true);
  const hasOutput = showEventAudit(output, true);
  const hasContext = showEventAudit(context, false);
  return <div>
    {hasInput && <MessageAuditSection label="입력" audit={input} />}
    {hasOutput && <MessageAuditSection label="결과" audit={output} />}
    {hasContext && <AuditSection label="추가 기록" audit={context} />}
    {!hasInput && !hasOutput && !hasContext && <p role="status" className="py-4 text-xs text-ink-3">이 기록에 표시할 본문이 없습니다. 원자료에서 확인할 수 있습니다.</p>}
  </div>;
};

interface AgentContextSectionProps { output: AuditValue; node: TraceNodeDetail['node']; input: AuditValue; audit: AuditValue }
const AgentContextSection = ({ node, input, output, audit }: AgentContextSectionProps) => {
  let inputObject = recordValue(input.value);
  if (!inputObject && typeof input.value === 'string') { try { inputObject = recordValue(JSON.parse(input.value)); } catch { /* Preserve non-JSON source text below. */ } }
  const root = recordValue(audit.value);
  const dispatch = recordValue(root?.dispatch);
  const parent = recordValue(root?.parent);
  const parentValue = recordValue(parent?.value) ?? parent;
  const history = recordValue(root?.history);
  const historyValue = recordValue(history?.value) ?? history;
  const prompt = inputObject?.prompt ?? inputObject?.instructions ?? inputObject?.task ?? dispatch?.prompt ?? dispatch?.message ?? dispatch?.instructions;
  const description = inputObject?.description ?? dispatch?.description;
  const agentType = inputObject?.subagent_type ?? inputObject?.agent_type ?? dispatch?.subagent_type ?? dispatch?.agent_type;
  const forkContext = dispatch?.fork_context;
  const forkTurns = dispatch?.fork_turns;
  const missingInput = contextMissingLabel(input);
  const parentMissing = parent?.state ? stateLabel(String(parent.state)) : contextMissingLabel(audit);
  const historyMissing = history?.state ? stateLabel(String(history.state)) : contextMissingLabel(audit);
  const parentFields = ['thread_id', 'parent_thread_id', 'forked_from_id', 'thread_source', 'evidence'].filter(key => parentValue?.[key] !== undefined);
  const historyFields = ['history_mode', 'history_base', 'copied_prefix', 'trigger_turn', 'fork_turns', 'inherited_prefix'].filter(key => historyValue?.[key] !== undefined);
  const directMetadata = root ? ['parent_thread_id', 'forked_from_id', 'thread_source'].filter(key => root[key] !== undefined) : [];
  const directHistory = root ? ['history_mode', 'history_base', 'copied_prefix', 'trigger_turn', 'fork_turns', 'inherited_prefix'].filter(key => root[key] !== undefined) : [];
  return <section aria-label="하위 에이전트 문맥" className="space-y-2 border-b border-border-subtle py-3">
    <div className="flex flex-wrap items-center gap-2">
      <h3 className="text-xs font-medium text-ink">하위 에이전트 문맥</h3>
      <span className="rounded border border-border px-1.5 py-0.5 text-[10px] text-ink-3">전달 프롬프트와 상속 이력은 각각 원천 근거로 표시</span>
    </div>
    {description !== undefined && <StructuredField label="전달 목적" value={description} missingLabel={missingInput} />}
    {agentType !== undefined && <StructuredField label="에이전트 종류" value={agentType} missingLabel={missingInput} />}
    <StructuredField label="전달 프롬프트" value={prompt} missingLabel={missingInput} />
    {forkContext !== undefined && <StructuredField label="전달된 분기 설정 · fork_context (실제 복사 범위 아님)" value={forkContext} missingLabel={missingInput} />}
    {forkTurns !== undefined && <StructuredField label="전달된 분기 설정 · fork_turns (실제 복사 범위 아님)" value={forkTurns} missingLabel={missingInput} />}
    <StructuredField label="상위 실행 노드" value={node.parent_id ?? undefined} missingLabel="부모 연결이 기록되지 않았습니다." />
    {parentFields.map(key => <StructuredField key={key} label={`상위 문맥 · ${key}`} value={parentValue?.[key]} missingLabel={parentMissing} />)}
    {directMetadata.filter(key => !parentFields.includes(key)).map(key => <StructuredField key={key} label={`상위 문맥 · ${key}`} value={root?.[key]} missingLabel={parentMissing} />)}
    {historyFields.map(key => <StructuredField key={key} label={`상속 이력 범위 · ${key}`} value={historyValue?.[key]} missingLabel={historyMissing} />)}
    {directHistory.filter(key => !historyFields.includes(key)).map(key => <StructuredField key={key} label={`상속 이력 범위 · ${key}`} value={root?.[key]} missingLabel={historyMissing} />)}
    {historyFields.length === 0 && directHistory.length === 0 && <StructuredField label="상속 이력 범위" value={undefined} missingLabel={historyMissing} />}
    {typeof parent?.reason === 'string' && <p className="break-words text-[10px] text-ink-3">상위 문맥 상태: {parent.reason}</p>}
    {typeof history?.reason === 'string' && <p className="break-words text-[10px] text-ink-3">상속 범위 상태: {history.reason}</p>}
    {input.value !== undefined && <details className="rounded border border-border">
      <summary className="cursor-pointer px-3 py-2 text-[11px] text-ink-2">원래 도구 인수 보기</summary>
      <div className="border-t border-border-subtle px-3"><AuditSection label="호출 인수" audit={input} /></div>
    </details>}
    {output.value !== undefined && <AuditSection label="호출 반환 결과" audit={output} />}
    {audit.reason && !parent?.reason && !history?.reason && <p className="break-words text-[10px] text-ink-3">{audit.reason}</p>}
  </section>;
};

interface RequestSectionProps {
  nodeId: string;
  request?: NativeRequest;
  sourceLocations?: TraceSourceLocation[];
  inputPageLoading?: boolean;
  inputPageError?: unknown;
  onLoadInputPage?: (nodeId: string, cursor?: string) => void;
  onCompareRequest?: (nodeId: string, previousRequestId?: string) => void;
  onOpenSource?: (sourceRef: string) => void;
}
const RequestSection = ({ nodeId, request, sourceLocations, inputPageLoading, inputPageError, onLoadInputPage, onCompareRequest, onOpenSource }: RequestSectionProps) => {
  if (!request) return <p role="status" className="py-4 text-xs text-ink-3">이 노드에 연결된 정확한 API 요청 원자료가 없습니다. 요청 본문은 추정하지 않습니다.</p>;
  return <RequestInputView key={nodeId} nodeId={nodeId} request={request} sourceLocations={sourceLocations}
      loading={inputPageLoading} error={inputPageError} onLoadPage={onLoadInputPage}
      onCompareRequest={onCompareRequest} onOpenSource={onOpenSource} />;
};

interface TraceNodeInspectorProps {
  recordActions?: (record: SessionRecord) => ReactNode;
  selectedNodeId?: string;
  detail?: TraceNodeDetail;
  loading: boolean;
  error: boolean;
  onRetry: () => void;
  timingDisabledReason?: string;
  onOpenTiming?: () => void;
  inputPageLoading?: boolean;
  inputPageError?: unknown;
  onLoadInputPage?: (nodeId: string, cursor?: string) => void;
  onCompareRequest?: (nodeId: string, previousRequestId?: string) => void;
  onOpenSource?: (sourceRef: string) => void;
}

interface TimingAnalysisActionProps { disabledReason?: string; onOpenTiming?: () => void }
const TimingAnalysisAction = ({ disabledReason, onOpenTiming }: TimingAnalysisActionProps) => {
  const reasonId = useId();
  const reason = disabledReason || (onOpenTiming ? undefined : '시간 분석 화면이 아직 연결되지 않았습니다.');
  const openTiming = () => onOpenTiming?.();
  return <div className="flex shrink-0 flex-col items-end gap-1">
    <button type="button" onClick={openTiming} disabled={!!reason}
      aria-describedby={reason ? reasonId : undefined}
      className="rounded border border-border px-2.5 py-1.5 text-[11px] font-medium text-ink-2 hover:bg-surface-sunk focus-visible:ring-1 focus-visible:ring-accent disabled:cursor-not-allowed disabled:text-ink-3">
      시간 분석
    </button>
    {reason && <p id={reasonId} className="max-w-48 text-right text-[10px] text-ink-3">{reason}</p>}
  </div>;
};

interface InspectorHeaderProps extends TimingAnalysisActionProps { title: string; showTiming: boolean }
const InspectorHeader = ({ title, showTiming, disabledReason, onOpenTiming }: InspectorHeaderProps) => <header className="flex shrink-0 items-start justify-between gap-3 border-b border-border-subtle px-4 py-3">
  <h2 className="min-w-0 text-sm font-medium text-ink">{title}</h2>
  {showTiming && <TimingAnalysisAction disabledReason={disabledReason} onOpenTiming={onOpenTiming} />}
</header>;

const TraceNodeInspector = ({ selectedNodeId, detail, loading, error, onRetry, timingDisabledReason, onOpenTiming,
  inputPageLoading, inputPageError, onLoadInputPage, onCompareRequest, onOpenSource, recordActions }: TraceNodeInspectorProps) => {
  const matched = detail?.node.node_id === selectedNodeId ? detail : undefined;
  const retry = () => onRetry();
  if (!selectedNodeId) return <section aria-label="선택 노드 상세" className="flex min-h-0 flex-1 flex-col">
    <InspectorHeader title="선택 노드 상세" showTiming={false} />
    <p role="status" className="px-4 py-5 text-xs text-ink-3">실행 트리나 시간축에서 노드를 선택하면 입력·출력과 근거를 표시합니다.</p>
  </section>;
  if (loading && !matched) return <section aria-label="선택 노드 상세" className="flex min-h-0 flex-1 flex-col">
    <InspectorHeader title="선택 노드 상세" showTiming disabledReason={timingDisabledReason} onOpenTiming={onOpenTiming} />
    <p role="status" className="px-4 py-5 text-xs text-ink-3">선택한 노드의 자료를 읽는 중…</p>
  </section>;
  if (error && !matched) return <section aria-label="선택 노드 상세" className="flex min-h-0 flex-1 flex-col">
    <InspectorHeader title="선택 노드 상세" showTiming disabledReason={timingDisabledReason} onOpenTiming={onOpenTiming} />
    <p role="alert" className="px-4 py-5 text-xs text-ink-3">선택한 노드의 자료를 읽지 못했습니다.<button type="button" onClick={retry} className="ml-2 text-accent underline focus-visible:ring-1 focus-visible:ring-accent">다시 시도</button></p>
  </section>;
  if (!matched) return <section aria-label="선택 노드 상세" className="flex min-h-0 flex-1 flex-col">
    <InspectorHeader title="선택 노드 상세" showTiming disabledReason={timingDisabledReason} onOpenTiming={onOpenTiming} />
    <p role="status" className="px-4 py-5 text-xs text-ink-3">선택 노드 상세가 아직 제공되지 않았습니다.</p>
  </section>;

  const duration = formatObservedDuration(matched.node);
  const rawRecords = matched.raw_records ?? [];
  const timestamp = matched.node.timestamp ?? matched.node.started_at ?? matched.node.ended_at;
  const hasTimestamp = !!timestamp && Number.isFinite(Date.parse(timestamp));
  const exceptionalStatus = ['error', 'failed', 'running'].includes(matched.node.status);
  const unknownStatus = matched.node.status === 'unknown';
  const exceptionalAvailability = ['redacted', 'withheld', 'truncated', 'error', 'unavailable'].includes(matched.node.availability);
  const evidence = matched.node.evidence;
  const presentation = traceNodePresentation(matched);
  const contentLabel = contentLabels[presentation.content];
  return <section aria-label="선택 노드 상세" className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
    <header className="flex shrink-0 items-start justify-between gap-3 border-b border-border-subtle px-4 py-3">
      <div className="min-w-0 flex-1">
        <p className="mb-1 text-[11px] text-ink-3">{traceNodeKindLabel(matched.node.kind)}</p>
        <h2 className="break-words text-sm font-medium text-ink">{matched.node.title}</h2>
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[10px] text-ink-3">
          {matched.node.role && <span>역할 · {matched.node.role}</span>}
          {matched.node.semantic && <span>의미 · {matched.node.semantic}</span>}
          {matched.node.source_kind && <span>원천 · {matched.node.source_kind}</span>}
          {exceptionalStatus && <span className={traceNodeStatusTone(matched.node.status)}>{traceNodeStatusLabel(matched.node.status)}</span>}
          {exceptionalAvailability && <span>{traceNodeAvailabilityLabel(matched.node.availability)}</span>}
          {hasTimestamp && <span className="font-mono tabular-nums">{traceNodeTimeLabel(timestamp)}</span>}
          {duration && <span className="font-mono tabular-nums">{duration}</span>}
        </div>
        {(matched.node.source_ref || evidence.length > 0 || rawRecords.length > 0 || unknownStatus || !hasTimestamp) && <details className="mt-2 text-[10px] text-ink-3">
          <summary className="cursor-pointer focus-visible:ring-1 focus-visible:ring-accent">출처와 연결 근거</summary>
          <dl className="mt-2 grid min-w-0 gap-x-3 gap-y-1 sm:grid-cols-[auto_minmax(0,1fr)]">
            {unknownStatus && <><dt>상태</dt><dd>{traceNodeStatusLabel(matched.node.status)}</dd></>}
            {!hasTimestamp && <><dt>관측 시각</dt><dd>{traceNodeTimeLabel(timestamp)}</dd></>}
            {matched.node.source_ref && <><dt>원자료 참조</dt><dd className="break-all font-mono">{matched.node.source_ref}</dd></>}
            {evidence.length > 0 && <><dt>연결 근거</dt><dd className="break-all font-mono">{evidence.join(', ')}</dd></>}
            {rawRecords.length > 0 && <><dt>연결된 원자료</dt><dd>{rawRecords.length}건</dd></>}
          </dl>
        </details>}
      </div>
      <TimingAnalysisAction disabledReason={timingDisabledReason} onOpenTiming={onOpenTiming} />
    </header>
    {matched.notices.length > 0 && <ul aria-label="자료 상태 알림" className="shrink-0 space-y-1 border-b border-border-subtle px-4 py-2 text-[10px] text-warning-strong">
      {matched.notices.map(notice => <li key={notice}>{noticeLabels[notice] ?? notice}</li>)}
    </ul>}
    <div role="tabpanel" id="trace-node-panel-content" aria-label={contentLabel} className="min-h-0 flex-1 overflow-y-auto px-4 py-2">
      {presentation.content === 'turn' && <MessageAuditSection label="입력" audit={matched.input} />}
      {presentation.content === 'response' && <MessageAuditSection label="응답" audit={matched.output} />}
      {presentation.content === 'response' && presentation.showRequest && <RequestSection nodeId={matched.node.node_id} request={matched.request}
        sourceLocations={matched.source_locations} inputPageLoading={inputPageLoading} inputPageError={inputPageError}
        onLoadInputPage={onLoadInputPage} onCompareRequest={onCompareRequest} onOpenSource={onOpenSource} />}
      {presentation.showUsage && matched.usage && <UsageComparison usage={matched.usage} standalone={false} />}
      {presentation.content === 'tool' && <div>
        <AuditSection label="입력" audit={matched.input} />
        <AuditSection label="결과" audit={matched.output} />
        {matched.context.value !== undefined && <AuditSection label="연결 문맥" audit={matched.context} />}
        {presentation.showRequest && <RequestSection nodeId={matched.node.node_id} request={matched.request}
          sourceLocations={matched.source_locations} inputPageLoading={inputPageLoading} inputPageError={inputPageError}
          onLoadInputPage={onLoadInputPage} onCompareRequest={onCompareRequest} onOpenSource={onOpenSource} />}
      </div>}
      {presentation.content === 'request' && <RequestSection nodeId={matched.node.node_id} request={matched.request}
        sourceLocations={matched.source_locations} inputPageLoading={inputPageLoading} inputPageError={inputPageError}
        onLoadInputPage={onLoadInputPage} onCompareRequest={onCompareRequest} onOpenSource={onOpenSource} />}
      {presentation.content === 'instructions' && <InstructionContextSection audit={matched.context} />}
      {presentation.content === 'context' && (matched.node.kind === 'agent'
        ? <AgentContextSection node={matched.node} input={matched.input} output={matched.output} audit={matched.context} />
        : <ExecutionSettingsSection audit={matched.context} />)}
      {presentation.content === 'metadata' && <MetadataSection audit={matched.context} />}
      {presentation.content === 'event' && <EventContentSection input={matched.input} output={matched.output} context={matched.context} />}
      {presentation.content === 'hook' && <HookContextSection audit={matched.context} />}
      {presentation.content === 'agent' && <AgentContextSection node={matched.node} input={matched.input} output={matched.output} audit={matched.context} />}
      {presentation.content === 'usage' && <p role="status" className="py-3 text-xs text-ink-3">사용량은 해당 모델 응답의 속성으로 표시됩니다.</p>}
      {presentation.showSource && <details className="mt-3 border-t border-border-subtle py-3">
        <summary className="cursor-pointer text-[11px] font-medium text-ink-2 focus-visible:ring-1 focus-visible:ring-accent">연결된 원자료 {rawRecords.length}건</summary>
        <div className="mt-2 space-y-2">
          {rawRecords.map((record, index) => <RawRecord key={record.uuid ?? `${record.record_type}:${record.ts}:${index}`} record={record} index={index} actions={recordActions?.(record)} />)}
        </div>
      </details>}
    </div>
  </section>;
};

export { TraceNodeInspector };
export type { TraceNodeInspectorProps };
