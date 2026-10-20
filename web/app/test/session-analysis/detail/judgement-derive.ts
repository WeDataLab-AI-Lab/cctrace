/**
 * 미리보기 픽스처용 판정 계산. 서버가 할 일을 흉내 낸다 — 화면 컴포넌트는 이 계산을 하지 않고 서버가 준 값을 그리기만 한다.
 * 규칙의 원문은 `lib/session-analysis-types`의 `JudgementStatus`·`JudgementBucket`·`JudgementCheck` 주석이다.
 *
 *   turn_ended        신호 turn: ended면 pass, 다른 관측값이면 fail, 판단 불가면 그 이유
 *   no_pending_reply  신호 question: 0이면 pass, 양수면 fail(count)
 *   no_background     신호 background: 0이면 pass, 양수면 fail(count)
 *   committed         신호 uncommitted: 0이면 pass, 양수면 fail(count). 세션 중간부터 모은 0(at_least)은 판단 불가
 *   work_done         세션 기록이 없으면 판단 불가(no_record). 있으면 최상위 항목이 모두 done이면 pass,
 *                     done=false가 있으면 fail(count), 그렇지 않고 done=null이 있거나 항목이 없으면 판단 불가(no_list)
 *   신호 키가 없다(Codex)  판단 불가(not_applicable). 해당 없음을 통과로 세지 않는다.
 */

import type {
  JudgementBucket,
  JudgementCheck,
  JudgementCheckState,
  JudgementChecks,
  JudgementReason,
  JudgementStatus,
  RowJudgement,
  SessionJudgement,
  SessionRecord,
  SessionSignals,
} from '@/lib/session-analysis-types';

interface DeriveInput {
  /** 살아 있는 실행 인스턴스가 있다(관측 범주 waiting·running·idle). */
  alive: boolean;
  /** 사용자가 마지막 턴을 중단했다. 살아 있을 때만 뜻이 있다. */
  interrupted: boolean;
  signals: SessionSignals | undefined;
  record: SessionRecord | undefined;
}

const pass = (): Pick<JudgementCheck, 'state'> => ({ state: 'pass' });
const fail = (count?: number): Pick<JudgementCheck, 'state' | 'count'> => (count === undefined ? { state: 'fail' } : { state: 'fail', count });
const und = (reason: JudgementReason): Pick<JudgementCheck, 'state' | 'reason'> => ({ state: 'undeterminable', reason });

const NOT_APPLICABLE = und('not_applicable');

const turnEnded = (signals: SessionSignals | undefined): JudgementChecks[0] => {
  const s = signals?.turn;
  if (!s) return { key: 'turn_ended', ...NOT_APPLICABLE };
  if (s.state === 'undeterminable') return { key: 'turn_ended', ...und(s.reason) };
  return { key: 'turn_ended', ...(s.value === 'ended' ? pass() : fail()) };
};

const noPendingReply = (signals: SessionSignals | undefined): JudgementChecks[1] => {
  const s = signals?.question;
  if (!s) return { key: 'no_pending_reply', ...NOT_APPLICABLE };
  if (s.state === 'undeterminable') return { key: 'no_pending_reply', ...und(s.reason) };
  return { key: 'no_pending_reply', ...(s.value === 0 ? pass() : fail(s.value)) };
};

const noBackground = (signals: SessionSignals | undefined): JudgementChecks[2] => {
  const s = signals?.background;
  if (!s) return { key: 'no_background', ...NOT_APPLICABLE };
  if (s.state === 'undeterminable') return { key: 'no_background', ...und(s.reason) };
  return { key: 'no_background', ...(s.value === 0 ? pass() : fail(s.value)) };
};

const committed = (signals: SessionSignals | undefined): JudgementChecks[3] => {
  const s = signals?.uncommitted;
  if (!s) return { key: 'committed', ...NOT_APPLICABLE };
  if (s.state === 'undeterminable') return { key: 'committed', ...und(s.reason) };
  // 세션 중간부터 모은 값의 0은 "없다"가 아니라 "앞부분을 못 봤다"다(§3.18).
  if (s.at_least && s.value === 0) return { key: 'committed', ...und('pre_collect') };
  return { key: 'committed', ...(s.value === 0 ? pass() : fail(s.value)) };
};

const workDone = (record: SessionRecord | undefined): JudgementChecks[4] => {
  if (!record || (record.state !== 'ready' && record.state !== 'stale')) return { key: 'work_done', ...und('no_record') };
  const open = record.work.filter((w) => w.done === false).length;
  if (open > 0) return { key: 'work_done', ...fail(open) };
  // 항목이 하나도 없는 기록을 "모두 완료"로 세지 않는다. 비어 있다는 것은 끝났다는 증거가 아니다.
  if (record.work.length === 0 || record.work.some((w) => w.done === null)) return { key: 'work_done', ...und('no_list') };
  return { key: 'work_done', ...pass() };
};

const deriveChecks = (input: Pick<DeriveInput, 'signals' | 'record'>): JudgementChecks => [
  turnEnded(input.signals),
  noPendingReply(input.signals),
  noBackground(input.signals),
  committed(input.signals),
  workDone(input.record),
];

const states = (checks: JudgementChecks): JudgementCheckState[] => checks.map((c) => c.state);

/** "지금 끝났다고 치면 어디인가". 살아 있는지와 무관하다. */
const deriveBucket = (checks: JudgementChecks): JudgementBucket | null => {
  const [turn, ...others] = checks;
  if (turn.state === 'fail') return 'ended_midway';
  if (others.some((c) => c.state === 'fail')) return 'leftover';
  return checks.every((check) => check.state === 'pass') ? 'done' : null;
};

const deriveStatus = (input: DeriveInput, checks: JudgementChecks): JudgementStatus => {
  if (input.alive) return input.interrupted ? 'interrupted' : 'running';
  const turn = checks[0];
  if (turn.state === 'undeterminable') return 'unknown';
  if (turn.state === 'fail') return 'ended_midway';
  if (states(checks).includes('fail')) return 'leftover';
  return checks.every((check) => check.state === 'pass') ? 'done' : 'unknown';
};

const deriveJudgement = (input: DeriveInput): SessionJudgement => {
  const checks = deriveChecks(input);
  return { status: deriveStatus(input, checks), bucket: deriveBucket(checks), checks };
};

/** 목록 행에는 상태와 다섯 판정의 상태만 싣는다. */
const rowJudgementOf = (judgement: SessionJudgement): RowJudgement => {
  const [a, b, c, d, e] = judgement.checks;
  return { status: judgement.status, bucket: judgement.bucket, checks: [a.state, b.state, c.state, d.state, e.state] };
};

export { deriveChecks, deriveBucket, deriveStatus, deriveJudgement, rowJudgementOf };
export type { DeriveInput };
