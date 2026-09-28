import { RefreshCw } from 'lucide-react';
import { cn } from '@/lib/utils';
import { USAGE_REBUILD_NOTE } from '@/lib/usage-rebuild';

interface UsageRebuildNoteProps {
  pending: boolean | null | undefined;
  className?: string;
}

const UsageRebuildNote = ({ pending, className }: UsageRebuildNoteProps) => {
  if (!pending) return null;
  return (
    <p
      role="status"
      className={cn(
        'flex items-center gap-2 rounded-lg border border-border bg-canvas px-3 py-2 text-[12px] text-ink-2',
        className,
      )}
    >
      <RefreshCw size={13} className="shrink-0 animate-spin text-ink-3" />
      <span>{USAGE_REBUILD_NOTE}</span>
    </p>
  );
};

export { UsageRebuildNote };
