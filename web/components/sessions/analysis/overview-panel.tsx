'use client';

import { ChevronRight, CircleAlert, CircleDashed, Hourglass, Lock, ScrollText } from 'lucide-react';
import { useState, type ReactNode } from 'react';
import { cn } from '@/lib/utils';
import type { AnalysisRecaps, AnalysisTurn, RecordWithContent, RulesFileVersion, SessionRecord } from '@/lib/session-analysis-types';
import { CollectingLoader } from '@/components/common/collecting-loader';
import { Panel } from './atoms';
import { HashChip } from './chips';
import { formatClockNear, formatCount, formatDateTime, formatRelative, safeHref, splitPath } from './format';
import { RecapList } from './recap-list';
import {
  RELATED_KIND_LABEL,
  REVIEW_HINT,
  hasRecordContent,
  kindTagLabel,
  recordReasonDescription,
  recordNotice,
  reviewPrefix,
  splitInlineCode,
  type RecordNotice,
} from './record';

/**
 * "세션 개요"(D12~D14): 굵은 한 줄, 글머리표(인라인 코드와 커밋 칩), 테두리 상자 "검토 사항"(결정·해석·미확인), 태그 줄.
 * 세션 기록은 서버의 LLM이 쓴다. 본문이 있는 상태는 `ready`·`stale`뿐이고 나머지 다섯 상태는 각자 무슨 일이 있었는지를
 * 적는다. 기록이 없을 때는 Claude Code의 recap(패스 A의 요약 목록)이 개요 자리를 대신한다 — 어디서 온 글인지 밝히고서.
 */

/* ---------------- 본문 ---------------- */

const InlineText = ({ text }: { text: string }) => (
  <>
    {splitInlineCode(text).map((span, i) =>
      span.code ? (
        <code key={i} className="rounded bg-surface-sunk px-1 py-px font-mono text-[11px] text-ink">
          {span.text}
        </code>
      ) : (
        <span key={i}>{span.text}</span>
      ),
    )}
  </>
);

const Bullets = ({ bullets }: { bullets: RecordWithContent['bullets'] }) => (
  <ul className="mt-1 list-disc space-y-0.5 pl-4 marker:text-ink-3">
    {bullets.map((bullet, i) => (
      <li key={i} className="pl-0.5 text-[11.5px] leading-[1.4] break-keep text-ink [overflow-wrap:anywhere]">
        <InlineText text={bullet.text} />
        {bullet.commits.length > 0 && (
          <span className="ml-1 inline-flex flex-wrap items-center gap-1 align-middle">
            {bullet.commits.map((short) => (
              <HashChip key={short} short={short} />
            ))}
          </span>
        )}
      </li>
    ))}
  </ul>
);

const PREFIX_CLASS = {
  decision: 'text-ink-2',
  interpretation: 'text-warning-strong',
  unverified: 'text-danger-strong',
} as const;

/** 테두리 위에 제목이 걸치는 상자. 항목마다 결정·해석·미확인 접두가 붙는다. */
const ReviewEvidence = ({ item, turns, onOpenRequest }: { item: RecordWithContent['review'][number]; turns: readonly AnalysisTurn[]; onOpenRequest: (turn: AnalysisTurn) => void }) => {
  const turn = item.turn_key ? turns.find((candidate) => candidate.key === item.turn_key) : undefined;
  const handleOpen = () => { if (turn) onOpenRequest(turn); };
  if (!turn) {
    return <span className="ml-1.5 text-[10px] text-ink-4" title={item.turn_key ? '이 근거 키가 현재 턴 분석에 없습니다.' : '서버가 근거 턴을 보내지 않았습니다.'}>근거 턴 확인 불가</span>;
  }
  return (
    <button type="button" onClick={handleOpen} aria-label={`검토 근거 턴 ${turn.no}로 이동`} className="ml-1.5 rounded border border-border px-1 py-px font-mono text-[10px] text-ink-3 hover:bg-surface-sunk">
      T{turn.no} 근거
    </button>
  );
};

