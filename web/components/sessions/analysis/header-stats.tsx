'use client';

import { Info } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { AnalysisHeader, DeepPartial } from '@/lib/session-analysis-types';
import { Chip, Hint, PercentBar, Stat, StackedBar, Unknown, type ChipTone } from './atoms';
import { UNATTACHED_COST_HINT, UNKNOWN_TEXT } from './labels';
import { contextView, costView, formatCount, formatTokenCount, tokenView, type CostTone } from './format';

/**
 * 머리의 지표 네 칸: 총 토큰, 비용, 컨텍스트, 도구.
 * 값을 모르면 0이 아니라 점선 "알 수 없음"으로 그린다. 출처가 있는 값(비용, 컨텍스트)은 출처를 칩으로 붙인다.
 */

type Header = DeepPartial<AnalysisHeader>;

const COST_CHIP_TONE: Record<CostTone, ChipTone> = {
  measured: 'neutral',
  estimated: 'warning',
  partial: 'warning',
  unknown: 'unknown',
};

/** 총 토큰 툴팁 머리말. 합이 어디서 왔는지를 basis별로 적는다(요청 단위 합과 롤업 합은 같은 값이 아닐 수 있다). */
const TOKENS_BASIS_TEXT = {
  request_set: '요청 단위 합(복사본 제외, 서브에이전트·버려진 분기 포함)',
  rollup: '세션 롤업 합계(목록에 보이는 값과 같습니다)',
} as const;

const tokensBasisText = (basis: string | undefined): string =>
  basis === 'request_set' ? TOKENS_BASIS_TEXT.request_set : TOKENS_BASIS_TEXT.rollup;

const TokensStat = ({ header }: { header: Header }) => {
  const view = tokenView(header.tokens);
  const apiRequests = header.counts?.api_requests;
  const input = view.parts[0].value;
  const output = view.parts[1].value;
  return (
    <Stat
      label="총 토큰"
      caption={
        <>
          <div>
            입력 {formatTokenCount(input)} · 출력 {formatTokenCount(output)}
          </div>
          <div>
            캐시 읽기 {formatTokenCount(view.parts[2].value)} · 쓰기 {formatTokenCount(view.parts[3].value)}
          </div>
        </>
      }
    >
      {view.total === null ? (
        <Unknown reason="입력·출력·캐시 토큰 중 모르는 칸이 있어 합계를 내지 않았습니다." />
      ) : (
        <Hint
          label={
            <span className="block space-y-0.5">
              <span className="block font-medium">{tokensBasisText(header.tokens?.basis)}</span>
              {view.parts.map((p) => (
                <span key={p.key} className="flex justify-between gap-4 tabular-nums">
                  <span>{p.label}</span>
                  <span>{p.value === null ? UNKNOWN_TEXT : p.value.toLocaleString('en-US')}</span>
                </span>
              ))}
              {typeof apiRequests === 'number' && (
                <span className="flex justify-between gap-4 border-t border-background/20 pt-0.5 tabular-nums">
                  <span>API 호출</span>
                  <span>{apiRequests.toLocaleString('en-US')}</span>
                </span>
              )}
            </span>
          }
        >
          {formatTokenCount(view.total)}
        </Hint>
      )}
    </Stat>
  );
};

const CostStat = ({ header }: { header: Header }) => {
  const view = costView(header.cost);
  // 턴이 없으면 모든 호출이 "턴에 붙지 않은 호출"이라 몫을 나눠 적으면 "턴 귀속 $0"처럼 읽힌다(§4.2).
  const noBoundaries = header.counts?.turn_boundaries?.state === 'undeterminable';
  return (
    <Stat
      label={
        <>
          비용
          {view.sourceLabel && (
            <Hint label={view.sourceHint} ariaLabel={`비용 출처: ${view.sourceLabel}. ${view.sourceHint}`}>
              <Chip tone={COST_CHIP_TONE[view.tone]} size="sm">
                {view.sourceLabel}
              </Chip>
            </Hint>
          )}
        </>
      }
      caption={
        <>
          {noBoundaries ? (
            <div>턴 경계를 몰라 요청별로 나누지 않았습니다</div>
          ) : view.split ? (
            <>
              <div className="truncate">턴 귀속 {view.split.attached}</div>
              <div className="truncate">
                <Hint label={UNATTACHED_COST_HINT} className="underline decoration-dotted underline-offset-2">
                  붙지 않은 호출
                </Hint>{' '}
                {view.split.unattached}
              </div>
            </>
          ) : (
            <div>턴 귀속 몫을 나누지 못했습니다</div>
          )}
          {view.note && (
            <div className="mt-0.5 flex items-start gap-1 text-ink-3">
              <Info size={11} strokeWidth={1.75} className="mt-px shrink-0" aria-hidden="true" />
              <span>{view.note}</span>
            </div>
          )}
        </>
      }
    >
      {view.known ? (
        <span className={cn(view.tone === 'estimated' && 'text-ink-2')}>{view.amount}</span>
      ) : (
        <Unknown reason={view.sourceHint} />
      )}
    </Stat>
  );
};

