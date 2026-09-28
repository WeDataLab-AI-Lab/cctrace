import type { ReactNode } from 'react';

const reportCardClass = 'rounded-[var(--r-lg)] border border-border bg-surface p-[var(--pad-card)] shadow-[var(--sh-sm)]';

interface BlockTagProps {
  children: ReactNode;
}

/** Says who produced a block -- rule or model -- on a page that holds both (#672). */
const BlockTag = ({ children }: BlockTagProps) => (
  <span className="rounded-[var(--r-xs)] bg-surface-sunk px-1.5 py-0.5 text-[11px] font-semibold tracking-[0.05em] text-ink-3">
    {children}
  </span>
);

interface ReportBlockHeaderProps {
  title: string;
  tag: string;
  /** Action belonging to this block, shown after the tag. Blocks without one
   *  keep the plain title + tag row. */
  action?: ReactNode;
}

const ReportBlockHeader = ({ title, tag, action }: ReportBlockHeaderProps) => (
  <header className="flex items-center justify-between gap-3">
    <h3 className="text-[17px] font-semibold tracking-[-0.02em] text-ink">{title}</h3>
    <span className="flex items-center gap-2">
      <BlockTag>{tag}</BlockTag>
      {action}
    </span>
  </header>
);

interface ReportSummaryProps {
  summary: string;
  /** Re-running the analysis belongs beside the report it replaces. It used to
   *  sit at the end of the generation footer, among the grey facts about when
   *  and how the report was made, where a reader looking for it did not find
   *  it -- the report reads top-down and the action was below the fold. */
  action?: ReactNode;
}

const ReportSummary = ({ summary, action }: ReportSummaryProps) => (
  <section className={reportCardClass}>
    <ReportBlockHeader title="요약" tag="AI 작성" action={action} />
    <p className="mt-3 whitespace-pre-line text-[14px] leading-relaxed text-ink">{summary}</p>
  </section>
);

export { BlockTag, ReportBlockHeader, reportCardClass, ReportSummary };
