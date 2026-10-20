'use client';

import { Image as ImageIcon } from 'lucide-react';
import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';
import type { TimelineMessage } from '@/lib/session-analysis-types';
import { ConversationMarkdown } from '@/components/sessions/conversation-markdown';
import { isInput, messageMeta, type MessageMeta } from './model';
import { PlanCard } from './plan-card';
import { QuestionCard } from './question-card';

/**
 * 타임라인의 메시지 하나(E4~E7).
 *   사용자 입력  머리 줄(시각 · n/N · 출처 · 나)을 오른쪽에 붙이고, 말풍선도 오른쪽이다. 줄바꿈은 그대로 둔다.
 *   답 입력      질문 카드는 Claude의 것이라 왼쪽에 "Claude · 시각"을 먼저 적고, 그 아래 사용자 머리 줄, 그 아래 카드가 온다.
 *   assistant    왼쪽 "Claude · 시각" 아래에 본문. 도구 호출 사이의 중간 출력은 더 조용하게 그린다.
 *   system       양옆에 가는 줄을 둔 작은 글자 한 줄.
 * 바깥 요소가 `data-turn-key`·`data-input-n`을 달아 상세에서 누른 턴과 이동 버튼이 DOM에서 찾는다.
 */

const Sep = () => (
  <span aria-hidden="true" className="text-ink-4">
    {' · '}
  </span>
);

const timeText = (meta: MessageMeta): string => (meta.relative ? `${meta.time} (${meta.relative})` : meta.time);

const UserMetaLine = ({ meta, current, witness }: { meta: MessageMeta; current: boolean; witness?: TimelineMessage['source_witness'] }) => (
  <p
    title={witness === 'analysis_input_uuid' ? '입력 출처 근거: 원문 UUID가 세션 분석 입력 UUID와 일치합니다.'
      : witness === 'unmatched_record' ? '원문 역할은 사용자지만 세션 분석 입력과 일치하지 않아 직접 입력으로 분류하지 않습니다.' : undefined}
    className="max-w-full self-end text-right text-[10.5px] leading-4 tabular-nums text-ink-3"
  >
    <span>{timeText(meta)}</span>
    {meta.position && (
      <>
        <Sep />
        <span className={cn(current && 'font-semibold text-ink-2')}>{meta.position}</span>
      </>
    )}
    {meta.source && (
      <>
        <Sep />
        <span>{meta.source}</span>
      </>
    )}
    {meta.actor && (
      <>
        <Sep />
        <span>{meta.actor}</span>
      </>
    )}
    {witness && (
      <>
        <Sep />
        <span>{witness === 'analysis_input_uuid' ? '입력 근거 확인' : '입력 근거 미확인'}</span>
      </>
    )}
  </p>
);

const AssistantMetaLine = ({ meta }: { meta: MessageMeta }) => (
  <p className="max-w-full self-start text-[10.5px] leading-4 tabular-nums text-ink-3">
    <span>{meta.actor}</span>
    <Sep />
    <span>{timeText(meta)}</span>
  </p>
);

const ImageMarker = ({ count }: { count: number }) => (
  <span
    title={`이미지 ${count}장을 붙였습니다.`}
    className="inline-flex shrink-0 items-center gap-1 self-end rounded-full border border-border bg-surface px-2 py-px text-[10.5px] leading-4 text-ink-2"
  >
    <ImageIcon size={11} strokeWidth={1.75} aria-hidden="true" />
    이미지 {count}장
  </span>
);

/** 사람이 보낸 입력은 강조색, 알림·다른 세션·예약처럼 사람이 아닌 입력은 테두리만 둔다. */
const BUBBLE_CLASS = 'max-w-[88%] self-end rounded-xl px-3 py-2 text-[12px] leading-[1.55] break-keep whitespace-pre-wrap [overflow-wrap:anywhere]';

