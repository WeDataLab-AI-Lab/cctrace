import Link from 'next/link';
import type { AIReportItem, AIReportItemMeta } from '@/lib/types';
import { ReportBlockHeader, reportCardClass } from './report-summary';

const numberFormat = new Intl.NumberFormat('en-US');

/** `9/14 06:02` in the reader's zone. */
const formatReportTime = (iso: string, timeZone: string): string => {
  const parts = Object.fromEntries(
    new Intl.DateTimeFormat('en-US', {
      timeZone,
      month: 'numeric',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      hourCycle: 'h23',
    })
      .formatToParts(new Date(iso))
      .map((part) => [part.type, part.value]),
  );
  return `${parts.month}/${parts.day} ${parts.hour}:${parts.minute}`;
};

/** Same route as the task segment modal's timestamp link. */
const segmentHref = (meta: AIReportItemMeta): string =>
  `/sessions?session_id=${encodeURIComponent(meta.session_id)}&from=${encodeURIComponent(meta.start_ts)}`;

/** The rule-derived row under a model-written title. Built from the server's
 *  segment lookup only; duration, commits and token sums are left out (spec §3.3). */
const metaLine = (meta: AIReportItemMeta, timeZone: string): string => {
  // Codex calls are counted from its JSONL tool_call rows, but JSONL records no
  // outcome, so its failure count of 0 would read as "none failed".
  const tools = [
    `도구 ${numberFormat.format(meta.tool_call_count)}`,
    meta.agent === 'codex' ? '실패 미관측' : `실패 ${numberFormat.format(meta.tool_fail_count)}`,
  ];
  return [
    meta.project_name,
    formatReportTime(meta.start_ts, timeZone),
    `입력 기록 ${numberFormat.format(meta.typed_turn_count)}`,
    ...tools,
  ].join(' · ');
};

interface ReportItemsProps {
  items: AIReportItem[];
  timeZone: string;
}

const ReportItems = ({ items, timeZone }: ReportItemsProps) => (
  <section className={reportCardClass}>
    <ReportBlockHeader title="다시 볼 작업" tag="AI 선정" />
    {items.length === 0 ? (
      <p className="mt-3 text-[13px] text-ink-3">선정한 작업 없음</p>
    ) : (
      <ol className="mt-3 space-y-4">
        {items.map((item, position) => (
          <li key={item.segment_id} className="grid grid-cols-[1.25rem_1fr] gap-x-2">
            <span className="text-[13px] tabular-nums text-ink-3">{position + 1}</span>
            <article className="space-y-1">
              <h4 className="text-[14px] font-medium text-ink">{item.title}</h4>
              <p data-meta="true" className="text-[12px] tabular-nums text-ink-3">
                {metaLine(item.meta, timeZone)}
              </p>
              <p className="text-[13px] text-ink-2">선정 이유: {item.reason}</p>
              <Link href={segmentHref(item.meta)} className="inline-block text-[12.5px] text-ink-2 hover:text-ink hover:underline">
                구간 열기 →
              </Link>
            </article>
          </li>
        ))}
      </ol>
    )}
  </section>
);

export { formatReportTime, ReportItems, segmentHref };
