'use client';

import { cn } from '@/lib/utils';
import type { UserPrompt } from './session-utils';

interface UserPromptRailProps {
  prompts: UserPrompt[];
  onJump: (anchorId: string) => void;
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
const UserPromptRail = ({ prompts, onJump }: UserPromptRailProps) => {
  const handleJump = (anchorId: string) => () => onJump(anchorId);

  if (prompts.length === 0) {
    return <p className="px-6 py-4 text-sm text-ink-3">이 세션에는 사용자가 입력한 메시지가 없습니다</p>;
  }

  return (
    <ol className="space-y-1.5 bg-canvas px-6 py-4">
      {prompts.map((prompt, index) => (
        <li key={prompt.key}>
          <button
            type="button"
            onClick={handleJump(prompt.anchorId)}
            className="flex w-full cursor-pointer items-start gap-3 rounded-md border border-border-subtle bg-surface px-3 py-2 text-left transition-colors hover:border-brand hover:bg-brand-soft focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-ring)]"
          >
            <span className="mt-0.5 w-6 flex-shrink-0 font-mono text-[10px] text-ink-4">{index + 1}</span>
            <span className={cn('min-w-0 flex-1 whitespace-pre-wrap break-words text-[13px] leading-snug', prompt.command ? 'font-mono text-brand' : 'text-ink-1')}>
              {promptLabel(prompt)}
            </span>
          </button>
        </li>
      ))}
    </ol>
  );
};

export { UserPromptRail };
