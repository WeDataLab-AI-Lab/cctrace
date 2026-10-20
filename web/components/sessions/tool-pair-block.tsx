'use client';

import { useState } from 'react';
import { cn } from '@/lib/utils';
import type { ToolPair } from './session-utils';

interface ToolPairBlockProps {
  pair: ToolPair;
  defaultOpen?: boolean;
  onInspect?: () => void;
  memoryKey?: string;
}

const toolPreview = (pair: ToolPair) => {
  try {
    const input: unknown = JSON.parse(pair.input);
    if (input && typeof input === 'object') {
      const fields = input as Record<string, unknown>;
      for (const key of ['command', 'cmd', 'file_path', 'path', 'description', 'prompt', 'query']) {
        if (typeof fields[key] === 'string') return fields[key].replace(/\s+/g, ' ').trim();
      }
    }
  } catch { /* Plain tool inputs remain readable without JSON. */ }
  return (pair.input || pair.result).replace(/\s+/g, ' ').trim();
};

const ToolPairBlock = ({ pair, defaultOpen = true, onInspect, memoryKey }: ToolPairBlockProps) => {
  const [open, setOpen] = useState(defaultOpen);
  const preview = toolPreview(pair);

  const handleToggle = () => setOpen(prev => !prev);

  return (
    <div data-tool-key={memoryKey} data-tool-open={open} className="min-w-0 border-b border-border-subtle text-xs">
      <button
        type="button"
        aria-expanded={open}
        onClick={handleToggle}
        className="flex min-w-0 w-full items-center gap-2 px-2 py-2 text-left text-ink-2 hover:bg-canvas"
      >
        <span className="text-[10px]">{open ? '▼' : '▶'}</span>
        <span className="flex-shrink-0 font-mono">{pair.name}</span>
        {pair.isError && <span className="text-danger text-[10px]">error</span>}
        {preview && <span title={preview} className="min-w-0 flex-1 truncate font-mono text-[11px] text-ink-2">{preview}</span>}
      </button>
      {open && (
        <div className="border-t border-border/50">
          {onInspect && <button type="button" onClick={onInspect} className="m-2 rounded border border-border px-2 py-1 text-ink-2 hover:text-brand">이 툴 호출 검사</button>}
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
          {!pair.endTs && !pair.result && <p className="px-2 py-1.5 text-ink-3">결과 미확인</p>}
        </div>
      )}
    </div>
  );
};

export { ToolPairBlock };
