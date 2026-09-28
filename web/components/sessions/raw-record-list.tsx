'use client';

import type { SessionRecord } from '@/lib/types';
import { cn } from '@/lib/utils';
import { groupFilesByFile, keyRecords } from './session-utils';

interface IndividualViewProps {
  records: SessionRecord[];
}

const fileHeading = (sourceFile: string, agentId?: string, isSidechain?: boolean): string => {
  if (sourceFile) return sourceFile;
  if (isSidechain && agentId) return `subagent · ${agentId}`;
  return '(파일 정보 없음)';
};

// Individual view: each jsonl file shown separately, records in original form.
const IndividualView = ({ records }: IndividualViewProps) => {
  const groups = groupFilesByFile(records);

  if (groups.length === 0) {
    return <div className="px-6 py-4 text-sm text-ink-3">No records</div>;
  }

  // No scroll container here — the parent (SessionDetail) owns the single scroll area
  // shared with the assembled view so one observer can drive progressive rendering.
  return (
    <div className="px-6 pb-4 bg-canvas space-y-4">
      {groups.map(g => (
        <section key={g.key} className={cn(g.isSidechain && 'pl-3 border-l-2 border-brand')}>
          <header className="sticky top-0 z-10 bg-canvas py-1.5 flex items-center gap-2 text-[11px]">
            <span className="font-mono text-ink truncate">{fileHeading(g.sourceFile, g.agentId, g.isSidechain)}</span>
            {g.isSidechain && <span className="flex-shrink-0 px-1.5 py-0.5 rounded bg-[var(--accent-soft)] text-brand font-mono text-[10px]">sidechain</span>}
            <span className="flex-shrink-0 text-ink-3">{g.records.length} records</span>
          </header>
          <div className="space-y-2">
            {keyRecords(g.records, g.key).map(({ key, record: r }) => (
              <div key={key} className="rounded-lg border border-border bg-surface">
                <div className="flex items-center gap-2 px-3 py-1.5 border-b border-border text-[11px] text-ink-2">
                  <span className="font-bold text-ink">{r.record_type}</span>
                  <span className="font-mono">{r.ts}</span>
                </div>
                <pre className="px-3 py-2 text-[11px] text-ink whitespace-pre-wrap break-words">
                  {JSON.stringify(r.raw, null, 2)}
                </pre>
              </div>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
};

export { IndividualView };
