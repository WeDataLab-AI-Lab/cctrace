'use client';

import { cn } from '@/lib/utils';
import type { UserPrompt } from './session-utils';

interface UserPromptRailProps {
  prompts: UserPrompt[];
  onJump: (anchorId: string) => void;
  rangeLabel?: string;
  /** Open the request's execution trace. Omitted when the session has no trace surface. */
  onOpenTrace?: (turnKey: string) => void;
}

const promptLabel = (prompt: UserPrompt): string => {
  if (prompt.command) return `/${prompt.command}${prompt.commandArgs ? ` ${prompt.commandArgs}` : ''}`;
  return prompt.text.trim();
};

/**
 * The person's own turns, in order, as a way back into a long session.
 *
 * A session read end to end is mostly the assistant's output; what the reader is usually
 * looking for is "where did I ask about X". Listing the prompts alone turns that from a
 * scroll into a scan, and clicking one returns to the full conversation at that point
 * rather than replacing it — the surrounding turns are the reason to go there.
 */
const UserPromptRail = ({ prompts, onJump, rangeLabel, onOpenTrace }: UserPromptRailProps) => {
  const handleJump = (anchorId: string) => () => onJump(anchorId);

  if (prompts.length === 0) {
    return <p className="px-6 py-4 text-sm text-ink-3">이 세션에는 사용자가 입력한 메시지가 없습니다</p>;
  }

  return (
    <ol className="space-y-1.5 bg-canvas px-6 py-4">
      {rangeLabel && <li className="pb-1 text-[10px] text-ink-3">{rangeLabel}</li>}
      {prompts.map((prompt, index) => (
        <li key={prompt.key}>
          <div className="flex items-start gap-3 rounded-md border border-border-subtle bg-surface px-3 py-2 transition-colors hover:border-brand focus-within:ring-2 focus-within:ring-[var(--accent-ring)]">
            <button
              type="button"
              onClick={handleJump(prompt.anchorId)}
              className="flex min-w-0 flex-1 cursor-pointer items-start gap-3 text-left focus-visible:outline-none"
            >
              <span className="mt-0.5 w-6 flex-shrink-0 font-mono text-[10px] text-ink-4">{prompt.requestNo ?? index + 1}</span>
              <span className={cn('min-w-0 flex-1 whitespace-pre-wrap break-words text-[13px] leading-snug', prompt.command ? 'font-mono text-brand' : 'text-ink-1')}>
                {promptLabel(prompt)}
              </span>
            </button>
            {onOpenTrace && <button type="button" onClick={() => onOpenTrace(prompt.anchorId)} title="이 요청의 실행 기록을 엽니다" className="shrink-0 rounded px-1.5 py-0.5 text-[10px] text-ink-3 hover:bg-surface-sunk hover:text-ink">실행</button>}
          </div>
        </li>
      ))}
    </ol>
  );
};

export { UserPromptRail };
