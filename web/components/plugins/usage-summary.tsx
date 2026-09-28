import { SummaryCard } from '@/components/common/summary-card';
import { avgTokens, fmtTokens } from './usage-format';

interface UsageSummaryProps {
  pluginCalls: number;
  skillCalls: number;
  calls: number;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
}

const UsageSummary = ({
  pluginCalls,
  skillCalls,
  calls,
  totalTokens,
  inputTokens,
  outputTokens,
}: UsageSummaryProps) => (
  <section className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-6">
    <SummaryCard title="Claude Calls" value={pluginCalls.toLocaleString()} />
    <SummaryCard title="Codex Calls" value={skillCalls.toLocaleString()} />
    <SummaryCard title="Avg Tokens" value={avgTokens(totalTokens, calls)} />
    <SummaryCard title="Total" value={fmtTokens(totalTokens)} />
    <SummaryCard title="Input" value={fmtTokens(inputTokens)} />
    <SummaryCard title="Output" value={fmtTokens(outputTokens)} />
  </section>
);

export { UsageSummary };
