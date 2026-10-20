import { describe, expect, it } from 'vitest';
import type { JudgementChecks, SessionRecord, SessionSignals, UncommittedSignal } from '@/lib/session-analysis-types';
import { NO_RECORD, S, recordOf, workOf } from './builders';
import { deriveBucket, deriveChecks, deriveJudgement, deriveStatus, rowJudgementOf } from './judgement-derive';

const KEYS = ['turn_ended', 'no_pending_reply', 'no_background', 'committed', 'work_done'];

const PASS_SIGNALS: SessionSignals = {
  turn: S.turn('ended'),
  question: S.question(0),
  background: S.background(0),
  uncommitted: S.uncommitted(0),
  todo: S.todo(0),
};

const record = (...done: Array<boolean | null>): SessionRecord =>
  recordOf(
    {
      headline: '시험용 기록',
      bullets: [],
      review: [],
      tags: { kinds: [], components: [], screens: [], related: [] },
      work: done.map((d, i) => workOf(`w${i + 1}`, 'followup', d, `항목 ${i + 1}`)),
    },
    { generatedAgo: 5, coversAgo: 6 },
  );

const checksOf = (signals: SessionSignals | undefined, rec: SessionRecord | undefined): JudgementChecks => deriveChecks({ signals, record: rec });

describe('판정 다섯은 정해진 순서로 정확히 다섯 개다', () => {
  it('키 순서', () => {
    expect(checksOf(PASS_SIGNALS, record(true)).map((c) => c.key)).toEqual(KEYS);
    expect(checksOf(undefined, undefined).map((c) => c.key)).toEqual(KEYS);
  });
});

describe('turn_ended', () => {
  it('ended는 통과, 다른 관측값은 건수 없는 실패', () => {
    expect(checksOf({ turn: S.turn('ended') }, undefined)[0]).toEqual({ key: 'turn_ended', state: 'pass' });
    for (const value of ['open', 'interrupted', 'error', 'no_response'] as const) {
      expect(checksOf({ turn: S.turn(value) }, undefined)[0]).toEqual({ key: 'turn_ended', state: 'fail' });
    }
  });

  it('판단 불가는 신호의 이유를 그대로 가진다', () => {
    expect(checksOf({ turn: S.turnUnknown('pre_collect') }, undefined)[0]).toEqual({ key: 'turn_ended', state: 'undeterminable', reason: 'pre_collect' });
    expect(checksOf({ turn: S.turnUnknown('redacted') }, undefined)[0]).toMatchObject({ reason: 'redacted' });
  });

  it('신호 키가 없으면 해당 없음이고 통과가 아니다', () => {
    expect(checksOf({}, undefined)[0]).toEqual({ key: 'turn_ended', state: 'undeterminable', reason: 'not_applicable' });
  });
});

describe('no_pending_reply · no_background', () => {
  it('0은 통과, 양수는 건수와 함께 실패', () => {
    expect(checksOf({ question: S.question(0) }, undefined)[1]).toEqual({ key: 'no_pending_reply', state: 'pass' });
    expect(checksOf({ question: S.question(2) }, undefined)[1]).toEqual({ key: 'no_pending_reply', state: 'fail', count: 2 });
    expect(checksOf({ background: S.background(0) }, undefined)[2]).toEqual({ key: 'no_background', state: 'pass' });
    expect(checksOf({ background: S.background(3) }, undefined)[2]).toEqual({ key: 'no_background', state: 'fail', count: 3 });
  });

  it('판단 불가는 이유를 가지고, 키가 없으면 해당 없음', () => {
    expect(checksOf({ question: S.questionUnknown('redacted') }, undefined)[1]).toMatchObject({ state: 'undeterminable', reason: 'redacted' });
    expect(checksOf({ background: S.backgroundUnknown('pre_collect') }, undefined)[2]).toMatchObject({ state: 'undeterminable', reason: 'pre_collect' });
    expect(checksOf({}, undefined)[1]).toMatchObject({ state: 'undeterminable', reason: 'not_applicable' });
    expect(checksOf({}, undefined)[2]).toMatchObject({ state: 'undeterminable', reason: 'not_applicable' });
  });
});

