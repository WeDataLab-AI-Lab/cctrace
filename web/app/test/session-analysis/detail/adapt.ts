/**
 * 패스 A 픽스처(`../fixtures`의 `PREVIEW_SESSIONS`)를 패스 C 모양으로 감싼다. 패스 A가 보이는 상태(ready, stale, building,
 * none, truncated, unsupported, 404, 복사, Codex, 추정 비용, ...)가 새 화면에서도 그대로 보이게 한다.
 *
 * 패스 A는 세션 기록, 커밋, task, 메모, 분류를 모른다. 그래서 감싼 응답은 정직하게 "없다"고 말한다:
 *   세션 기록 없음(`none`), 커밋 수집 안 함, task 갱신 없음, 메모 없음, 분류는 대부분 미분류.
 * 판정은 신호와 기록 없음에서 `deriveJudgement`가 계산한다(작업 내용은 판단 불가 `no_record`). 응답이 완전하지 않은 상태
 * (building 등)와 404는 패스 A 그대로 두고 목록 행에 분류만 얹는다.
 */

import type {
  SessionAnalysisFull,
  SessionCategoryKey,
  SessionClassification,
  SessionSignals,
  TimelineMessage,
} from '@/lib/session-analysis-types';
import { isFullAnalysis } from '@/components/sessions/analysis/guards';
import type { SessionFixture } from '../fixtures';
import { NOT_COLLECTED_COMMITS, NO_RECORD, buildTimeline, totalOf, withTurnFacts, type DetailFixture } from './builders';
import { deriveJudgement, rowJudgementOf } from './judgement-derive';

/** 살아 있는 실행 인스턴스가 있는 관측 범주. 목록의 "실행 중인 세션" 묶음과 판정의 상태가 같은 기준을 쓴다. */
const ALIVE_CATEGORIES: ReadonlySet<SessionCategoryKey> = new Set<SessionCategoryKey>(['waiting', 'running', 'idle']);

export const isAliveCategory = (key: SessionCategoryKey | undefined): boolean => key !== undefined && ALIVE_CATEGORIES.has(key);

/** 마지막 턴을 사용자가 중단했다(신호 `turn`이 `interrupted`). 살아 있을 때만 판정의 상태 "중단 중"이 된다. */
export const isInterruptedSignal = (signals: SessionSignals | undefined): boolean =>
  signals?.turn?.state === 'observed' && signals.turn.value === 'interrupted';

/** 분류는 대부분 미분류이고 몇 개만 다른 값이다(색인으로 정해 늘 같다). */
const CLASSIFICATION_SPREAD: readonly SessionClassification[] = [
  'unsorted',
  'unsorted',
  'working',
  'unsorted',
  'consulting',
  'unsorted',
  'later',
  'unsorted',
  'done',
  'unsorted',
];

/**
 * 아주 긴 세션의 타임라인은 처음 입력 몇 개까지만 담는다. 실제 앱은 대화 API를 쪽 단위로 읽는다.
 * 이 창에서도 `of`는 전체 입력 수이므로 번호는 1부터 이어지지만 마지막 번호가 `of`보다 작다.
 */
export const TIMELINE_WINDOW = 120;

const timelineOf = (analysis: SessionAnalysisFull): TimelineMessage[] => {
  const total = analysis.header.counts.inputs ?? analysis.inputs.length;
  const outside = analysis.inputs[TIMELINE_WINDOW];
  const raw = buildTimeline(analysis);
  const windowed = outside ? raw.filter((m) => Date.parse(m.ts) < Date.parse(outside.ts)) : raw;
  return windowed.map((m) => (m.of === undefined ? m : { ...m, of: total }));
};

export const adaptPassA = (f: SessionFixture, index = 0): DetailFixture => {
  const classification = CLASSIFICATION_SPREAD[index % CLASSIFICATION_SPREAD.length];
  const a = f.analysis;

  if (a === null || !isFullAnalysis(a)) {
    return {
      id: f.id,
      label: f.label,
      covers: f.covers,
      row: f.row.analysis ? { ...f.row, analysis: { ...f.row.analysis, classification } } : f.row,
      analysis: a,
      identities: f.identities,
      timeline: [],
    };
  }

  const alive = isAliveCategory(a.header.category?.key);
  const judgement = deriveJudgement({ alive, interrupted: alive && isInterruptedSignal(a.signals), signals: a.signals, record: NO_RECORD });
  const analysis: SessionAnalysisFull = {
    ...a,
    turns: withTurnFacts(a.turns, NOT_COLLECTED_COMMITS, NO_RECORD),
    abandoned: withTurnFacts(a.abandoned, NOT_COLLECTED_COMMITS, NO_RECORD),
    judgement,
    record: NO_RECORD,
    commits: NOT_COLLECTED_COMMITS,
    tasks: { items: [] },
    note: null,
    classification,
  };
  const rowAnalysis = f.row.analysis;
  return {
    id: f.id,
    label: f.label,
    covers: f.covers,
    row: rowAnalysis
      ? {
          ...f.row,
          analysis: {
            ...rowAnalysis,
            judgement: rowJudgementOf(judgement),
            commits: null,
            note: null,
            classification,
            headline: null,
            tokens_total: totalOf(a.header.tokens),
          },
        }
      : f.row,
    analysis,
    identities: f.identities,
    timeline: timelineOf(analysis),
  };
};
