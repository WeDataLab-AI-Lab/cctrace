import { SummaryCard } from '@/components/common/summary-card';
import type { ViewMode } from '@/components/common/trend-chart';
import { fmt } from './overview-helpers';

interface SummaryCardsProps {
  viewMode: ViewMode;
  totalCost: number;
  totalTokens: number;
  activeUsers: number;
  totalEvents: number;
  subtitle: string;
  loading?: boolean;
}

const fmtCost = (n: number) => `$${n.toFixed(2)}`;
const fmtFrame = (n: number) => fmt(Math.round(n));
// These two land on the raw number (no separators), so their frames match it.
const fmtPlain = (n: number) => String(Math.round(n));

const SummaryCards = ({
  viewMode,
  totalCost,
  totalTokens,
  activeUsers,
  totalEvents,
  subtitle,
  loading,
}: SummaryCardsProps) => {
  const isToken = viewMode === 'token';
  return (
    <div className="grid grid-cols-4 gap-4">
      {/* Keyed by metric: switching cost/token view swaps what the card measures, and
          counting from dollars to tokens would pass through nonsense in between. */}
      <SummaryCard
        key={isToken ? 'tokens' : 'cost'}
        compact
        title={isToken ? 'Total Tokens' : 'Total Cost'}
        value={isToken ? fmt(totalTokens) : fmtCost(totalCost)}
        count={isToken ? totalTokens : totalCost}
        format={isToken ? fmtFrame : fmtCost}
        loading={loading}
        subtitle={subtitle}
        note="API 사용량 기반 추정치이며, 구독 요금제 사용시 별도 금액이 사전결제됩니다."
      />
      <SummaryCard
        key={isToken ? 'cost' : 'tokens'}
        compact
        title={isToken ? 'Total Cost' : 'Total Tokens'}
        value={isToken ? fmtCost(totalCost) : fmt(totalTokens)}
        count={isToken ? totalCost : totalTokens}
        format={isToken ? fmtCost : fmtFrame}
        loading={loading}
        subtitle={subtitle}
      />
      <SummaryCard compact title="Active Users" value={activeUsers} format={fmtPlain} loading={loading} subtitle={subtitle} />
      <SummaryCard compact title="Total Events" value={totalEvents} format={fmtPlain} loading={loading} subtitle={subtitle} />
    </div>
  );
};

export { SummaryCards };
