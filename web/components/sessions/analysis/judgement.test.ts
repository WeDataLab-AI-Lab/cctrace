import { describe, expect, it } from 'vitest';
import type { JudgementCheck, RowJudgement, SessionAnalysisFull, SessionJudgement } from '@/lib/session-analysis-types';
import type { SessionOverview } from '@/lib/types';
import { DETAIL_NOW } from '@/app/test/session-analysis/detail/builders';
import { flagship } from '@/app/test/session-analysis/detail/flagship';
import {
  BUCKET_LABEL,
  BUCKET_ORDER,
  CHECK_PASS_LABEL,
  JUDGEMENT_CHECK_ORDER,
  bucketCounts,
  checkChipView,
  checkChipViews,
  checkEvidence,
  rowCheckGlyphs,
  statusView,
} from './judgement';

const fullFlagship = (): SessionAnalysisFull => {
  const a = flagship().analysis;
  if (!a || (a.state !== 'ready' && a.state !== 'stale')) throw new Error('flagship must be a full analysis');
  return a;
};

const check = (over: Partial<JudgementCheck> & Pick<JudgementCheck, 'key' | 'state'>): JudgementCheck => over;

describe('판정 칩: 글자와 모양', () => {
  it('통과: 다섯 칩의 글자는 레퍼런스의 용어이고, 초록 확인이다', () => {
    expect(JUDGEMENT_CHECK_ORDER.map((key) => CHECK_PASS_LABEL[key])).toEqual(['턴 종료', '답 대기 없음', '백그라운드 작업 없음', '커밋됨', '작업 내용 완료']);
    for (const key of JUDGEMENT_CHECK_ORDER) {
      const view = checkChipView(check({ key, state: 'pass' }));
      expect(view.glyph).toBe('check');
      expect(view.tone).toBe('pass');
      expect(view.label).toBe(CHECK_PASS_LABEL[key]);
    }
  });

  it('실패: 빨간 엑스와 남은 것(미답 질문, 백그라운드 작업, 미커밋 파일, 미완 작업)', () => {
    const cases: Array<[JudgementCheck, string]> = [
      [check({ key: 'no_pending_reply', state: 'fail', count: 2 }), '미답 질문 2건'],
      [check({ key: 'no_background', state: 'fail', count: 1 }), '백그라운드 작업 1건'],
      [check({ key: 'committed', state: 'fail', count: 3 }), '미커밋 파일 3'],
      [check({ key: 'work_done', state: 'fail', count: 1 }), '미완 작업 1건'],
      [check({ key: 'turn_ended', state: 'fail' }), '턴 종료 안 됨'],
    ];
    for (const [c, label] of cases) {
      const view = checkChipView(c);
      expect(view.label).toBe(label);
      expect(view.glyph).toBe('cross');
      expect(view.tone).toBe('fail');
    }
  });

  it('실패인데 건수가 없으면 숫자를 지어내지 않는다', () => {
    expect(checkChipView(check({ key: 'committed', state: 'fail' })).label).toBe('미커밋 파일 있음');
    expect(checkChipView(check({ key: 'work_done', state: 'fail' })).label).toBe('미완 작업 있음');
  });

  it('판단 불가: 회색 점선 대시이고 글자도 확인 불가를 말한다', () => {
    const view = checkChipView(check({ key: 'no_background', state: 'undeterminable', reason: 'pre_collect' }));
    expect(view.glyph).toBe('dash');
    expect(view.tone).toBe('unknown');
    expect(view.label).toBe('백그라운드 작업 판단 불가');
    expect(view.ariaLabel).toContain('판단 불가');
    expect(view.ariaLabel).not.toMatch(/통과/);
  });

  it('work_done 사유를 완료처럼 표시하지 않는다', () => {
    expect(checkChipView(check({ key: 'work_done', state: 'undeterminable', reason: 'no_record' })).label).toBe('작업 내용 미기입');
    expect(checkChipView(check({ key: 'work_done', state: 'undeterminable', reason: 'no_list' })).label).toBe('작업 내용 확인 불가');
    expect(checkChipView(check({ key: 'work_done', state: 'undeterminable', reason: 'unvalidated_model' })).label).toBe('판정 검증 대기');
    expect(checkChipView(check({ key: 'work_done', state: 'undeterminable', reason: 'behind' })).label).toBe('기록 반영 대기');
  });

  it('접근성 이름은 이름과 상태를 글자로 말한다(색이 아니다)', () => {
    expect(checkChipView(check({ key: 'committed', state: 'pass' })).ariaLabel).toBe('커밋: 통과');
    expect(checkChipView(check({ key: 'committed', state: 'fail', count: 3 })).ariaLabel).toBe('커밋: 남은 것 있음 — 미커밋 파일 3');
  });
});

