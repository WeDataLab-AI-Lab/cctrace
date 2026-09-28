import { Skeleton } from '@/components/ui/skeleton';
import { cn } from '@/lib/utils';

interface ChartSkeletonProps {
  className?: string;
  variant?: 'trend' | 'pie' | 'bar';
}

const TREND_LINES = ['top-[18%]', 'top-[36%]', 'top-[54%]', 'top-[72%]'];
const TREND_BARS = [
  'left-[5%] h-[28%]',
  'left-[15%] h-[44%]',
  'left-[25%] h-[34%]',
  'left-[35%] h-[62%]',
  'left-[45%] h-[26%]',
  'left-[55%] h-[48%]',
  'left-[65%] h-[38%]',
  'left-[75%] h-[56%]',
  'left-[85%] h-[30%]',
];
const BAR_ROWS = ['w-[86%]', 'w-[68%]', 'w-[74%]', 'w-[52%]', 'w-[38%]', 'w-[27%]'];

const TrendSkeleton = () => (
  <div className="relative h-full overflow-hidden rounded-lg border border-border-subtle bg-surface-2 px-5 py-4">
    <Skeleton className="absolute bottom-8 left-10 top-4 w-px" />
    <Skeleton className="absolute bottom-8 left-10 right-4 h-px" />
    {TREND_LINES.map((line) => (
      <Skeleton key={line} className={cn('absolute left-10 right-4 h-px', line)} />
    ))}
    {TREND_BARS.map((bar) => (
      <Skeleton key={bar} className={cn('absolute bottom-8 w-[3.5%] rounded-t-sm', bar)} />
    ))}
    <div className="absolute bottom-2 left-1/2 flex -translate-x-1/2 gap-2">
      <Skeleton className="h-2 w-12 rounded-full" />
      <Skeleton className="h-2 w-10 rounded-full" />
      <Skeleton className="h-2 w-14 rounded-full" />
    </div>
  </div>
);

const PieSkeleton = () => (
  <div className="flex h-full min-h-[200px] flex-col items-center justify-center gap-5 rounded-lg border border-border-subtle bg-surface-2">
    <div className="relative size-32">
      <Skeleton className="absolute inset-0 rounded-full" />
      <div className="absolute inset-8 rounded-full bg-surface" />
    </div>
    <div className="flex flex-wrap justify-center gap-3">
      <Skeleton className="h-2 w-16 rounded-full" />
      <Skeleton className="h-2 w-14 rounded-full" />
      <Skeleton className="h-2 w-20 rounded-full" />
    </div>
  </div>
);

const BarSkeleton = () => (
  <div className="flex h-full min-h-[220px] flex-col justify-center gap-4 rounded-lg border border-border-subtle bg-surface-2 px-5 py-4">
    {BAR_ROWS.map((row, index) => (
      <div key={row} className="flex items-center gap-3">
        <Skeleton className="h-3 w-16 rounded-full" />
        <Skeleton className={cn('h-5 rounded-sm', row, index === 0 && 'h-6')} />
      </div>
    ))}
    <div className="mt-2 flex justify-center gap-3">
      <Skeleton className="h-2 w-14 rounded-full" />
      <Skeleton className="h-2 w-16 rounded-full" />
      <Skeleton className="h-2 w-12 rounded-full" />
    </div>
  </div>
);

const ChartSkeleton = ({ className, variant = 'trend' }: ChartSkeletonProps) => {
  return (
    <div aria-busy="true" className={cn('w-full', className)}>
      {variant === 'pie' && <PieSkeleton />}
      {variant === 'bar' && <BarSkeleton />}
      {variant === 'trend' && <TrendSkeleton />}
    </div>
  );
};

export { ChartSkeleton };
