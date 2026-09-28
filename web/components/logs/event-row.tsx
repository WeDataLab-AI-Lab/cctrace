'use client';

import { useState } from 'react';
import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';
import type { OtelEvent } from '@/lib/types';
import { EventBadge } from './event-badge';
import { formatRelativeTime } from '@/lib/format';
import { fmt, formatTs } from './logs-helpers';

interface EventRowProps {
  event: OtelEvent;
}

interface DetailItemProps {
  label: string;
  children: ReactNode;
}

const DetailItem = ({ label, children }: DetailItemProps) => (
  <div>
    <span className="text-ink-3">{label}:</span> {children}
  </div>
);

const EventRow = ({ event }: EventRowProps) => {
  const [open, setOpen] = useState(false);

  const handleToggle = () => setOpen(!open);

  return (
    <>
      <tr
        onClick={handleToggle}
        className="border-b border-border hover:bg-canvas cursor-pointer transition-colors"
      >
        <td className="py-2 px-3 text-[12px] text-ink-3 font-mono whitespace-nowrap">{formatTs(event.ts)}</td>
        <td className="py-2 px-3"><EventBadge name={event.event_name} /></td>
        <td className="py-2 px-3 text-[12px] text-ink-2">{event.model?.replace('claude-', '') || '-'}</td>
        <td className="py-2 px-3 text-[12px] text-ink-2 font-mono truncate max-w-[100px]">{event.session_id?.slice(0, 8) || '-'}</td>
        <td className="py-2 px-3 text-[12px] text-ink-2">{event.user_id?.slice(0, 8) || '-'}</td>
        <td className="py-2 px-3 text-[12px] text-ink text-right tabular-nums">
          {event.input_tokens != null ? fmt(event.input_tokens) : '-'}
        </td>
        <td className="py-2 px-3 text-[12px] text-ink text-right tabular-nums">
          {event.output_tokens != null ? fmt(event.output_tokens) : '-'}
        </td>
        <td className="py-2 px-3 text-[12px] text-ink text-right tabular-nums">
          {event.cost_usd != null ? `$${event.cost_usd.toFixed(4)}` : '-'}
        </td>
        <td className="py-2 px-3 text-[12px] text-ink-3 text-right">{formatRelativeTime(event.ts)}</td>
      </tr>
      {open && (
        <tr className="border-b border-border">
          <td colSpan={9} className="px-3 py-3 bg-canvas">
            <div className="grid grid-cols-3 gap-x-8 gap-y-2 text-[11px]">
              {event.tool_name && (
                <DetailItem label="Tool">
                  <span className="text-ink font-medium">{event.tool_name}</span>
                </DetailItem>
              )}
              {event.tool_decision && (
                <DetailItem label="Decision">
                  <span className="text-ink">{event.tool_decision}</span>
                </DetailItem>
              )}
              {event.tool_success != null && (
                <DetailItem label="Success">
                  <span className={cn(event.tool_success ? 'text-success' : 'text-danger')}>
                    {event.tool_success ? 'Yes' : 'No'}
                  </span>
                </DetailItem>
              )}
              {event.duration_ms != null && (
                <DetailItem label="Duration">
                  <span className="text-ink">{event.duration_ms}ms</span>
                </DetailItem>
              )}
              {event.cache_read_tokens != null && (
                <DetailItem label="Cache Read">
                  <span className="text-ink">{fmt(event.cache_read_tokens)}</span>
                </DetailItem>
              )}
              {event.cache_create_tokens != null && (
                <DetailItem label="Cache Create">
                  <span className="text-ink">{fmt(event.cache_create_tokens)}</span>
                </DetailItem>
              )}
              {event.speed && (
                <DetailItem label="Speed">
                  <span className="text-ink">{event.speed}</span>
                </DetailItem>
              )}
              {event.service_version && (
                <DetailItem label="Version">
                  <span className="text-ink font-mono">{event.service_version}</span>
                </DetailItem>
              )}
              {event.prompt_id && (
                <DetailItem label="Prompt">
                  <span className="text-ink font-mono">{event.prompt_id.slice(0, 12)}</span>
                </DetailItem>
              )}
              {event.login_email && (
                <DetailItem label="Login">
                  <span className="text-ink">{event.login_email}</span>
                </DetailItem>
              )}
            </div>
            {event.attrs && Object.keys(event.attrs).length > 0 && (
              <details className="mt-2">
                <summary className="text-[10px] text-ink-3 cursor-pointer hover:text-ink-2">Attributes</summary>
                <pre className="mt-1 text-[10px] text-ink-2 bg-surface rounded p-2 border border-border overflow-x-auto max-h-[200px]">
                  {JSON.stringify(event.attrs, null, 2)}
                </pre>
              </details>
            )}
          </td>
        </tr>
      )}
    </>
  );
};

export { EventRow };
