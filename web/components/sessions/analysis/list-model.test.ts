import { describe, expect, it } from 'vitest';
import type { JudgementBucket, JudgementStatus, SessionCategoryKey, SessionClassification } from '@/lib/session-analysis-types';
import type { SessionOverview } from '@/lib/types';
import {
  CLASSIFICATION_LABEL,
  CLASSIFICATION_ORDER,
  ENDED_TOGETHER_GAP_MS,
  classificationCounts,
  classificationOf,
  groupRows,
  isAlive,
  matchesBucket,
  runningCounts,
} from './list-model';

interface RowInit {
  id: string;
  category?: SessionCategoryKey;
  status?: JudgementStatus;
  bucket?: JudgementBucket | null;
  classification?: SessionClassification;
  end?: string;
}

const row = ({ id, category, status, bucket, classification, end = '2026-10-10T05:00:00Z' }: RowInit): SessionOverview => ({
  session_id: id,
  profile_email: 'a@example.com',
  model: 'm',
  agent: 'claude',
  start_time: '2026-10-10T03:00:00Z',
  end_time: end,
  input_tokens: 1,
  output_tokens: 1,
  cost_usd: 1,
  event_count: 1,
  has_sync: true,
  analysis: {
    state: 'ready',
    ...(category ? { category } : {}),
    ...(classification ? { classification } : {}),
    ...(status ? { judgement: { status, bucket: bucket ?? null, checks: ['pass', 'pass', 'pass', 'pass', 'pass'] } } : {}),
  },
});

describe('사용자 분류 탭 (F5)', () => {
  it('다섯 탭의 이름과 순서', () => {
    expect(CLASSIFICATION_ORDER.map((c) => CLASSIFICATION_LABEL[c])).toEqual(['미분류', '상담 중', '작업 중', '나중에', '완료']);
  });

  it('개수는 행의 분류에서 센다. 분류가 오지 않은 행은 어느 탭에도 세지 않는다(미분류와 다르다)', () => {
    const rows = [
      row({ id: 'a', classification: 'unsorted' }),
      row({ id: 'b', classification: 'working' }),
      row({ id: 'c', classification: 'working' }),
      row({ id: 'd' }),
    ];
    expect(classificationCounts(rows)).toEqual({ unsorted: 1, consulting: 0, working: 2, later: 0, done: 0 });
    expect(classificationOf(rows[3])).toBeNull();
    expect(classificationOf(rows[1])).toBe('working');
  });
});

describe('상태별 개수 필터', () => {
  it('null이면 모두, 아니면 그 버킷만', () => {
    const r = row({ id: 'a', status: 'done', bucket: 'leftover' });
    expect(matchesBucket(r, null)).toBe(true);
    expect(matchesBucket(r, 'leftover')).toBe(true);
    expect(matchesBucket(r, 'done')).toBe(false);
    expect(matchesBucket(row({ id: 'b' }), 'done')).toBe(false);
  });
});

describe('실행 중인 세션 (F2)', () => {
  it('살아 있는 것은 waiting·running·idle 범주다. 오래된 생존 행은 믿지 않는다', () => {
    expect(isAlive(row({ id: 'a', category: 'waiting' }))).toBe(true);
    expect(isAlive(row({ id: 'b', category: 'running' }))).toBe(true);
    expect(isAlive(row({ id: 'c', category: 'idle' }))).toBe(true);
    expect(isAlive(row({ id: 'd', category: 'stale_live' }))).toBe(false);
    expect(isAlive(row({ id: 'e', category: 'ended' }))).toBe(false);
    expect(isAlive(row({ id: 'f' }))).toBe(false);
  });

  it('살아 있는 세션을 한 곳에만 센다: 중단 중 → 응답 대기 → 처리 중 → 완료 · 작업 도중 · 모름. 합이 total이다', () => {
    const counts = runningCounts([
      row({ id: 'a', category: 'running', status: 'running', bucket: 'ended_midway' }),
      row({ id: 'b', category: 'idle', status: 'running', bucket: 'done' }),
      row({ id: 'c', category: 'idle', status: 'interrupted', bucket: 'ended_midway' }),
      row({ id: 'd', category: 'waiting', status: 'running', bucket: 'ended_midway' }),
      row({ id: 'h', category: 'idle', status: 'running', bucket: 'leftover' }),
      row({ id: 'e', category: 'idle', status: 'running', bucket: null }),
      row({ id: 'f', category: 'idle' }),
      row({ id: 'g', category: 'ended', status: 'done', bucket: 'done' }),
    ]);
    expect(counts).toEqual({ total: 7, processing: 1, waiting: 1, done: 1, interrupted: 1, leftover: 1, unknown: 2 });
    expect(counts.processing + counts.waiting + counts.done + counts.interrupted + counts.leftover + counts.unknown).toBe(counts.total);
  });

  it('살아 있는 세션이 없으면 모두 0이다', () => {
    expect(runningCounts([row({ id: 'a', category: 'recent' })])).toEqual({ total: 0, processing: 0, waiting: 0, done: 0, interrupted: 0, leftover: 0, unknown: 0 });
  });
});

