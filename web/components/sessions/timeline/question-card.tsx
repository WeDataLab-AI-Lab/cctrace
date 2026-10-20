'use client';

import { CircleHelp, Square, SquareCheck } from 'lucide-react';
import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';
import type { AnalysisQuestion, QuestionItem, QuestionOption, QuestionStatus } from '@/lib/session-analysis-types';
import { Chip } from '@/components/sessions/analysis/atoms';

/**
 * AskUserQuestion 카드(E6). 질문 호출 하나가 회색 상자 하나이고, 안의 질문 항목마다 가는 줄로 나뉜다.
 *
 *   머리   채워진 동그라미 "?" + "질문: 머리글" + "1개 선택"/"여러 개 선택"
 *   본문   질문 문장
 *   선택지 흰 카드. 고른 것은 파란 테두리로 또렷하게(2px), 단일 선택은 점, 여러 개 선택은 체크 상자
 *   끝     "자유 응답" 카드. 직접 쓴 답이 있으면 그것이 고른 것이다
 *
 * 고른 답 중 선택지에 없는 글(`answer.labels`)은 버리지 않고 고른 카드로 덧붙인다. 답하지 않았거나 거절된 질문은
 * 아무것도 고르지 않은 채 상태 칩을 단다. 본문이 가려졌거나 보류된 질문은 선택지 대신 안내를 쓴다.
 */

const REDACTED_NOTE = '비식별화로 질문 내용이 가려졌습니다.';
const WITHHELD_NOTE = '질문 내용을 서버에 올리지 않았습니다(보류됨). 질문이 없었다는 뜻이 아닙니다.';

/** 라벨 비교용. 모델이 라벨 끝에 "(Recommended)"를 글자로 붙여 쓰는 경우를 같은 선택지로 본다. */
const normalizeLabel = (label: string): string => label.replace(/\s*\(Recommended\)\s*$/i, '').trim();

/** 채워진 동그라미 안의 "?". 아이콘의 바깥 원만 채우고 물음표 획은 밝게 남긴다. */
const QuestionBadge = () => (
  <CircleHelp
    aria-hidden="true"
    size={18}
    strokeWidth={2.25}
    className="shrink-0 fill-brand stroke-brand-ink [&>circle]:stroke-brand"
  />
);

/** 단일 선택은 점(고름)과 빈 고리, 여러 개 선택은 체크 상자다. */
const ChoiceGlyph = ({ multi, chosen }: { multi: boolean; chosen: boolean }) => {
  if (multi) {
    const Icon = chosen ? SquareCheck : Square;
    return (
      <Icon
        size={14}
        strokeWidth={chosen ? 2.25 : 1.75}
        aria-hidden="true"
        className={cn('mt-[3px] shrink-0', chosen ? 'text-brand' : 'text-ink-3')}
      />
    );
  }
  return (
    <span aria-hidden="true" className="mt-[5px] grid size-3 shrink-0 place-items-center">
      {chosen ? <span className="size-2 rounded-full bg-brand" /> : <span className="size-3 rounded-full border border-ink-3" />}
    </span>
  );
};

interface OptionCardProps {
  multi: boolean;
  chosen: boolean;
  label: string;
  recommended?: boolean;
  description?: string;
  /** "자유 응답"처럼 고르지 않았을 때 글자를 흐리게 쓰는 카드. */
  muted?: boolean;
  children?: ReactNode;
}

const OptionCard = ({ multi, chosen, label, recommended = false, description, muted = false, children }: OptionCardProps) => (
  <li
    data-chosen={chosen ? 'true' : 'false'}
    // 고른 카드는 테두리 1px에 링 1px을 더해 2px로 보이게 한다(두께가 달라도 카드 크기가 변하지 않는다).
    className={cn('rounded-lg border bg-surface px-2.5 py-1.5', chosen ? 'border-brand ring-1 ring-brand' : 'border-border')}
  >
    <div className="flex items-start gap-2">
      <ChoiceGlyph multi={multi} chosen={chosen} />
      <div className="min-w-0 flex-1">
        <p className={cn('text-[12px] leading-5 font-semibold break-keep [overflow-wrap:anywhere]', muted && !chosen ? 'text-ink-3' : 'text-ink')}>
          {chosen && <span className="sr-only">선택한 답: </span>}
          {label}
          {recommended && ' (Recommended)'}
        </p>
        {description && <p className="mt-0.5 text-[11px] leading-[1.5] break-keep text-ink-3 [overflow-wrap:anywhere]">{description}</p>}
        {children}
      </div>
    </div>
  </li>
);

