'use client';

import { ChevronRight, Info, Lock } from 'lucide-react';
import { useState } from 'react';
import { cn } from '@/lib/utils';
import type { SessionAnalysisFull } from '@/lib/session-analysis-types';
import { Dot, Hint, Panel, type DotKind } from './atoms';
import { JudgementChip } from './chips';
import { JUDGEMENT_CAPTION, checkChipViews } from './judgement';
import { signalLines, type SignalLine, type SignalTone } from './signals';

/**
 * "세션 상태"(D8): 관측 검사 다섯과 근거. 칩마다 근거(어느 신호, 어느 항목)가 툴팁에 있다.
 * 검사는 평가가 아니라 관측이다 — 칩은 "남은 것이 있는가"만 말하고, 화면은 다섯을
 * 합쳐 완료/미완료 결론을 만들지 않는다. 목록의 필터도 같은 이유로 결론을 쓰지 않는다.
 *
 *   통과        초록 확인     실패   빨강 엑스와 남은 것("미완 작업 1건")     판단 불가   회색 점선 대시(0이 아니다)
 *
 * 관측 신호 다섯 문장(패스 A의 신호 섹션)은 칩의 근거이자 칩에 없는 `할 일` 신호를 담고 있어 접어서 둔다.
 * 비소유 관리자에게는 판정도 신호도 오지 않는다(집계 경로와 같은 화면 정책).
 */

const TONE_DOT: Record<SignalTone, DotKind> = {
  quiet: 'ring',
  attention: 'solid-warning',
  live: 'solid-brand',
  danger: 'solid-danger',
  unknown: 'dashed',
};

const TONE_TEXT: Record<SignalTone, string> = {
  quiet: 'text-ink-2',
  attention: 'text-ink font-medium',
  live: 'text-ink font-medium',
  danger: 'text-danger-strong font-medium',
  unknown: 'text-ink-3 italic',
};

const SignalRow = ({ line }: { line: SignalLine }) => (
  <li className="flex items-baseline gap-2 py-0.5">
    <span className="flex w-3 shrink-0 translate-y-px justify-center">
      <Dot kind={TONE_DOT[line.tone]} />
    </span>
    <span className="w-[64px] shrink-0 text-[10.5px] text-ink-3">{line.label}</span>
    <span className={cn('min-w-0 text-[12px] leading-5 break-keep', TONE_TEXT[line.tone])}>
      {line.undeterminable && line.reason ? (
        <Hint
          label={line.reason}
          ariaLabel={`${line.sentence}. ${line.reason}`}
          className="italic underline decoration-dotted decoration-ink-4 underline-offset-[3px]"
        >
          {line.sentence}
        </Hint>
      ) : (
        line.sentence
      )}
      {line.note && (
        <Hint label={line.note} ariaLabel={`${line.sentence}에 대한 설명`} className="ml-1.5 inline-flex translate-y-0.5 text-ink-3 hover:text-ink-2">
          <Info size={12} strokeWidth={1.75} aria-hidden="true" />
        </Hint>
      )}
    </span>
  </li>
);

const SignalsDisclosure = ({ analysis, now, forceOpen }: { analysis: SessionAnalysisFull; now: number; forceOpen: boolean }) => {
  const [open, setOpen] = useState(false);
  const lines = signalLines(analysis.signals, { now });
  if (lines.length === 0) return null;
  const expanded = forceOpen || open;
  return (
    <div className="mt-1">
      {!forceOpen && (
        <button
          type="button"
          aria-expanded={expanded}
          onClick={() => setOpen((v) => !v)}
          className="inline-flex items-center gap-0.5 rounded text-[11px] text-ink-3 hover:text-ink-2"
        >
          <ChevronRight
            size={12}
            strokeWidth={2}
            aria-hidden="true"
            className={cn('transition-transform motion-reduce:transition-none', expanded && 'rotate-90')}
          />
          관측 신호 {lines.length}
        </button>
      )}
      {expanded && (
        <ul aria-label="관측 신호" className="mt-1 @xl:columns-2 @xl:gap-x-8 [&>li]:break-inside-avoid">
          {lines.map((line) => (
            <SignalRow key={line.key} line={line} />
          ))}
        </ul>
      )}
    </div>
  );
};

interface StatusPanelProps {
  id: string;
  analysis: SessionAnalysisFull;
  now: number;
}

const StatusPanel = ({ id, analysis, now }: StatusPanelProps) => {
  const ownerView = analysis.owner_view;

  if (!ownerView) {
    return (
      <Panel id={id} title="세션 상태">
        <p className="flex items-start gap-2 text-[12px] leading-relaxed text-ink-3">
          <Lock size={13} strokeWidth={1.75} className="mt-0.5 shrink-0" aria-hidden="true" />
          <span>
            이 세션의 주인이 아니라서 작업 완료 판정과 신호, 관측 범주, 실시간 상태를 표시하지 않습니다. 관리자는 대화 원문과 턴별 상태를 읽을 수 있지만,
            남의 세션 판정을 모아 보는 화면은 만들지 않습니다.
          </span>
        </p>
      </Panel>
    );
  }

  const views = checkChipViews(analysis, now);
  if (views.length === 0) {
    // 계약상 주인에게는 늘 오지만, 오지 않았다면 "주인이 아니라서"가 아니라 받지 못했다고 적는다.
    return (
      <Panel id={id} title="세션 상태">
        <p className="text-[12px] leading-relaxed text-ink-3">
          이 응답에는 작업 완료 판정이 담겨 있지 않습니다. 판정을 받지 못한 것이며, 남은 것이 없다는 뜻이 아닙니다.
        </p>
        {analysis.signals ? (
          <SignalsDisclosure analysis={analysis} now={now} forceOpen />
        ) : (
          <p className="mt-1 text-[12px] leading-relaxed text-ink-3">신호도 담겨 있지 않습니다.</p>
        )}
      </Panel>
    );
  }

  return (
    <Panel id={id} title="세션 상태">
      <div className="flex items-center gap-1.5 text-[11px] text-ink-3">
        관측 검사
        <Hint label={JUDGEMENT_CAPTION} ariaLabel={`작업 완료 판정에 대한 설명. ${JUDGEMENT_CAPTION}`} className="inline-flex text-ink-4 hover:text-ink-2">
          <Info size={12} strokeWidth={1.75} aria-hidden="true" />
        </Hint>
      </div>
      <ul aria-label="관측 검사" className="mt-1.5 flex flex-wrap gap-1.5">
        {views.map((view) => (
          <li key={view.key}>
            <JudgementChip view={view} />
          </li>
        ))}
      </ul>
      <SignalsDisclosure analysis={analysis} now={now} forceOpen={false} />
    </Panel>
  );
};

export { StatusPanel };
