'use client';

import { useState, Fragment } from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { cn } from '@/lib/utils';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import type { ToolFailure } from '@/lib/types';
import { formatRelativeTime } from '@/lib/format';
import { truncate } from './time-format';

interface FailureListProps {
  failures: ToolFailure[];
}

interface FailureAttrEntryProps {
  attrKey: string;
  value: unknown;
}

const FailureAttrEntry = ({ attrKey, value }: FailureAttrEntryProps) => {
  const text = typeof value === 'string' ? value : JSON.stringify(value, null, 2);
  const isError = attrKey.toLowerCase().includes('error') || attrKey.toLowerCase().includes('output');

  return (
    <div>
      <div className="text-[11px] font-medium text-ink-3 mb-1">{attrKey}</div>
      <pre
        className={cn(
          'text-[11px] bg-surface rounded border border-border p-2 overflow-x-auto max-h-[200px] overflow-y-auto whitespace-pre-wrap break-all',
          isError ? 'text-danger' : 'text-ink',
        )}
      >
        {truncate(text, 2000)}
      </pre>
    </div>
  );
};

const FailureList = ({ failures }: FailureListProps) => {
  const [expandedIdx, setExpandedIdx] = useState<number | null>(null);

  return (
    <Table className="text-sm">
      <TableHeader>
        <TableRow className="text-[11px] text-ink-3 border-b border-surface-sunk hover:bg-transparent">
          <TableHead className="py-1.5 text-left font-medium w-5 text-ink-3" />
          <TableHead className="py-1.5 text-left font-medium text-ink-3">Time</TableHead>
          <TableHead className="py-1.5 text-left font-medium text-ink-3">Session ID</TableHead>
          <TableHead className="py-1.5 text-left font-medium text-ink-3">User</TableHead>
          <TableHead className="py-1.5 text-left font-medium text-ink-3">Model</TableHead>
          <TableHead className="py-1.5 text-right font-medium text-ink-3">Duration</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {failures.map((f, i) => {
          const isOpen = expandedIdx === i;
          const attrs = f.attrs ?? {};
          const hasDetail = Object.keys(attrs).length > 0;
          const handleToggle = () => hasDetail && setExpandedIdx(isOpen ? null : i);

          return (
            <Fragment key={i}>
              <TableRow
                className={cn(
                  'border-b border-surface-sunk last:border-b-0 hover:bg-transparent',
                  hasDetail && 'cursor-pointer hover:bg-surface-sunk',
                  isOpen && 'bg-canvas',
                )}
                onClick={handleToggle}
              >
                <TableCell className="py-2 pl-1 text-ink-3">
                  {hasDetail && (isOpen ? <ChevronDown size={12} /> : <ChevronRight size={12} />)}
                </TableCell>
                <TableCell className="py-2 text-[12px] text-ink-2">{formatRelativeTime(f.ts)}</TableCell>
                <TableCell className="py-2 text-[12px] text-ink font-mono">
                  {f.session_id ? f.session_id.slice(0, 11) : '-'}
                </TableCell>
                <TableCell className="py-2 text-[12px] text-ink-2 font-mono">
                  {f.user_id ? f.user_id.slice(0, 8) : '-'}
                </TableCell>
                <TableCell className="py-2 text-[12px] text-ink-2">
                  {f.model ? f.model.replace('claude-', '').split('-20')[0] : '-'}
                </TableCell>
                <TableCell className="py-2 text-[12px] text-ink-2 text-right">
                  {f.duration_ms != null ? `${(f.duration_ms / 1000).toFixed(1)}s` : '-'}
                </TableCell>
              </TableRow>
              {isOpen && (
                <TableRow className="hover:bg-transparent">
                  <TableCell colSpan={6} className="px-4 py-3 bg-canvas border-b border-surface-sunk">
                    <div className="space-y-2 max-w-full">
                      {Object.entries(attrs).map(([key, val]) => (
                        <FailureAttrEntry key={key} attrKey={key} value={val} />
                      ))}
                    </div>
                  </TableCell>
                </TableRow>
              )}
            </Fragment>
          );
        })}
      </TableBody>
    </Table>
  );
};

export { FailureList };
