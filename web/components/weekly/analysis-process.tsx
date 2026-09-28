import { ChevronDown, ChevronRight } from 'lucide-react';
import type { AIToolCall, AIToolCallStatus } from '@/lib/types';

const numberFormat = new Intl.NumberFormat('en-US');

/** Arguments as the server summarised them; never a raw payload (spec §3.3). */
const summarizeArgs = (args: Record<string, unknown>): string => {
  const entries = Object.entries(args);
  if (entries.length === 0) return '인자 없음';
  return entries.map(([key, value]) => `${key}=${typeof value === 'string' ? value : JSON.stringify(value)}`).join(', ');
};

const formatCallDuration = (ms: number): string => (ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}초`);

const UNFINISHED_LABELS: Record<Exclude<AIToolCallStatus, 'ok'>, string> = {
  running: '실행 중',
  failed: '실패',
  timeout: '시간 초과',
};

/** Rows and duration for a finished call; the status word otherwise, so a call
 *  that returned nothing never reads as "0 rows". */
const callOutcome = (call: AIToolCall): string => {
  const duration = call.duration_ms == null ? null : formatCallDuration(call.duration_ms);
  if (call.status !== 'ok') return [UNFINISHED_LABELS[call.status], duration].filter(Boolean).join(' · ');
  const rows = call.result_rows == null ? null : `결과 ${numberFormat.format(call.result_rows)}행`;
  return [rows, duration].filter(Boolean).join(' · ');
};

interface ToolCallListProps {
  toolCalls: AIToolCall[];
}

const ToolCallList = ({ toolCalls }: ToolCallListProps) => (
  <ol className="space-y-1 text-[12px]">
    {toolCalls.map((call) => (
      <li key={call.seq} className="flex flex-wrap items-baseline gap-x-2 text-ink-3">
        <span className="font-mono text-ink-2">{call.tool}</span>
        <span className="font-mono">{summarizeArgs(call.args)}</span>
        <span className="tabular-nums">{callOutcome(call)}</span>
      </li>
    ))}
  </ol>
);

interface AnalysisProcessProps {
  toolCalls: AIToolCall[];
  segmentsRead: number;
  /** Owned by the parent so a refetch or stream event never folds it (spec §3.5). */
  open: boolean;
  onToggle: () => void;
}

const AnalysisProcess = ({ toolCalls, segmentsRead, open, onToggle }: AnalysisProcessProps) => (
  <section>
    <button
      type="button"
      aria-expanded={open}
      onClick={onToggle}
      className="flex items-center gap-1 text-[12.5px] tabular-nums text-ink-2 hover:text-ink"
    >
      {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
      분석 과정 · 도구 호출 {numberFormat.format(toolCalls.length)}회 · 읽은 구간 {numberFormat.format(segmentsRead)}개
    </button>
    {open && (
      <div className="mt-2 pl-5">
        <ToolCallList toolCalls={toolCalls} />
      </div>
    )}
  </section>
);

export { AnalysisProcess, summarizeArgs, ToolCallList };