describe('묶음 나누기', () => {
  const ended = (id: string, end: string) => row({ id, category: 'ended', end });

  it('살아 있는 세션은 실행 중 묶음, 나머지는 일반 목록이다', () => {
    const g = groupRows([row({ id: 'a', category: 'idle' }), row({ id: 'b', category: 'recent' }), row({ id: 'c' })]);
    expect(g.running.map((r) => r.session_id)).toEqual(['a']);
    expect(g.rest.map((r) => r.session_id)).toEqual(['b', 'c']);
    expect(g.ended).toEqual([]);
  });

  it('같은 분 안에 이어서 끝난 둘 이상은 "함께 끝난" 묶음이 되고 늦게 끝난 순이다', () => {
    const g = groupRows([ended('a', '2026-10-10T05:00:10Z'), ended('b', '2026-10-10T05:00:40Z'), ended('c', '2026-10-10T05:00:25Z')]);
    expect(g.ended).toHaveLength(1);
    expect(g.ended[0].rows.map((r) => r.session_id)).toEqual(['b', 'c', 'a']);
    expect(g.ended[0].label).toMatch(/에 함께 끝난 3건$/);
    expect(g.ended[0].endedAt).toBe('2026-10-10T05:00:40Z');
    expect(g.rest).toEqual([]);
  });

  it('간격이 크면 따로이고, 혼자 끝난 세션은 묶음을 만들지 않아 일반 목록에 남는다', () => {
    const far = ENDED_TOGETHER_GAP_MS / 1000 + 5;
    const g = groupRows([ended('a', '2026-10-10T05:00:00Z'), ended('b', `2026-10-10T05:0${Math.floor(far / 60)}:${String(far % 60).padStart(2, '0')}Z`)]);
    expect(g.ended).toEqual([]);
    expect(g.rest.map((r) => r.session_id).sort()).toEqual(['a', 'b']);
  });

  it('가장 늦게 끝난 세션을 기준으로 잰다: 하나씩 이어 끝나는 사슬은 한 묶음으로 불어나지 않는다', () => {
    const g = groupRows([ended('a', '2026-10-10T05:00:00Z'), ended('b', '2026-10-10T05:00:50Z'), ended('c', '2026-10-10T05:01:40Z')]);
    expect(g.ended).toHaveLength(1);
    expect(g.ended[0].rows.map((r) => r.session_id)).toEqual(['c', 'b']);
    expect(g.rest.map((r) => r.session_id)).toEqual(['a']);
  });

  it('끝난 시각을 읽을 수 없는 행은 묶지 않고 일반 목록에 둔다', () => {
    const g = groupRows([ended('a', 'not-a-date'), ended('b', 'also-bad')]);
    expect(g.ended).toEqual([]);
    expect(g.rest.map((r) => r.session_id)).toEqual(['a', 'b']);
  });

  it('"떼기"한 세션은 묶음에서 빠져 일반 목록으로 간다. 하나만 남으면 묶음이 사라진다', () => {
    const rows = [ended('a', '2026-10-10T05:00:00Z'), ended('b', '2026-10-10T05:00:05Z'), ended('c', '2026-10-10T05:00:10Z')];
    const one = groupRows(rows, new Set(['a']));
    expect(one.ended[0].rows.map((r) => r.session_id)).toEqual(['c', 'b']);
    expect(one.rest.map((r) => r.session_id)).toEqual(['a']);
    const two = groupRows(rows, new Set(['a', 'b']));
    expect(two.ended).toEqual([]);
    expect(two.rest.map((r) => r.session_id).sort()).toEqual(['a', 'b', 'c']);
  });

  it('입력 순서를 일반 목록 안에서 지킨다', () => {
    const g = groupRows([row({ id: 'z', category: 'recent' }), ended('s', '2026-10-10T05:00:00Z'), row({ id: 'a', category: 'inactive' })]);
    expect(g.rest.map((r) => r.session_id)).toEqual(['z', 's', 'a']);
  });
});