const ReviewBox = ({ review, turns, onOpenRequest }: { review: RecordWithContent['review']; turns: readonly AnalysisTurn[]; onOpenRequest: (turn: AnalysisTurn) => void }) => (
  <div className="relative mt-2.5 rounded-[var(--r-sm)] border border-border px-2 pt-2 pb-1.5">
    <span className="absolute -top-2 left-2.5 bg-surface px-1 text-[11px] font-semibold text-ink-2">검토 사항</span>
    <ul className="list-disc space-y-0.5 pl-3 marker:text-ink-3">
      {review.map((item, i) => (
        <li key={i} className="pl-0.5 text-[11.5px] leading-[1.4] break-keep text-ink [overflow-wrap:anywhere]">
          <span title={REVIEW_HINT[item.kind]} className={cn('font-medium', PREFIX_CLASS[item.kind] ?? 'text-ink-2')}>
            {reviewPrefix(item.kind)}:
          </span>{' '}
          <InlineText text={item.text} />
          <ReviewEvidence item={item} turns={turns} onOpenRequest={onOpenRequest} />
        </li>
      ))}
    </ul>
  </div>
);

/* ---------------- 태그 줄 ---------------- */

const TagGroup = ({ label, children }: { label: string; children: ReactNode }) => (
  <span className="inline-flex flex-wrap items-center gap-1">
    <span className="text-[11px] text-ink-3">{label}</span>
    {children}
  </span>
);

const Pill = ({ children, title, className }: { children: ReactNode; title?: string; className?: string }) => (
  <span title={title} className={cn('inline-flex items-center rounded-full bg-surface-sunk px-2 py-px text-[11px] leading-4 text-ink-2', className)}>
    {children}
  </span>
);

const TagLine = ({ tags, rules }: { tags: RecordWithContent['tags']; rules: readonly RulesFileVersion[] }) => {
  const groups = tags.kinds.length + tags.components.length + tags.screens.length + tags.related.length + rules.length;
  if (groups === 0) return null;
  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1" aria-label="태그">
      {tags.kinds.length > 0 && (
        <TagGroup label="종별">
          {tags.kinds.map((kind) => (
            <span key={kind} className="inline-flex items-center rounded-[5px] bg-cat-6 px-1.5 py-px text-[11px] leading-4 font-semibold text-surface">
              {kindTagLabel(kind)}
            </span>
          ))}
        </TagGroup>
      )}
      {tags.components.length > 0 && (
        <TagGroup label="컴포넌트">
          {tags.components.map((c) => (
            <Pill key={c}>{c}</Pill>
          ))}
        </TagGroup>
      )}
      {tags.screens.length > 0 && (
        <TagGroup label="화면">
          {tags.screens.map((s) => (
            <Pill key={s}>{s}</Pill>
          ))}
        </TagGroup>
      )}
      {tags.related.length > 0 && (
        <TagGroup label="관련">
          {tags.related.map((r) => {
            const href = safeHref(r.url);
            const title = `${RELATED_KIND_LABEL[r.kind] ?? r.kind}${r.item_key ? ` · item_key ${r.item_key}` : ''}${href ? ' · 새 창에서 열기' : ''}`;
            const key = `${r.kind}-${r.item_key ?? r.id}`;
            return href ? (
              <a key={key} href={href} target="_blank" rel="noopener noreferrer" className="rounded-full hover:bg-border">
                <Pill title={title} className="font-medium">
                  {r.id}
                </Pill>
              </a>
            ) : (
              <Pill key={key} title={title}>
                {r.id}
              </Pill>
            );
          })}
        </TagGroup>
      )}
      {rules.map((f) => (
        <TagGroup key={f.path} label={`${splitPath(f.path).base} 판`}>
          <Pill
            title={`${f.path} · 서버가 처음 본 시각 ${formatDateTime(f.discovered_at)}. 세션이 시작되기 전에 서버가 마지막으로 본 판이며, 세션이 실제로 읽은 판이라고 단정할 수는 없습니다.`}
            className="font-mono"
          >
            v{f.version}
          </Pill>
        </TagGroup>
      ))}
    </div>
  );
};

/* ---------------- 상태별 안내 ---------------- */

