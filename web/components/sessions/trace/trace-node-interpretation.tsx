'use client';

import { Fragment } from 'react';
import { cn } from '@/lib/utils';
import type { TraceInterpretation } from '@/lib/session-trace-node-types';
import { readableContentValue } from './trace-node-instructions';

/**
 * 하네스가 쓴 기록(슬래시 명령, 로컬 출력, system 종류, Codex가 덧붙인 항목)을 서버가 코드로 해석해 보내면
 * 여기서 문장과 사실 목록으로 바꿔 보여 준다. 원문은 아래 본문과 원자료에 그대로 남는다.
 */

const interpretationText: Record<string, string> = {
  slash_command: '사용자가 실행한 슬래시 명령 기록입니다. Claude Code는 명령 이름과 인자를 태그로 감싸 기록합니다.',
  local_command_caveat: '사용자가 쓴 글이 아닙니다. 바로 뒤의 로컬 명령 출력에 응답하지 말라고 Claude Code가 모델에게 덧붙이는 안내문입니다.',
  local_command_output: '슬래시 명령이 출력한 내용입니다.',
  local_command_error: '슬래시 명령이 오류로 출력한 내용입니다.',
  system_turn_duration: 'Claude Code가 요청 하나를 처리한 뒤 걸린 시간을 남긴 기록입니다.',
  system_compact_boundary: '대화가 길어져 Claude Code가 앞선 대화를 요약으로 압축한 지점입니다.',
  system_kind_not_collected: 'v0.7.66보다 오래된 cctrace 클라이언트가 수집한 시스템 기록입니다. 당시에는 기록 종류(subtype)와 본문을 저장하지 않아 어떤 기록인지 알 수 없습니다.',
  codex_message_items: 'Codex가 직접 덧붙인 항목입니다. Codex가 항목마다 기록한 종류로 나눠 보여 줍니다.',
};

const formatNumber = (value: unknown): string => typeof value === 'number' ? new Intl.NumberFormat('ko-KR').format(value) : String(value);
const orNone = (value: unknown): string => value === '' ? '없음' : String(value);

interface FactField {
  key: string;
  label: string;
  format?: (value: unknown) => string;
  mono?: boolean;
  shown?: (facts: Record<string, unknown>) => boolean;
}
const factFields: FactField[] = [
  { key: 'command', label: '명령', mono: true },
  { key: 'args', label: '인자', format: orNone, mono: true },
  { key: 'output', label: '출력', format: orNone, mono: true },
  { key: 'duration_ms', label: '소요 시간', format: value => `${formatNumber(value)} ms` },
  { key: 'message_count', label: '메시지 수', format: formatNumber },
  { key: 'trigger', label: '압축 방식', format: value => ({ auto: '자동', manual: '수동' }[String(value)] ?? String(value)) },
  { key: 'pre_tokens', label: '압축 전 토큰', format: formatNumber },
  { key: 'post_tokens', label: '압축 후 토큰', format: formatNumber },
  { key: 'parent', label: '앞선 기록' },
  { key: 'parent_uuid', label: '앞선 기록 ID', format: value => `${String(value)} (이 세션에서 찾지 못함)`, mono: true, shown: facts => facts.parent === undefined },
];

interface TraceInterpretationSectionProps { interpretation: TraceInterpretation }
const TraceInterpretationSection = ({ interpretation }: TraceInterpretationSectionProps) => {
  const sentence = interpretationText[interpretation.kind];
  const facts = interpretation.facts ?? {};
  const fields = factFields.filter(field => facts[field.key] !== undefined && (field.shown?.(facts) ?? true));
  if (!sentence && fields.length === 0) return null;
  return <section aria-label="기록 해석" className="border-b border-border-subtle py-3">
    <h3 className="text-xs font-medium text-ink">기록 해석</h3>
    {sentence && <p className="mt-1 break-words text-[11px] text-ink-2">{sentence}</p>}
    {fields.length > 0 && <dl className="mt-2 grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-[11px]">
      {fields.map(field => <Fragment key={field.key}>
        <dt className="text-ink-3">{field.label}</dt>
        <dd className={cn('max-h-48 min-w-0 overflow-auto whitespace-pre-wrap break-words text-ink', field.mono && 'font-mono')}>{(field.format ?? String)(facts[field.key])}</dd>
      </Fragment>)}
    </dl>}
  </section>;
};

interface CodexMessageItem { kind: string; label: string }
/** Codex가 블록마다 기록한 종류. 본문 블록 수와 맞을 때만 블록별로 나눈다. */
const codexMessageItems = (interpretation: TraceInterpretation | undefined, value: unknown): CodexMessageItem[] | undefined => {
  const items = interpretation?.kind === 'codex_message_items' ? interpretation.facts?.items : undefined;
  if (!Array.isArray(items) || !Array.isArray(value) || items.length !== value.length) return;
  return items.map((item: unknown) => {
    const object = item && typeof item === 'object' ? item as Record<string, unknown> : {};
    return { kind: String(object.kind ?? ''), label: String(object.label ?? object.kind ?? '') };
  });
};

const blockText = (block: unknown): string => {
  const readable = readableContentValue([block]);
  return typeof readable === 'string' ? readable : JSON.stringify(block, null, 2) ?? String(block);
};

interface CodexItemsSectionProps { items: CodexMessageItem[]; blocks: unknown[] }
const CodexItemsSection = ({ items, blocks }: CodexItemsSectionProps) => <section aria-label="Codex가 덧붙인 항목" className="space-y-2 py-3">
  {items.map((item, index) => <details key={`${item.kind}:${index}`} open={items.length === 1} className="rounded border border-border bg-surface">
    <summary className="cursor-pointer px-3 py-2 text-[11px] text-ink-2 focus-visible:ring-1 focus-visible:ring-accent">{item.label}</summary>
    <pre className="max-h-[min(60vh,48rem)] overflow-auto whitespace-pre-wrap break-words border-t border-border-subtle bg-surface-sunk p-3 font-mono text-[11px] text-ink-2">{blockText(blocks[index])}</pre>
  </details>)}
</section>;

export { TraceInterpretationSection, CodexItemsSection, codexMessageItems };
export type { CodexMessageItem };
