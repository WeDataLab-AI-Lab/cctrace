'use client';

import { useEffect, useState } from 'react';
import { FlaskConical } from 'lucide-react';
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
    <p
      role="status"
      className={cn(
        'flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-border bg-canvas px-3 py-2 text-[12px] text-ink-2',
        className,
      )}
    >
      <FlaskConical size={13} className="shrink-0 text-ink-3" />
      <span>
        Demo: a fictitious five-person team, recorded {recordedLabel(RECORDED_AT)}. Nothing you change here is saved.
      </span>
      {readOnly && <span className="text-ink-3">That action is not available in the demo.</span>}
      {missing > 0 && <span className="text-ink-3">{missing} view{missing === 1 ? ' was' : 's were'} not recorded.</span>}
      {DOCS_URL && (
        <a href={DOCS_URL} className="ml-auto font-medium text-brand hover:underline">
          cctrace docs
        </a>
      )}
    </p>
  );
};

export { DemoBanner };