const UserBody = ({ message, meta }: { message: TimelineMessage; meta: MessageMeta }) => {
  const human = meta.actor === '나';
  const images = message.images ?? 0;
  const hasText = typeof message.text === 'string' && message.text.length > 0;
  return (
    <>
      {hasText ? (
        <div data-bubble className={cn(BUBBLE_CLASS, human ? 'bg-brand-soft text-ink' : 'border border-border bg-surface-2 text-ink-2')}>
          {message.text}
        </div>
      ) : (
        // 본문이 없는 입력: 이미지만 붙였으면 이미지 칩이 전부다. 그 밖에는 가려졌거나 보내지 않은 것이다.
        images === 0 && (
          <div data-bubble className={cn(BUBBLE_CLASS, 'border border-dashed border-border-strong text-ink-3 italic')}>
            본문을 볼 수 없습니다. 비식별화로 가려졌거나 서버에 올리지 않았습니다.
          </div>
        )
      )}
      {images > 0 && <ImageMarker count={images} />}
    </>
  );
};

interface MessageViewProps {
  message: TimelineMessage;
  now: number;
  timeZone?: string;
  /** 이동 버튼이 가리키는 현재 입력. 번호가 굵어진다. */
  current?: boolean;
}

const MessageView = ({ message, now, timeZone, current = false }: MessageViewProps) => {
  const meta = messageMeta(message, now, timeZone);
  const input = isInput(message);
  const { question, plan } = message;

  let content: ReactNode;
  if (message.role === 'system') {
    content = (
      <div className="flex items-center gap-2 py-0.5 text-[10.5px] text-ink-3" title={timeText(meta)}>
        <span aria-hidden="true" className="h-px flex-1 bg-border-subtle" />
        <span
          className="max-w-[80%] text-center break-keep [overflow-wrap:anywhere]"
          title={message.source_witness === 'unmatched_record' ? '역할·입력 출처 근거가 확인되지 않아 직접 입력으로 분류하지 않습니다.' : undefined}
        >
          {message.text ?? '시스템 메시지'}
        </span>
        <span aria-hidden="true" className="h-px flex-1 bg-border-subtle" />
      </div>
    );
  } else if (message.role === 'user') {
    // 질문 카드가 붙은 답 입력: 카드는 Claude가 낸 것이라 질문한 시각으로 "Claude" 줄을 먼저 적는다.
    const asked = question ? messageMeta({ id: message.id, ts: question.ts, role: 'assistant', turn_key: message.turn_key }, now, timeZone) : null;
    content = (
      <>
        {asked && <AssistantMetaLine meta={asked} />}
        <UserMetaLine meta={meta} current={current} witness={message.source_witness} />
        {question ? <QuestionCard question={question} /> : <UserBody message={message} meta={meta} />}
        {plan && <PlanCard plan={plan} />}
      </>
    );
  } else {
    content = (
      <>
        <AssistantMetaLine meta={meta} />
        {message.text && (
          <div
            className={cn(
              'w-full text-[12px] text-ink',
              message.intermediate && 'border-l-2 border-border pl-2 text-[11.5px] text-ink-2',
            )}
          >
            <ConversationMarkdown text={message.text} />
          </div>
        )}
        {question && <QuestionCard question={question} />}
        {plan && <PlanCard plan={plan} />}
      </>
    );
  }

  return (
    <div
      data-message-id={message.id}
      data-turn-key={message.turn_key}
      data-role={message.role}
      data-input-n={input ? message.n : undefined}
      data-intermediate={message.intermediate ? 'true' : undefined}
      data-current={current ? 'true' : undefined}
      aria-current={current ? 'location' : undefined}
      // 상세에서 누른 턴은 잠깐 배경이 바뀐다(`data-focused`는 타임라인 칸이 DOM에 직접 달았다 뗀다).
      className="-mx-1.5 flex flex-col gap-1 rounded-lg px-1.5 py-1.5 transition-colors duration-500 data-[focused=true]:bg-warning-soft"
    >
      {content}
    </div>
  );
};

export { MessageView };
