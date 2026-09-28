'use client';

import Link from 'next/link';
import { useQuery } from '@tanstack/react-query';
import { AlertCircle, Wrench } from 'lucide-react';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { fetchTaskSegments } from '@/lib/api';
import type { TaskSegment } from '@/lib/types';

const numberFormat = new Intl.NumberFormat('en-US');

const formatTokens = (tokens: number) => {
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(1)}M`;
  if (tokens >= 1_000) return `${(tokens / 1_000).toFixed(1)}K`;
  return numberFormat.format(tokens);
};

/** Session-scoped, not segment-scoped: tool_evidence asks whether the *session*
 *  left any tool record at all. Saying so keeps it from reading as "this stretch
 *  used no tools" (#670). Codex counts its calls from JSONL, which carries no
 *  outcome, so its zero failures are named as unobserved rather than printed. */
const segmentToolLabel = (segment: TaskSegment): string => {
  if (!segment.tool_evidence) return '이 세션에 도구 기록 없음';
  const calls = `${numberFormat.format(segment.tool_call_count)} calls`;
  if (!segment.tool_outcome_evidence) return `${calls} · 실패 미관측`;
  if (segment.tool_fail_count === 0) return calls;
  return `${calls} (${numberFormat.format(segment.tool_fail_count)} failed)`;
};

interface TaskSegmentModalProps {
  readonly taskType: string;
  readonly since: string;
  readonly until: string;
  readonly onClose: () => void;
}

export const TaskSegmentModal = ({ taskType, since, until, onClose }: TaskSegmentModalProps) => {
  const { data: page, isLoading, isError } = useQuery({
    queryKey: ['task-segments', since, until, taskType],
    queryFn: () => fetchTaskSegments(since, until, taskType),
  });

  const segments = page?.segments;

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[560px] max-w-[560px] gap-0 space-y-3.5 rounded-xl border border-border bg-surface p-6 shadow-2xl"
      >
        <header>
          <DialogTitle className="text-[14px] font-semibold capitalize text-ink">{taskType} work segments</DialogTitle>
          <p className="mt-1 text-[11px] text-ink-3">Sessions with a {taskType} prompt in this period. Prompt and command content is never shown here.</p>
          {/* The card that opened this counts prompts; this list counts work
              segments, and one segment can hold several such prompts. Saying so
              is the alternative to two numbers that quietly disagree (#669). */}
          {page && page.total > 0 && (
            <p className="mt-1 text-[11px] text-ink-3">
              작업 구간 {numberFormat.format(page.total)}개
              {page.truncated && ` 중 ${numberFormat.format(segments?.length ?? 0)}개 표시 (서버 상한)`}
              {' '}· 위 카드의 수는 프롬프트 수라 이 수와 다를 수 있습니다
            </p>
          )}
          {page && page.total > 0 && (
            <p className="mt-1 text-[11px] text-ink-3">
              구간 토큰은 모델 레코드만 셉니다. Codex 는 캐시 읽기를 뺀 값입니다.
            </p>
          )}
        </header>

        {isLoading && <p className="py-6 text-center text-[13px] text-ink-3">Loading…</p>}
        {isError && (
          <p className="flex items-center gap-2 text-[13px] text-danger"><AlertCircle size={15} /> Could not load segments.</p>
        )}
        {!isLoading && !isError && (segments?.length ?? 0) === 0 && (
          <p className="py-6 text-center text-[13px] text-ink-3">No segments found.</p>
        )}
        {(segments?.length ?? 0) > 0 && (
          <ul className="max-h-[420px] space-y-1.5 overflow-y-auto">
            {segments!.map((segment) => (
              <li key={`${segment.session_id}-${segment.start_ts}`} className="rounded border border-border px-3 py-2.5">
                <div className="flex items-center justify-between gap-3">
                  <Link
                    href={`/sessions?project_hash=${encodeURIComponent(segment.project_hash || '')}`}
                    className="truncate text-[12px] font-medium text-ink hover:underline"
                    title={segment.project_hash}
                  >
                    {segment.project_name || segment.project_hash || segment.session_id}
                  </Link>
                  {/* The timestamp is the link to the conversation itself: it is
                      already the thing that says "when", and a segment's whole
                      point is that stretch of the session. The project name keeps
                      its own link beside it, so the coarser route did not move. */}
                  <span className="flex shrink-0 items-baseline gap-1.5">
                    {/* The moment is only a moment when the session has usable
                        times. A bulk-converted file stamps every line with its own
                        run time, so this reads as work that happened then (#686). */}
                    {segment.timeline_collapsed && (
                      <span
                        className="rounded bg-warning-soft px-1 py-0.5 text-[10px] font-medium text-warning-strong"
                        title="이 세션의 기록이 한 순간에 몰려 있습니다 — 가져온 기록으로 보이며, 아래 시각은 실제 작업 시각이 아닙니다"
                      >
                        시각 불명
                      </span>
                    )}
                    <Link
                      href={`/sessions?session_id=${encodeURIComponent(segment.session_id)}&from=${encodeURIComponent(segment.start_ts)}`}
                      className={
                        segment.timeline_collapsed
                          ? 'text-[11px] text-ink-3 line-through hover:text-ink'
                          : 'text-[11px] text-ink-3 hover:text-ink hover:underline'
                      }
                      title="Open this stretch of the session"
                    >
                      {new Date(segment.start_ts).toLocaleString()}
                    </Link>
                  </span>
                </div>
                <div className="mt-1.5 flex items-center justify-between gap-3 text-[11px] text-ink-3">
                  <span className="flex items-center gap-1">
                    <Wrench size={12} />
                    {segmentToolLabel(segment)}
                  </span>
                  {/* A different contract from the Projects card's "기록 토큰":
                      this counts only the segment's model rows, and for Codex it
                      subtracts cache reads (postgres_task_segment_facts.go:255). */}
                  <span className="tabular-nums">구간 {formatTokens(segment.input_tokens)} in / {formatTokens(segment.output_tokens)} out</span>
                </div>
              </li>
            ))}
          </ul>
        )}

        <div className="flex justify-end pt-0.5">
          <Button className="h-[30px] px-3 text-[12px]" onClick={onClose}>닫기</Button>
        </div>
      </DialogContent>
    </Dialog>
  );
};

export { segmentToolLabel };
