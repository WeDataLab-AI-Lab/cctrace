import { cn } from '@/lib/utils';

interface EventBadgeProps {
  name: string;
}

const COLORS: Record<string, string> = {
  'llm.completion': 'bg-cat-1/15 text-cat-1',
  'tool.use': 'bg-success-soft text-success-strong',
  'tool.result': 'bg-warning-soft text-warning-strong',
};

const EventBadge = ({ name }: EventBadgeProps) => (
  <span
    className={cn(
      'text-[10px] font-medium px-1.5 py-0.5 rounded',
      COLORS[name] || 'bg-surface-sunk text-ink-2'
    )}
  >
    {name}
  </span>
);

export { EventBadge };
