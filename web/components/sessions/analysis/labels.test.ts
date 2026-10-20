import { describe, expect, it } from 'vitest';
import type { SessionCategoryKey } from '@/lib/session-analysis-types';
import {
  CATEGORY_META,
  CATEGORY_ORDER,
  COST_SOURCE_META,
  INPUT_KIND_LABEL,
  SORT_META,
  SORT_ORDER,
  TURN_COST_SOURCE_META,
  TURN_STATUS_LABEL,
  categoryMeta,
  inputKindLabel,
  waitingKindLabel,
} from './labels';

const ALL_CATEGORIES: SessionCategoryKey[] = [
  'waiting',
  'running',
  'ended',
  'stale_live',
  'open_question',
  'idle',
  'collector_delayed',
  'recent',
  'inactive',
];

describe('관측 범주 라벨 (계획 §5.3)', () => {
  it('아홉 범주가 판정 순서(§8.4 SQL)로 한 번씩 있다', () => {
    expect(CATEGORY_ORDER).toEqual(ALL_CATEGORIES);
    expect(new Set(CATEGORY_ORDER).size).toBe(9);
  });

  it('이름은 계획 표에 적힌 그대로다', () => {
    const labels = Object.fromEntries(ALL_CATEGORIES.map((k) => [k, CATEGORY_META[k].label]));
    expect(labels).toEqual({
      waiting: '응답 대기',
      running: '진행 중',
      ended: '종료됨',
      stale_live: '판단 불가(오래된 생존 행)',
      open_question: '열린 질문',
      idle: '쉬는 중',
      collector_delayed: '수집 지연',
      recent: '최근 활동',
      inactive: '24시간 이상 활동 없음',
    });
  });

  it('모든 범주에 설명과 색조가 있고, 좁은 자리용 줄임말은 라벨보다 길지 않다', () => {
    for (const key of ALL_CATEGORIES) {
      const meta = CATEGORY_META[key];
      expect(meta.hint.length).toBeGreaterThan(10);
      expect(meta.tone).toBeTruthy();
      expect(meta.shortLabel.length).toBeLessThanOrEqual(meta.label.length);
    }
  });

  it('"모른다"를 뜻하는 두 범주는 점선(unknown) 색조다', () => {
    expect(CATEGORY_META.stale_live.tone).toBe('unknown');
    expect(CATEGORY_META.collector_delayed.tone).toBe('unknown');
  });

  it('서버가 모르는 범주 키를 보내도 키를 라벨로 쓴다', () => {
    const meta = categoryMeta('hibernating');
    expect(meta.label).toBe('hibernating');
    expect(meta.tone).toBe('quiet');
  });
});

describe('그 밖의 라벨', () => {
  it('대기 종류: 계획의 "권한 승인"과 일곱 종류', () => {
    expect(waitingKindLabel('permission')).toBe('권한 승인');
    expect(['input', 'permission', 'dialog', 'sandbox', 'worker', 'goal', 'other'].map(waitingKindLabel)).toEqual([
      '입력 필요',
      '권한 승인',
      '대화 상자',
      '샌드박스 요청',
      '워커 요청',
      '목표 제안',
      '기타',
    ]);
    expect(waitingKindLabel('unheard-of')).toBe('unheard-of');
  });

  it('입력 출처(E4): 계획의 일곱 말을 그대로 쓰고 열두 종류가 모두 있다', () => {
    expect(INPUT_KIND_LABEL.typed).toBe('직접 입력');
    expect(INPUT_KIND_LABEL.answer).toBe('질문 답');
    expect(INPUT_KIND_LABEL.queued).toBe('작업 중 입력');
    expect(INPUT_KIND_LABEL.notification).toBe('백그라운드 알림');
    expect(INPUT_KIND_LABEL.peer).toBe('다른 세션');
    expect(INPUT_KIND_LABEL.scheduled).toBe('예약 실행');
    expect(INPUT_KIND_LABEL.command).toBe('명령');
    expect(Object.keys(INPUT_KIND_LABEL)).toHaveLength(12);
    expect(inputKindLabel('from-mars')).toBe('from-mars');
  });

  it('턴 상태(D16): 끝남·중단·오류·응답 없음·진행 중과 판단 불가', () => {
    expect(TURN_STATUS_LABEL).toEqual({
      ended: '끝남',
      interrupted: '중단',
      error: '오류',
      no_response: '응답 없음',
      open: '진행 중',
      undeterminable: '판단 불가',
    });
  });

  it('정렬(B7): 시작·마지막 활동·비용·요청 수 네 가지', () => {
    expect(SORT_ORDER).toEqual(['start', 'activity', 'cost', 'requests']);
    expect(SORT_ORDER.map((k) => SORT_META[k].label)).toEqual(['시작', '활동', '비용', '요청 수']);
  });

  it('비용 출처마다 라벨과 설명이 있다', () => {
    expect(Object.keys(COST_SOURCE_META).sort()).toEqual(['estimate', 'estimate_pending', 'none', 'otel', 'otel_partial']);
    expect(Object.keys(TURN_COST_SOURCE_META).sort()).toEqual([
      'estimate',
      'estimate_pending',
      'expired',
      'no_rate',
      'none',
      'otel',
      'otel_partial',
      'unattributable',
    ]);
    for (const meta of [...Object.values(COST_SOURCE_META), ...Object.values(TURN_COST_SOURCE_META)]) {
      expect(meta.label).toBeTruthy();
      expect(meta.hint).toBeTruthy();
    }
  });
});