const NOTICE_ICON = { neutral: CircleDashed, pending: Hourglass, warning: Lock, danger: CircleAlert } as const;

const NoticeBlock = ({ state, notice }: { state: string; notice: RecordNotice }) => {
  const Icon = NOTICE_ICON[notice.tone];
  return (
    <div data-record-state={state} role="status" className="flex gap-2.5 rounded-[var(--r-sm)] border border-dashed border-border-strong bg-surface-2 px-3 py-2.5">
      <Icon size={16} strokeWidth={1.75} className={cn('mt-0.5 shrink-0', notice.tone === 'danger' ? 'text-danger' : 'text-ink-3')} aria-hidden="true" />
      <div className="min-w-0">
        <p className="text-[12px] font-semibold text-ink">{notice.title}</p>
        {state === 'generating' && <CollectingLoader compact label="서버가 쓰는 중" className="justify-start py-0.5" />}
        <p className="mt-0.5 text-[11.5px] leading-relaxed break-keep text-ink-2">{notice.body}</p>
        {notice.reason && (
          <p className="mt-1 text-[10.5px] text-ink-3">
            사유: {notice.reasonDescription ?? '서버가 사유 설명을 보내지 않았습니다.'} <code className="font-mono">{notice.reason}</code>
          </p>
        )}
      </div>
    </div>
  );
};

/** 개요 머리띠 오른쪽: 언제 만들었고 어디까지 반영했는가. */
const recordAside = (record: SessionRecord | undefined, now: number): string | null => {
  if (!record || !hasRecordContent(record)) return null;
  const made = record.generated_at ? formatRelative(record.generated_at, now) : null;
  const covers = record.covers_until ? `${formatClockNear(record.covers_until, now)}까지 반영` : null;
  return [made ? `기록 ${made}` : null, covers].filter(Boolean).join(' · ') || null;
};

const RecapDisclosure = ({ count, children }: { count: number; children: ReactNode }) => {
  const [open, setOpen] = useState(false);
  return (
    <div className="mt-3 border-t border-border-subtle pt-1.5">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="inline-flex items-center gap-0.5 rounded text-[11px] text-ink-3 hover:text-ink-2"
      >
        <ChevronRight size={12} strokeWidth={2} aria-hidden="true" className={cn('transition-transform motion-reduce:transition-none', open && 'rotate-90')} />
        Claude Code recap {formatCount(count)}건
      </button>
      {open && <div className="mt-1.5">{children}</div>}
    </div>
  );
};

interface OverviewPanelProps {
  id: string;
  record: SessionRecord | undefined;
  recaps: AnalysisRecaps;
  recapsTruncated: boolean;
  bodiesTruncated: boolean;
  agent?: string;
  rulesFiles: readonly RulesFileVersion[];
  now: number;
  turns: readonly AnalysisTurn[];
  onOpenRequest: (turn: AnalysisTurn) => void;
  /** 있으면 stale·failed·none 상태에서 "다시 만들기"를 보인다. 없으면 버튼을 그리지 않는다. */
  onRegenerate?: () => void;
  recordActionPending?: boolean;
  recordActionAccepted?: boolean;
  recordActionAcceptedMessage?: string | null;
  recordActionError?: string | null;
}

