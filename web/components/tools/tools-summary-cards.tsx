import { SummaryCard } from '@/components/common/summary-card';

interface ToolsSummaryCardsProps {
  totalUse: number;
  totalSuccess: number;
  totalFail: number;
  overallRate: string;
  loading?: boolean;
}

const fmtRate = (n: number) => `${n.toFixed(1)}%`;

const ToolsSummaryCards = ({
  totalUse,
  totalSuccess,
  totalFail,
  overallRate,
  loading,
}: ToolsSummaryCardsProps) => (
  <div className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
    <SummaryCard title="Total Uses" value={totalUse.toLocaleString()} count={totalUse} loading={loading} subtitle="This month" />
    <SummaryCard title="Success" value={totalSuccess.toLocaleString()} count={totalSuccess} loading={loading} />
    <SummaryCard title="Failures" value={totalFail.toLocaleString()} count={totalFail} loading={loading} />
    <SummaryCard
      title="Success Rate"
      value={overallRate === '-' ? '-' : `${overallRate}%`}
      count={overallRate === '-' ? 0 : Number(overallRate)}
      format={fmtRate}
      loading={loading}
    />
  </div>
);

export { ToolsSummaryCards };
