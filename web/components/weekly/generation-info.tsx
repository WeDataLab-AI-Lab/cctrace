import type { AIReport, AIReportUsage } from '@/lib/types';
import { formatReportTime } from './report-items';

const RUNTIME_LABELS: Record<string, string> = {
  'codex-app-server': 'Codex app-server',
};

const formatTokenCount = (tokens: number): string => {
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(1).replace(/\.0$/, '')}M`;
  if (tokens >= 1_000) return `${Math.round(tokens / 1_000)}K`;
  return String(tokens);
};

const formatDuration = (ms: number): string => {
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}초`;
  return `${Math.floor(seconds / 60)}분 ${seconds % 60}초`;
};

/** A runtime that did not report tokens is not a run that spent none (spec §3.3). */
const usageLabel = (usage: AIReportUsage): string => {
  if (!usage.reported || usage.input_tokens == null || usage.output_tokens == null) return '토큰 미보고';
  return `입력 ${formatTokenCount(usage.input_tokens)} / 출력 ${formatTokenCount(usage.output_tokens)}`;
};

interface GenerationInfoProps {
  report: AIReport;
  inProgress: boolean;
  timeZone: string;
}

/** Facts about how this report was made. The re-analyse action used to end this
 *  row; it now sits in the summary header, where the report starts. */
const GenerationInfo = ({ report, inProgress, timeZone }: GenerationInfoProps) => {
  const generatedAt = formatReportTime(report.generated_at, timeZone);
  const facts = [
    `생성 ${generatedAt}`,
    RUNTIME_LABELS[report.runtime] ?? report.runtime,
    // Empty when no model was configured and the runtime did not name the one it used.
    report.model || '기본 모델',
    usageLabel(report.usage),
    formatDuration(report.duration_ms),
  ].filter(Boolean);

  return (
    <footer className="flex flex-wrap items-center justify-between gap-3 text-[12px] tabular-nums text-ink-3">
      <p>
        {facts.join(' · ')}
        {inProgress && ` · 기간 진행 중 · ${generatedAt} 까지`}
      </p>
    </footer>
  );
};

export { formatDuration, formatTokenCount, GenerationInfo };