const OverviewPanel = ({ id, record, recaps, recapsTruncated, bodiesTruncated, agent, rulesFiles, now, turns, onOpenRequest, onRegenerate, recordActionPending = false, recordActionAccepted = false, recordActionAcceptedMessage, recordActionError }: OverviewPanelProps) => {
  const aside = recordAside(record, now);

  const regenerate = onRegenerate && record && (record.state === 'stale' || record.state === 'failed' || record.state === 'none' || record.state === 'missing') && (
    <button
      type="button"
      onClick={onRegenerate}
      disabled={recordActionPending}
      aria-busy={recordActionPending || undefined}
      className="rounded border border-border bg-surface px-1.5 py-px text-[10.5px] font-medium text-ink-2 hover:bg-surface-sunk disabled:cursor-not-allowed disabled:opacity-50"
    >
      {recordActionPending ? '요청 중' : '다시 만들기'}
    </button>
  );
  const actionNotice = recordActionError
    ? <p role="alert" className="mt-2 text-[11px] text-danger">{recordActionError}</p>
    : recordActionAccepted ? <p role="status" className="mt-2 text-[11px] text-ink-3">{recordActionAcceptedMessage ?? '재생성 요청을 접수했습니다. 생성 완료가 아니라 서버 응답 상태를 기다리는 중입니다.'}</p> : null;

  // 응답에 기록 객체가 없다: 서버가 아직 보내지 않는 것이지 "기록 없음"이 아니다.
  if (!record) {
    return (
      <Panel id={id} title="세션 개요">
        <p className="text-[12px] leading-relaxed text-ink-3">이 응답에는 세션 기록이 담겨 있지 않습니다. 기록을 받지 못한 것이며, 기록이 없다는 뜻이 아닙니다.</p>
        <div className="mt-2 border-t border-border-subtle pt-2">
          <p className="mb-1 flex items-center gap-1 text-[11px] font-medium text-ink-2">
            <ScrollText size={12} strokeWidth={1.75} aria-hidden="true" />
            대신 보이는 값: Claude Code가 자리를 비운 뒤 남긴 recap
          </p>
          <RecapList recaps={recaps} truncated={recapsTruncated} bodiesTruncated={bodiesTruncated} agent={agent} />
        </div>
      </Panel>
    );
  }

  if (hasRecordContent(record)) {
    return (
      <Panel id={id} title="세션 개요" aside={<>{aside}{regenerate}</>}>
        {record.state === 'stale' && (
          <div role="status" className="mb-2 flex items-start gap-1.5 rounded border border-warning/40 bg-warning-soft px-2 py-1 text-[11px] leading-relaxed text-warning-strong">
            <Hourglass size={12} strokeWidth={2} className="mt-0.5 shrink-0" aria-hidden="true" />
            <span>
              이 기록은 {record.covers_until ? `${formatClockNear(record.covers_until, now)}까지의` : '이전'} 대화만 반영합니다. 그 뒤의 활동은 아직 담기지 않았고, 서버가 곧
              다시 씁니다.
              {record.reason && <span className="block text-[10.5px]">사유: {recordReasonDescription(record.reason) ?? '설명을 받지 못했습니다.'} <code className="font-mono">{record.reason}</code></span>}
            </span>
          </div>
        )}
        <p className="text-[13px] leading-snug font-semibold break-keep text-ink">{record.headline}</p>
        {record.bullets.length > 0 && <Bullets bullets={record.bullets} />}
        {record.review.length > 0 && <ReviewBox review={record.review} turns={turns} onOpenRequest={onOpenRequest} />}
        <TagLine tags={record.tags} rules={rulesFiles} />
        {actionNotice}
        {recaps.items.length > 0 && (
          <RecapDisclosure count={recaps.items.length}>
            <RecapList recaps={recaps} truncated={recapsTruncated} bodiesTruncated={bodiesTruncated} agent={agent} />
          </RecapDisclosure>
        )}
      </Panel>
    );
  }

  const notice = recordNotice(record.state, record.reason) ?? recordNotice('none');
  return (
    <Panel id={id} title="세션 개요" aside={regenerate || undefined}>
      {notice && <NoticeBlock state={record.state} notice={notice} />}
      {actionNotice}
      {(notice?.recapFallback ?? true) && (
        <div className="mt-2.5 border-t border-border-subtle pt-2">
          <p className="mb-1 flex items-center gap-1 text-[11px] font-medium text-ink-2">
            <ScrollText size={12} strokeWidth={1.75} aria-hidden="true" />
            대신 보이는 값: Claude Code가 자리를 비운 뒤 남긴 recap
          </p>
          <RecapList recaps={recaps} truncated={recapsTruncated} bodiesTruncated={bodiesTruncated} agent={agent} />
        </div>
      )}
      <TagLine tags={{ kinds: [], components: [], screens: [], related: [] }} rules={rulesFiles} />
    </Panel>
  );
};

export { OverviewPanel };