const STATUS_CHIP: Partial<Record<QuestionStatus, { tone: 'warning' | 'neutral'; text: string; title: string }>> = {
  unanswered: { tone: 'warning', text: '답 없음', title: '이 질문에 아직 답하지 않았습니다.' },
  rejected: { tone: 'neutral', text: '거절됨', title: '사용자가 이 질문에 답하지 않고 거절했습니다.' },
};

const Heading = ({ title, hint, status }: { title: string; hint?: string; status?: QuestionStatus }) => {
  const chip = status ? STATUS_CHIP[status] : undefined;
  return (
    <div className="flex items-center gap-1.5">
      <QuestionBadge />
      <h3 className="min-w-0 truncate text-[12.5px] leading-5 font-semibold text-ink">{title}</h3>
      {hint && <span className="shrink-0 text-[11px] text-ink-3">{hint}</span>}
      {chip && (
        <Chip tone={chip.tone} size="sm" title={chip.title} className="ml-auto">
          {chip.text}
        </Chip>
      )}
    </div>
  );
};

/** 질문 항목 하나. 고른 라벨은 `answered`일 때만 인정한다. */
const QuestionSection = ({ item, status, first }: { item: QuestionItem; status: QuestionStatus; first: boolean }) => {
  const multi = item.multi_select;
  const answer = status === 'answered' ? item.answer : null;
  const chosenLabels = new Set((answer?.labels ?? []).map(normalizeLabel));
  const optionLabels = new Set(item.options.map((o: QuestionOption) => normalizeLabel(o.label)));
  // 선택지에 없는 답은 버리지 않는다(라벨이 바뀌었거나 모델이 다르게 적었을 수 있다).
  const extras = (answer?.labels ?? []).filter((l) => !optionLabels.has(normalizeLabel(l)));
  const freeText = answer && typeof answer.free_text === 'string' && answer.free_text.trim() !== '' ? answer.free_text : null;

  return (
    <div className={cn('px-3 py-2.5', !first && 'border-t border-border')}>
      <Heading title={`질문: ${item.header}`} hint={multi ? '여러 개 선택' : '1개 선택'} status={first ? status : undefined} />
      <p className="mt-1.5 text-[12px] leading-[1.55] break-keep whitespace-pre-wrap text-ink [overflow-wrap:anywhere]">{item.question}</p>
      <ul className="mt-2 space-y-1.5">
        {item.options.map((option, i) => (
          <OptionCard
            key={`${i}-${option.label}`}
            multi={multi}
            chosen={chosenLabels.has(normalizeLabel(option.label))}
            label={option.label}
            recommended={option.recommended}
            description={option.description}
          />
        ))}
        {extras.map((label) => (
          <OptionCard key={`extra-${label}`} multi={multi} chosen label={label} description="선택지에 없는 답입니다." />
        ))}
        <OptionCard multi={multi} chosen={freeText !== null} label="자유 응답" muted>
          {freeText !== null && (
            <p className="mt-1 text-[12px] leading-[1.5] break-keep whitespace-pre-wrap text-ink [overflow-wrap:anywhere]">{freeText}</p>
          )}
        </OptionCard>
      </ul>
    </div>
  );
};

const QuestionCard = ({ question }: { question: AnalysisQuestion }) => {
  const headers = question.items.map((i) => i.header).filter(Boolean);
  const name = headers.length > 0 ? `질문: ${headers.join(' / ')}` : '질문';
  const hidden = question.state === 'redacted' || question.state === 'withheld';
  return (
    <div
      role="group"
      aria-label={name}
      data-question-status={question.status}
      data-question-state={question.state}
      className="w-full overflow-hidden rounded-xl border border-border bg-surface-sunk"
    >
      {hidden ? (
        <div className="px-3 py-2.5">
          <Heading title="질문" status={question.status} />
          <p className="mt-1.5 text-[11.5px] leading-relaxed text-ink-3">{question.state === 'redacted' ? REDACTED_NOTE : WITHHELD_NOTE}</p>
        </div>
      ) : question.items.length === 0 ? (
        <div className="px-3 py-2.5">
          <Heading title="질문" status={question.status} />
          <p className="mt-1.5 text-[11.5px] leading-relaxed text-ink-3">질문 항목이 기록되지 않았습니다.</p>
        </div>
      ) : (
        question.items.map((item, i) => <QuestionSection key={`${i}-${item.header}`} item={item} status={question.status} first={i === 0} />)
      )}
    </div>
  );
};

export { QuestionCard, REDACTED_NOTE, WITHHELD_NOTE };