describe('committed', () => {
  it('0은 통과, 양수는 건수와 함께 실패', () => {
    expect(checksOf({ uncommitted: S.uncommitted(0) }, undefined)[3]).toEqual({ key: 'committed', state: 'pass' });
    expect(checksOf({ uncommitted: S.uncommitted(3, 30) }, undefined)[3]).toEqual({ key: 'committed', state: 'fail', count: 3 });
  });

  it('세션 중간부터 모은 0(at_least)은 통과가 아니라 판단 불가다', () => {
    const partialZero: UncommittedSignal = { state: 'observed', value: 0, at_least: true, changed_at: null, reevaluating: true };
    expect(checksOf({ uncommitted: partialZero }, undefined)[3]).toMatchObject({ state: 'undeterminable' });
    // 같은 부분 수집이라도 양수는 "N개 이상"의 실패다.
    expect(checksOf({ uncommitted: { ...partialZero, value: 2 } }, undefined)[3]).toEqual({ key: 'committed', state: 'fail', count: 2 });
  });

  it('판단 불가는 이유를 가지고, 키가 없으면 해당 없음', () => {
    expect(checksOf({ uncommitted: S.uncommittedUnknown('no_git') }, undefined)[3]).toMatchObject({ state: 'undeterminable', reason: 'no_git' });
    expect(checksOf({}, undefined)[3]).toMatchObject({ state: 'undeterminable', reason: 'not_applicable' });
  });
});

describe('work_done (세션 기록)', () => {
  it('기록이 없거나 본문이 없는 상태는 모두 no_record다', () => {
    expect(checksOf(PASS_SIGNALS, undefined)[4]).toEqual({ key: 'work_done', state: 'undeterminable', reason: 'no_record' });
    expect(checksOf(PASS_SIGNALS, NO_RECORD)[4]).toMatchObject({ reason: 'no_record' });
    for (const state of ['generating', 'withheld', 'redacted', 'failed'] as const) {
      expect(checksOf(PASS_SIGNALS, { state, generated_at: null, covers_until: null })[4]).toMatchObject({ state: 'undeterminable', reason: 'no_record' });
    }
  });

  it('모든 최상위 항목이 완료면 통과', () => {
    expect(checksOf(PASS_SIGNALS, record(true, true))[4]).toEqual({ key: 'work_done', state: 'pass' });
  });

  it('완료가 아닌 항목이 있으면 그 수와 함께 실패(확인 못 한 항목은 세지 않는다)', () => {
    expect(checksOf(PASS_SIGNALS, record(true, false))[4]).toEqual({ key: 'work_done', state: 'fail', count: 1 });
    expect(checksOf(PASS_SIGNALS, record(false, false, null))[4]).toEqual({ key: 'work_done', state: 'fail', count: 2 });
  });

  it('완료 여부를 확인 못 한 항목만 남으면 no_list로 판단 불가', () => {
    expect(checksOf(PASS_SIGNALS, record(true, null))[4]).toEqual({ key: 'work_done', state: 'undeterminable', reason: 'no_list' });
  });

  it('작업 항목이 하나도 없는 기록은 통과가 아니라 no_list로 판단 불가', () => {
    const empty = recordOf({ headline: '항목이 없는 기록', bullets: [], review: [], tags: { kinds: [], components: [], screens: [], related: [] }, work: [] }, { generatedAgo: 5, coversAgo: 5 });
    expect(checksOf(PASS_SIGNALS, empty)[4]).toEqual({ key: 'work_done', state: 'undeterminable', reason: 'no_list' });
  });

  it('stale 기록도 본문이 있으면 같은 규칙이다', () => {
    const stale = recordOf(
      { headline: '낡은 기록', bullets: [], review: [], tags: { kinds: [], components: [], screens: [], related: [] }, work: [workOf('w1', 'request', false, '하나')] },
      { state: 'stale', generatedAgo: 90, coversAgo: 100 },
    );
    expect(checksOf(PASS_SIGNALS, stale)[4]).toEqual({ key: 'work_done', state: 'fail', count: 1 });
  });

  it('하위 항목의 미완은 세지 않는다(최상위 항목 기준)', () => {
    const withChildren = recordOf(
      {
        headline: '하위 항목이 있는 기록',
        bullets: [],
        review: [],
        tags: { kinds: [], components: [], screens: [], related: [] },
        work: [workOf('w1', 'request', true, '요청', { children: [{ kind: 'check', done: false, text: '확인' }] })],
      },
      { generatedAgo: 5, coversAgo: 6 },
    );
    expect(checksOf(PASS_SIGNALS, withChildren)[4]).toEqual({ key: 'work_done', state: 'pass' });
  });
});