const ContextStat = ({ header }: { header: Header }) => {
  const view = contextView(header.context);
  const mix = header.context?.mix;
  return (
    <Stat
      label={
        <>
          컨텍스트
          <Hint label={view.sourceHint} ariaLabel={`컨텍스트 창 출처: ${view.sourceLabel}. ${view.sourceHint}`}>
            <Chip tone={view.kind === 'percent' ? 'neutral' : 'unknown'} size="sm">
              {view.sourceLabel}
            </Chip>
          </Hint>
        </>
      }
      caption={
        <>
          {view.kind === 'percent' && (
            <>
              <PercentBar value={view.percentValue ?? 0} label={view.summary} className="mb-1" />
              <div className="truncate">{view.windowLabel ? `${view.tokensLabel} / ${view.windowLabel}` : view.tokensLabel}</div>
            </>
          )}
          {view.kind === 'tokens_only' && (
            <>
              <div className="truncate">창 크기 모름 · 토큰 수만 보입니다</div>
              {view.largeWindowHint && (
                <Hint label={view.largeWindowHint} className="mt-0.5">
                  <Chip tone="outline" size="sm">
                    큰 창(1M) 흔적
                  </Chip>
                </Hint>
              )}
            </>
          )}
          {view.kind === 'unknown' && (
            <>
              <div className="truncate">마지막 요청의 크기를 알 수 없습니다</div>
              {view.largeWindowHint && (
                <Hint label={view.largeWindowHint} className="mt-0.5">
                  <Chip tone="outline" size="sm">
                    큰 창(1M) 흔적
                  </Chip>
                </Hint>
              )}
            </>
          )}
          {mix && <ContextMix mix={mix} />}
        </>
      }
    >
      {view.kind === 'percent' && view.percentLabel}
      {view.kind === 'tokens_only' && (
        <span>
          {view.tokensLabel}
          <span className="ml-1 text-[11px] font-normal text-ink-3">토큰</span>
        </span>
      )}
      {view.kind === 'unknown' && <Unknown reason={view.sourceHint} />}
    </Stat>
  );
};

/** 컨텍스트 구성(F4): 신규 입력 / 캐시 읽기 / 캐시 쓰기. */
const ContextMix = ({ mix }: { mix: { input?: number; cache_read?: number; cache_create?: number } }) => {
  // 한 칸이라도 모르면 막대를 그리지 않는다. 모르는 칸을 0으로 그리면 구성이 거짓이 된다.
  if (typeof mix.input !== 'number' || typeof mix.cache_read !== 'number' || typeof mix.cache_create !== 'number') {
    return null;
  }
  const segments = [
    { key: 'input', label: '신규 입력', value: mix.input, fill: 'fill-ink' },
    { key: 'cache_read', label: '캐시 읽기', value: mix.cache_read, fill: 'fill-ink-3' },
    { key: 'cache_create', label: '캐시 쓰기', value: mix.cache_create, fill: 'fill-ink-4' },
  ];
  const text = segments.map((s) => `${s.label} ${formatTokenCount(s.value)}`).join(' · ');
  // 줄임 표기(범례): 신규 입력 → 신규, 캐시 읽기 → 읽기, 캐시 쓰기 → 쓰기. 전체 이름은 툴팁에 있다.
  const legend = `신규 ${formatTokenCount(mix.input)} · 읽기 ${formatTokenCount(mix.cache_read)} · 쓰기 ${formatTokenCount(mix.cache_create)}`;
  return (
    <div className="mt-1.5" title={`컨텍스트 구성: ${text}`}>
      <StackedBar segments={segments} label={`컨텍스트 구성: ${text}`} />
      <div className="mt-0.5 text-[10px] leading-snug text-ink-3">{legend}</div>
    </div>
  );
};

const ToolsStat = ({ header }: { header: Header }) => {
  const calls = header.tools?.calls;
  const running = header.tools?.subagents_running;
  return (
    <Stat
      label="도구 호출"
      caption={
        running == null ? (
          <div className="truncate">서브에이전트 가동 수 알 수 없음</div>
        ) : (
          <div className="truncate">가동 중인 서브에이전트 {formatCount(running)}</div>
        )
      }
    >
      {typeof calls === 'number' ? formatCount(calls) : <Unknown />}
    </Stat>
  );
};

export { TokensStat, CostStat, ContextStat, ToolsStat, tokensBasisText };