describe('판정 칩: 근거', () => {
  const now = DETAIL_NOW;

  it('판단 불가는 첫 줄이 상태이고 이유가 문장으로 따라온다', () => {
    const a = fullFlagship();
    const lines = checkEvidence(a, check({ key: 'no_background', state: 'undeterminable', reason: 'pre_collect', source: 'session_analysis_facts.detail.signals' }), now);
    expect(lines[0]).toBe('백그라운드 작업: 판단 불가');
    expect(lines[1]).toContain('수집 규칙 이전에 쌓인 기록');
    expect(lines[2]).toBe('출처: session_analysis_facts.detail.signals');
  });

  it('no_record와 not_applicable은 신호 이유 표에 없는 문장을 쓴다', () => {
    const a = fullFlagship();
    expect(checkEvidence(a, check({ key: 'work_done', state: 'undeterminable', reason: 'no_record' }), now)[1]).toContain('세션 기록이 없어');
    expect(checkEvidence(a, check({ key: 'committed', state: 'undeterminable', reason: 'not_applicable' }), now)[1]).toContain('이 에이전트는 이 신호를 내지 않아');
  });

  it('통과의 근거는 어느 신호가 무엇을 관측했는지 적는다', () => {
    const a = fullFlagship();
    const turn = checkEvidence(a, check({ key: 'turn_ended', state: 'pass' }), now);
    expect(turn.join('\n')).toContain('신호 "턴": 마지막 턴 끝남');
    expect(turn.join('\n')).toMatch(/마지막 턴: 요청 #14 \(\d\d:\d\d 시작\)/);
    expect(checkEvidence(a, check({ key: 'no_pending_reply', state: 'pass' }), now).join('\n')).toContain('답하지 않은 질문 없음');
    expect(checkEvidence(a, check({ key: 'committed', state: 'pass' }), now).join('\n')).toContain('이 세션의 커밋 10건');
    expect(checkEvidence(a, check({ key: 'work_done', state: 'pass' }), now).join('\n')).toContain('작업 항목 17개가 모두 완료로 표시됐습니다');
  });

  it('미완 작업의 근거는 완료로 표시되지 않은 항목의 글이다', () => {
    const a = fullFlagship();
    const record = a.record;
    if (!record || record.state !== 'ready') throw new Error('flagship record must be ready');
    const open: SessionAnalysisFull = {
      ...a,
      record: { ...record, work: record.work.map((w, i) => (i < 2 ? { ...w, done: false } : w)) },
    };
    const lines = checkEvidence(open, check({ key: 'work_done', state: 'fail', count: 2 }), now);
    expect(lines[0]).toBe('작업 내용: 남은 것 있음');
    expect(lines.filter((l) => l.startsWith('· '))).toHaveLength(2);
    expect(lines.join('\n')).toContain('알림 센터의 필터 위치와 읽음 상태를 어디에 저장할지');
  });

  it('근거 항목은 셋까지만 적고 나머지는 개수로 접는다', () => {
    const a = fullFlagship();
    const record = a.record;
    if (!record || record.state !== 'ready') throw new Error('flagship record must be ready');
    const open: SessionAnalysisFull = { ...a, record: { ...record, work: record.work.map((w) => ({ ...w, done: false })) } };
    const lines = checkEvidence(open, check({ key: 'work_done', state: 'fail', count: 17 }), now);
    expect(lines.filter((l) => l.startsWith('· '))).toHaveLength(4);
    expect(lines[lines.length - 1]).toBe('· … 외 14건');
  });

  it('턴 종료 실패의 칩 글자는 신호의 관측값으로 더 자세히 나온다', () => {
    const a = fullFlagship();
    const interrupted: SessionAnalysisFull = { ...a, signals: { ...a.signals, turn: { state: 'observed', value: 'interrupted' } } };
    expect(checkChipView(check({ key: 'turn_ended', state: 'fail' }), interrupted, now).label).toBe('턴 중단됨');
    const error: SessionAnalysisFull = { ...a, signals: { ...a.signals, turn: { state: 'observed', value: 'error' } } };
    expect(checkChipView(check({ key: 'turn_ended', state: 'fail' }), error, now).label).toBe('턴 오류로 끝남');
    // 신호는 "끝남"인데 판정이 실패인 모순은 실패 칩에 "턴 종료"라고 쓰지 않는다.
    expect(checkChipView(check({ key: 'turn_ended', state: 'fail' }), a, now).label).toBe('턴 종료 안 됨');
  });
});

describe('판정 칩 다섯', () => {
  it('응답의 순서가 달라도 정해진 순서로 놓는다(타입은 순서를 강제하지만 서버가 어기는 경우를 막는다)', () => {
    const a = fullFlagship();
    if (!a.judgement) throw new Error('flagship has a judgement');
    // 타입이 막는 모양이라 JSON을 거쳐 만든다.
    const reordered: SessionJudgement = JSON.parse(JSON.stringify({ ...a.judgement, checks: [...a.judgement.checks].reverse() }));
    expect(reordered.checks[0].key).toBe('work_done');
    const view = checkChipViews({ ...a, judgement: reordered }, DETAIL_NOW);
    expect(view.map((v) => v.key)).toEqual([...JUDGEMENT_CHECK_ORDER]);
  });

  it('합성 대표 세션의 모델 작업 판정은 검증 전 판단 불가다', () => {
    const views = checkChipViews(fullFlagship(), DETAIL_NOW);
    expect(views.map((v) => v.state)).toEqual(['pass', 'pass', 'undeterminable', 'pass', 'undeterminable']);
    expect(views[4].label).toBe('판정 검증 대기');
    expect(views[4].evidence.join('\n')).toContain('P1.5');
  });

  it('모델 작업 목록이 fail이어도 검증 전 다섯째 칩은 unvalidated_model 판단 불가다', () => {
    const a = fullFlagship();
    if (!a.judgement || !a.record || (a.record.state !== 'ready' && a.record.state !== 'stale')) throw new Error('flagship record must be ready');
    const checks: SessionJudgement['checks'] = [
      a.judgement.checks[0], a.judgement.checks[1], a.judgement.checks[2], a.judgement.checks[3],
      { ...a.judgement.checks[4], state: 'fail', count: 9 },
    ];
    const result = checkChipViews({
      ...a,
      record: { ...a.record, work: a.record.work.map((work) => ({ ...work, done: false })) },
      judgement: { ...a.judgement, checks },
    }, DETAIL_NOW).find((view) => view.key === 'work_done');
    expect(result?.state).toBe('undeterminable');
    expect(result?.label).toBe('판정 검증 대기');
    expect(result?.evidence.join('\n')).toContain('P1.5');
    expect(result?.evidence.join('\n')).not.toContain('미완 작업 9건');
  });

  it('판정이 없는 응답은 칩을 만들지 않는다', () => {
    const a = fullFlagship();
    const { judgement, ...rest } = a;
    void judgement;
    expect(checkChipViews(rest, DETAIL_NOW)).toEqual([]);
  });
});

describe('머리 상태 칩', () => {
  it('여섯 상태의 글자', () => {
    expect(['running', 'done', 'leftover', 'interrupted', 'ended_midway', 'unknown'].map((s) => statusView(s).label)).toEqual([
      '실행 중',
      '완료',
      '작업 도중',
      '중단 중',
      '도중 종료',
      '판정 불가',
    ]);
  });

  it('계약에 없는 상태는 받은 값을 그대로 보인다', () => {
    expect(statusView('paused').label).toBe('paused');
    expect(statusView('paused').tone).toBe('unknown');
  });
});

describe('목록 카드의 판정 기호', () => {
  it('다섯 기호는 이름과 상태를 문장으로 갖는다', () => {
    const row: RowJudgement = { status: 'leftover', bucket: 'leftover', checks: ['pass', 'pass', 'undeterminable', 'fail', 'fail'] };
    const glyphs = rowCheckGlyphs(row);
    expect(glyphs.map((g) => g.glyph)).toEqual(['check', 'check', 'dash', 'cross', 'cross']);
    expect(glyphs.map((g) => g.title)).toEqual(['턴 종료: 통과', '답 대기: 통과', '백그라운드 작업: 판단 불가', '커밋: 남은 것 있음', '작업 내용: 남은 것 있음']);
  });
});

describe('상태별 개수 (B3)', () => {
  const row = (id: string, judgement: RowJudgement | undefined): SessionOverview => ({
    session_id: id,
    profile_email: 'a@example.com',
    model: 'm',
    agent: 'claude',
    start_time: '2026-10-01T00:00:00Z',
    end_time: '2026-10-01T01:00:00Z',
    input_tokens: 1,
    output_tokens: 1,
    cost_usd: 1,
    event_count: 1,
    has_sync: true,
    analysis: judgement ? { state: 'ready', judgement } : { state: 'ready' },
  });
  const j = (bucket: RowJudgement['bucket']): RowJudgement => bucket === 'done'
    ? { status: 'done', bucket, checks: ['pass', 'pass', 'pass', 'pass', 'pass'] }
    : bucket === 'leftover'
      ? { status: 'leftover', bucket, checks: ['pass', 'fail', 'pass', 'pass', 'undeterminable'] }
      : bucket === 'ended_midway'
        ? { status: 'ended_midway', bucket, checks: ['fail', 'pass', 'pass', 'pass', 'undeterminable'] }
        : { status: 'unknown', bucket, checks: ['pass', 'pass', 'pass', 'pass', 'undeterminable'] };

  it('네 버킷을 세고 합이 판정이 있는 행의 수다', () => {
    const counts = bucketCounts([row('a', j('done')), row('b', j('done')), row('c', j('leftover')), row('d', j('ended_midway')), row('e', j('unknown'))]);
    expect(counts).toEqual({ done: 2, leftover: 1, ended_midway: 1, unknown: 1 });
    expect(counts.done + counts.leftover + counts.ended_midway + counts.unknown).toBe(5);
  });

  it('판정이 오지 않은 행(비소유 범위, 서버가 아직 안 보냄)은 세지 않는다', () => {
    const counts = bucketCounts([row('a', j('done')), row('b', undefined)]);
    expect(counts).toEqual({ done: 1, leftover: 0, ended_midway: 0, unknown: 0 });
  });

  it('칩의 이름과 순서는 계획의 B3와 같다', () => {
    expect(BUCKET_ORDER.map((b) => BUCKET_LABEL[b])).toEqual(['완료', '하다 만 것', '도중 종료', '판정 불가']);
  });
});
