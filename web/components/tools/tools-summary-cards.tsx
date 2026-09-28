import { SummaryCard } from '@/components/common/summary-card';

interface ToolsSummaryCardsProps {
  totalUse: number;
  totalSuccess: number;
  totalFail: number;
  overallRate: string;
}

const ToolsSummaryCards = ({
  totalUse,
  totalSuccess,
  totalFail,
  overallRate,
}: ToolsSummaryCardsProps) => (
  <div className="grid grid-cols-4 gap-4">
    <SummaryCard title="Total Uses" value={totalUse.toLocaleString()} subtitle="This month" />
    <SummaryCard title="Success" value={totalSuccess.toLocaleString()} />
    <SummaryCard title="Failures" value={totalFail.toLocaleString()} />
    <SummaryCard title="Success Rate" value={overallRate === '-' ? '-' : `${overallRate}%`} />
  </div>
);

export { ToolsSummaryCards };
