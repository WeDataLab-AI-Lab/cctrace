import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';

interface StatusBadgeProps {
  status: string;
}

const STATUS_CLASSES: Record<string, string> = {
  active: 'bg-success-soft text-success-strong',
  missing: 'bg-warning-soft text-warning-strong',
  deleted: 'bg-danger-soft text-danger-strong',
  unreadable: 'bg-cat-6/15 text-cat-6',
  archived: 'bg-surface-sunk text-ink-2',
};

const StatusBadge = ({ status }: StatusBadgeProps) => {
  return (
    <Badge
      variant="outline"
      className={cn(
        'rounded border-transparent px-1.5 py-0.5 text-[10px] font-medium',
        STATUS_CLASSES[status] ?? 'bg-surface-sunk text-ink-2'
      )}
    >
      {status}
    </Badge>
  );
};

export { StatusBadge };
