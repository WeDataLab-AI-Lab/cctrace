import { SummaryCard } from '@/components/common/summary-card';
import { avgTokens, fmtTokens } from './usage-format';

interface UsageSummaryProps {
  pluginCalls: number;
  skillCalls: number;
  calls: number;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  loading?: boolean;
}

const fmtTokenFrame = (n: number) => fmtTokens(Math.round(n));

const UsageSummary = ({
  pluginCalls,
  skillCalls,
  calls,
  totalTokens,
  inputTokens,
  outputTokens,
  loading,
}: UsageSummaryProps) => (
  <section className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-6">
    <SummaryCard title="Claude Calls" value={pluginCalls.toLocaleString()} count={pluginCalls} loading={loading} />
    <SummaryCard title="Codex Calls" value={skillCalls.toLocaleString()} count={skillCalls} loading={loading} />
    <SummaryCard
      title="Avg Tokens"
      value={avgTokens(totalTokens, calls)}
      count={calls > 0 ? Math.round(totalTokens / calls) : 0}
      format={fmtTokenFrame}
      loading={loading}
    />
    <SummaryCard title="Total" value={fmtTokens(totalTokens)} count={totalTokens} format={fmtTokenFrame} loading={loading} />
    <SummaryCard title="Input" value={fmtTokens(inputTokens)} count={inputTokens} format={fmtTokenFrame} loading={loading} />
    <SummaryCard title="Output" value={fmtTokens(outputTokens)} count={outputTokens} format={fmtTokenFrame} loading={loading} />
  </section>
);

export { UsageSummary };
