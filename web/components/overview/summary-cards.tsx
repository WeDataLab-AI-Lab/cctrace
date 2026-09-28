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
}

const SummaryCards = ({
  viewMode,
  totalCost,
  totalTokens,
  activeUsers,
  totalEvents,
  subtitle,
}: SummaryCardsProps) => {
  const isToken = viewMode === 'token';
  return (
    <div className="grid grid-cols-4 gap-4">
      <SummaryCard
        title={isToken ? 'Total Tokens' : 'Total Cost'}
        value={isToken ? fmt(totalTokens) : `$${totalCost.toFixed(2)}`}
        subtitle={subtitle}
        note="API 사용량 기반 추정치이며, 구독 요금제 사용시 별도 금액이 사전결제됩니다."
      />
      <SummaryCard
        title={isToken ? 'Total Cost' : 'Total Tokens'}
        value={isToken ? `$${totalCost.toFixed(2)}` : fmt(totalTokens)}
        subtitle={subtitle}
      />
      <SummaryCard title="Active Users" value={activeUsers} subtitle={subtitle} />
      <SummaryCard title="Total Events" value={totalEvents} subtitle={subtitle} />
    </div>
  );
};

export { SummaryCards };
