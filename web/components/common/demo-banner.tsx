'use client';

import { useEffect, useState } from 'react';
import { ChevronDown, FlaskConical } from 'lucide-react';
import { DEMO_EVENT, type DemoNotice } from '@/lib/demo/runtime';
import { cn } from '@/lib/utils';

const RECORDED_AT = process.env.NEXT_PUBLIC_DEMO_RECORDED_AT ?? '';
const DOCS_URL = process.env.NEXT_PUBLIC_DEMO_DOCS_URL ?? '';

const recordedLabel = (iso: string): string => {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: 'Asia/Seoul', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
  }).format(t);
  return `${parts.replace(',', '')} KST`;
};

interface DemoBannerProps {
  className?: string;
}

/**
 * Tells a visitor of the static demo what they are looking at: a fictitious
 * team, frozen at the moment it was recorded, where nothing they change is
 * saved. It also says when a click asked for something the recording does not
 * hold, so an empty panel reads as a gap in the demo and not as a broken screen.
 */
const DemoBanner = ({ className }: DemoBannerProps) => {
  const [readOnly, setReadOnly] = useState(false);
  const [missing, setMissing] = useState(0);

  /** Counts what the demo runtime could not answer, from its window events. */
  useEffect(() => {
    const onNotice = (event: Event) => {
      const { kind } = (event as CustomEvent<DemoNotice>).detail;
      if (kind === 'read-only') setReadOnly(true);
      if (kind === 'missing') setMissing((n) => n + 1);
    };
    window.addEventListener(DEMO_EVENT, onNotice);
    return () => window.removeEventListener(DEMO_EVENT, onNotice);
  }, []);

  return (
    <div
      role="status"
      className={cn(
        'shrink-0 rounded-lg border border-border bg-canvas text-xs text-ink-2',
        className,
      )}
    >
      <details className="group">
        <summary className="flex min-h-11 cursor-pointer list-none items-center gap-2 rounded-lg px-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand/30 [&::-webkit-details-marker]:hidden">
          <FlaskConical size={14} className="shrink-0 text-ink-3" />
          <span className="min-w-0 flex-1 truncate font-medium">Demo · Sample data</span>
          {(readOnly || missing > 0) && <span className="shrink-0 text-ink-3">{readOnly ? 'Read-only' : `${missing} unavailable`}</span>}
          <ChevronDown size={16} className="shrink-0 text-ink-3 transition-transform group-open:rotate-180" aria-hidden />
        </summary>
        <div className="space-y-2 border-t border-border px-3 py-2 leading-relaxed">
          <p>Demo: a fictitious five-person team, recorded {recordedLabel(RECORDED_AT)}. Nothing you change here is saved.</p>
          {readOnly && <p className="text-ink-3">That action is not available in the demo.</p>}
          {missing > 0 && <p className="text-ink-3">{missing} view{missing === 1 ? ' was' : 's were'} not recorded.</p>}
          {DOCS_URL && <a href={DOCS_URL} className="inline-flex min-h-11 items-center font-medium text-brand hover:underline">cctrace docs</a>}
        </div>
      </details>
    </div>
  );
};

export { DemoBanner };
