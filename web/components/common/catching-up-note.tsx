import { RefreshCw } from 'lucide-react';
import { cn } from '@/lib/utils';

interface CatchingUpNoteProps {
  /** Shown only while true; null or undefined is not known and shows nothing. */
  pending: boolean | null | undefined;
  message: string;
  className?: string;
}

/**
 * Says that numbers on the screen are still being rewritten in the background:
 * the usage rebuild an exclusion change queues, the server's token recount. One
 * component for every such note, so they cannot drift apart.
 */
const CatchingUpNote = ({ pending, message, className }: CatchingUpNoteProps) => {
  if (!pending) return null;
  return (
    <p
      role="status"
      className={cn(
        'flex items-center gap-2 rounded-lg border border-border bg-canvas px-3 py-2 text-[12px] text-ink-2',
        className,
      )}
    >
      <RefreshCw size={13} className="shrink-0 motion-safe:animate-spin text-ink-3" />
      <span>{message}</span>
    </p>
  );
};

export { CatchingUpNote };