describe('상태와 버킷', () => {
  const pass = checksOf(PASS_SIGNALS, record(true));
  const turnOpen = checksOf({ ...PASS_SIGNALS, turn: S.turn('open') }, record(true));
  const turnUnknown = checksOf({ ...PASS_SIGNALS, turn: S.turnUnknown('pre_collect') }, record(true));
  const uncommitted = checksOf({ ...PASS_SIGNALS, uncommitted: S.uncommitted(2) }, record(true));
  const onlyUnknown = checksOf({ ...PASS_SIGNALS, background: S.backgroundUnknown('pre_collect') }, NO_RECORD);

  const base = { signals: PASS_SIGNALS, record: record(true) };

  it('살아 있으면 중단했는지에 따라 중단 중 또는 실행 중이다(검사 결과와 무관)', () => {
    expect(deriveStatus({ ...base, alive: true, interrupted: true }, uncommitted)).toBe('interrupted');
    expect(deriveStatus({ ...base, alive: true, interrupted: false }, turnOpen)).toBe('running');
    expect(deriveStatus({ ...base, alive: true, interrupted: false }, pass)).toBe('running');
  });

  it('살아 있지 않으면: 턴이 안 끝났으면 도중 종료, 남은 것이 있으면 작업 도중, 아니면 완료', () => {
    expect(deriveStatus({ ...base, alive: false, interrupted: false }, turnOpen)).toBe('ended_midway');
    expect(deriveStatus({ ...base, alive: false, interrupted: false }, uncommitted)).toBe('leftover');
    expect(deriveStatus({ ...base, alive: false, interrupted: false }, pass)).toBe('done');
  });

  it('다른 실패가 없어도 판단 불가가 하나라도 있으면 완료로 세지 않는다', () => {
    expect(deriveStatus({ ...base, alive: false, interrupted: false }, onlyUnknown)).toBe('unknown');
    expect(deriveBucket(onlyUnknown)).toBeNull();
    const combinations: (typeof pass)[] = [
      [{ ...pass[0], state: 'undeterminable' }, pass[1], pass[2], pass[3], pass[4]],
      [pass[0], { ...pass[1], state: 'undeterminable' }, pass[2], pass[3], pass[4]],
      [pass[0], pass[1], { ...pass[2], state: 'undeterminable' }, pass[3], pass[4]],
      [pass[0], pass[1], pass[2], { ...pass[3], state: 'undeterminable' }, pass[4]],
      [pass[0], pass[1], pass[2], pass[3], { ...pass[4], state: 'undeterminable' }],
    ];
    for (const checks of combinations) {
      expect(deriveBucket(checks)).toBeNull();
      expect(deriveStatus({ ...base, alive: false, interrupted: false }, checks)).toBe('unknown');
    }
  });

  it('턴이 끝났는지 모르면 상태는 판정 불가이고, 버킷은 남은 것이 있을 때만 정해진다', () => {
    expect(deriveStatus({ ...base, alive: false, interrupted: false }, turnUnknown)).toBe('unknown');
    expect(deriveBucket(turnUnknown)).toBeNull();
    const unknownWithLeft = checksOf({ ...PASS_SIGNALS, turn: S.turnUnknown('pre_collect'), uncommitted: S.uncommitted(1) }, record(true));
    expect(deriveBucket(unknownWithLeft)).toBe('leftover');
  });

  it('버킷은 살아 있는지와 무관하다: 턴 실패 > 그 밖의 실패 > 통과', () => {
    expect(deriveBucket(turnOpen)).toBe('ended_midway');
    expect(deriveBucket(uncommitted)).toBe('leftover');
    expect(deriveBucket(pass)).toBe('done');
  });

  it('deriveJudgement는 셋을 한 번에 낸다', () => {
    const j = deriveJudgement({ alive: true, interrupted: false, signals: { ...PASS_SIGNALS, turn: S.turn('open') }, record: record(true) });
    expect(j.status).toBe('running');
    expect(j.bucket).toBe('ended_midway');
    expect(j.checks.map((c) => c.key)).toEqual(KEYS);
  });

  it('목록 행에는 상태와 다섯 판정의 상태만 싣는다', () => {
    const j = deriveJudgement({ alive: false, interrupted: false, signals: { ...PASS_SIGNALS, uncommitted: S.uncommitted(3) }, record: NO_RECORD });
    expect(rowJudgementOf(j)).toEqual({
      status: 'leftover',
      bucket: 'leftover',
      checks: ['pass', 'pass', 'pass', 'fail', 'undeterminable'],
    });
  });
});
