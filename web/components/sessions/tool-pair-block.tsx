'use client';

import { useState } from 'react';
import { cn } from '@/lib/utils';
import type { ToolPair } from './session-utils';

interface ToolPairBlockProps {
  pair: ToolPair;
  defaultOpen?: boolean;
}

const ToolPairBlock = ({ pair, defaultOpen = true }: ToolPairBlockProps) => {
  const [open, setOpen] = useState(defaultOpen);
  const preview = (pair.result || pair.input).replace(/\s+/g, ' ').trim();

  const handleToggle = () => setOpen(prev => !prev);

  return (
    <div className="border border-border rounded text-xs mt-1">
      <button
        onClick={handleToggle}
        className="w-full flex items-center gap-1.5 px-2 py-1 text-left text-ink-3 hover:bg-canvas"
      >
        <span className="text-[10px]">{open ? '▼' : '▶'}</span>
        <span className="flex-shrink-0 font-mono">{pair.name}</span>
        {pair.isError && <span className="text-danger text-[10px]">error</span>}
        {preview && <span className="ml-auto min-w-0 truncate font-mono text-[10px] text-ink-4">{preview}</span>}
      </button>
      {open && (
        <div className="border-t border-border/50">
          {pair.input && (
            <pre className="px-2 py-1.5 bg-surface-sunk text-ink-2 whitespace-pre-wrap break-words max-h-[150px] overflow-y-auto font-mono">
              {pair.input}
            </pre>
          )}
          {pair.result && (
            <pre className={cn(
              'px-2 py-1.5 whitespace-pre-wrap break-words max-h-[150px] overflow-y-auto border-t border-dashed border-border/50 font-mono',
              pair.isError ? 'bg-danger-soft text-danger-strong' : 'bg-success-soft text-success-strong',
            )}>
              {pair.result}
            </pre>
          )}
        </div>
      )}
    </div>
  );
};

export { ToolPairBlock };
