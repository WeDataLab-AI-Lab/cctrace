import { cn } from '@/lib/utils';

interface SummaryCardProps {
  title: string;
  value: string | number;
  subtitle?: string;
  note?: string;
  className?: string;
}

const SummaryCard = ({ title, value, subtitle, note, className }: SummaryCardProps) => {
  return (
    <div className={cn('bg-surface rounded-xl border border-border shadow-[var(--sh-sm)] py-5 px-6 group', className)}>
      <p className="text-[13px] text-ink-3 mb-2">{title}</p>
      <p className="text-[28px] font-semibold text-ink tabular-nums">{value}</p>
      {subtitle && <p className="text-[12px] text-ink-3 mt-1">{subtitle}</p>}
      {note && (
        <p className="text-[10px] text-ink-4 mt-2 leading-tight opacity-0 group-hover:opacity-100 transition-opacity">
          {note}
        </p>
      )}
    </div>
  );
};

export { SummaryCard };
