/**
 * 작업 완료 판정(D8)의 순수 로직: 칩 다섯의 글자와 모양, 근거 문장, 머리 상태 칩, 상태별 개수.
 *
 * 판정은 평가가 아니라 관측이다(#682, #666). "닫아도 된다"는 결론을 만들지 않고, 칩마다 근거(어느 신호, 어느 항목)를 툴팁으로
 * 붙인다. 판단 불가는 통과가 아니다: 회색 점선 칩이고 이유를 적는다. 모델 작업 판정은 운영 검증 기준 전까지 판단 불가로 낮춘다.
 * 나머지 판정 규칙은 서버가 정한다.
 */

import type {
  JudgementBucket,
  JudgementCheck,
  JudgementCheckKey,
  JudgementCheckState,
  JudgementReason,
  JudgementStatus,
  RowJudgement,
  SessionAnalysisFull,
  SessionBucketCounts,
  TurnSignalValue,
} from '@/lib/session-analysis-types';
import type { SessionOverview } from '@/lib/types';
import { clip, formatClockNear } from './format';
import { reasonText, signalLines, type SignalKey } from './signals';
import { WORK_DONE_QUALITY_GATE_PASSED } from './work-items';

/** 칩은 이 순서로 정확히 다섯 개다. */
const JUDGEMENT_CHECK_ORDER: readonly JudgementCheckKey[] = [
  'turn_ended',
  'no_pending_reply',
  'no_background',
  'committed',
  'work_done',
];

/** 통과했을 때의 칩 글자. 판단 불가 칩도 같은 글자를 흐리게 쓴다(레퍼런스의 "− 裏の作業なし"). */
const CHECK_PASS_LABEL: Record<JudgementCheckKey, string> = {
  turn_ended: '턴 종료',
  no_pending_reply: '답 대기 없음',
  no_background: '백그라운드 작업 없음',
  committed: '커밋됨',
  work_done: '작업 내용 완료',
};

/** 접근성 이름과 목록 카드 기호의 툴팁에 쓰는 짧은 이름. */
const CHECK_NAME: Record<JudgementCheckKey, string> = {
  turn_ended: '턴 종료',
  no_pending_reply: '답 대기',
  no_background: '백그라운드 작업',
  committed: '커밋',
  work_done: '작업 내용',
};

const CHECK_UNKNOWN_LABEL: Record<JudgementCheckKey, string> = {
  turn_ended: '턴 종료 확인 불가',
  no_pending_reply: '답 대기 확인 불가',
  no_background: '백그라운드 작업 판단 불가',
  committed: '커밋 여부 판단 불가',
  work_done: '작업 내용 판단 불가',
};

const CHECK_STATE_WORD: Record<JudgementCheckState, string> = {
  pass: '통과',
  fail: '남은 것 있음',
  undeterminable: '판단 불가',
};

/** 신호 줄에서 가져오는 근거의 키. `work_done`은 신호가 아니라 세션 기록이다. */
const SIGNAL_OF: Record<Exclude<JudgementCheckKey, 'work_done'>, SignalKey> = {
  turn_ended: 'turn',
  no_pending_reply: 'question',
  no_background: 'background',
  committed: 'uncommitted',
};

/** 신호 이유에 없는 두 이유와 `work_done`의 이유 문장. */
const EXTRA_REASON_TEXT: Record<'no_record' | 'not_applicable', string> = {
  no_record: '세션 기록이 없어 작업 내용을 판단할 수 없습니다(작업 내용 미기입). 기록이 없다고 작업이 끝나지 않았다는 뜻은 아닙니다.',
  not_applicable: '이 에이전트는 이 신호를 내지 않아 판단할 수 없습니다. 통과로 세지 않습니다.',
};

const WORK_REASON_TEXT: Partial<Record<JudgementReason, string>> = {
  no_list: '세션 기록에 작업 항목이 없거나 완료 여부를 확인하지 못한 항목이 있어 판단할 수 없습니다. 미완이라고 단정하지도 않습니다.',
  withheld: '세션 기록을 서버에 올리지 않아(보류됨) 작업 내용을 판단할 수 없습니다.',
  redacted: '비식별화로 대화 본문이 가려져 작업 내용을 판단할 수 없습니다.',
  behind: '세션 기록이 최신 요청 턴까지 반영하지 않아 작업 완료를 판단할 수 없습니다.',
  unvalidated_model: '모델의 작업 완료 판정이 사람 평가 검증 기준을 통과하지 못해 판단 불가로 남았습니다.',
};

const WORK_UNKNOWN_LABEL: Partial<Record<JudgementReason, string>> = {
  no_record: '작업 내용 미기입',
  no_list: '작업 내용 확인 불가',
  withheld: '작업 내용 보류',
  redacted: '작업 내용 가려짐',
  behind: '기록 반영 대기',
  unvalidated_model: '판정 검증 대기',
};

const reasonSentence = (key: JudgementCheckKey, reason: JudgementReason | undefined): string => {
  if (reason === 'no_record' || reason === 'not_applicable') return EXTRA_REASON_TEXT[reason];
  if (key === 'work_done') return (reason ? WORK_REASON_TEXT[reason] : undefined) ?? '이 세션에서는 필요한 값이 없어 작업 내용을 판단할 수 없습니다.';
  if (reason === 'behind') return '세션 기록이 최신 요청 턴을 포함하지 않아 현재 작업 완료를 판정할 수 없습니다.';
  if (reason === 'unvalidated_model') return '모델의 작업 완료 판정이 아직 검증 기준을 통과하지 못했습니다.';
  return reasonText(SIGNAL_OF[key], reason);
};

/** 턴 신호의 관측값별 칩 글자. 계약상 `turn_ended`의 fail은 건수도 이유도 없으므로 신호에서 더 자세히 읽는다. */
const TURN_FAIL_LABEL: Record<TurnSignalValue, string> = {
  ended: '턴 종료',
  open: '턴 진행 중',
  interrupted: '턴 중단됨',
  error: '턴 오류로 끝남',
  no_response: '응답 없음',
};

type CheckTone = 'pass' | 'fail' | 'unknown';
type CheckGlyph = 'check' | 'cross' | 'dash';

interface CheckChipView {
  key: JudgementCheckKey;
  state: JudgementCheckState;
  tone: CheckTone;
  glyph: CheckGlyph;
  /** 칩에 보이는 글자: "턴 종료", "미완 작업 1건", "작업 내용 미기입". */
  label: string;
  /** 스크린리더가 읽는 이름: "작업 내용: 남은 것 있음 — 미완 작업 1건". */
  ariaLabel: string;
  /** 툴팁. 첫 줄이 상태, 나머지가 근거(어느 신호, 어느 항목)다. */
  evidence: string[];
}

const GLYPH: Record<JudgementCheckState, CheckGlyph> = { pass: 'check', fail: 'cross', undeterminable: 'dash' };
const TONE: Record<JudgementCheckState, CheckTone> = { pass: 'pass', fail: 'fail', undeterminable: 'unknown' };

const turnSignalValue = (analysis: SessionAnalysisFull | undefined): TurnSignalValue | null => {
  const turn = analysis?.signals?.turn;
  return turn && turn.state === 'observed' ? turn.value : null;
};

const failLabel = (check: JudgementCheck, analysis: SessionAnalysisFull | undefined): string => {
  const count = check.count;
  switch (check.key) {
    case 'turn_ended': {
      // 신호가 "끝남"인데 판정이 실패라면 서버의 두 값이 어긋난 것이다. 실패 칩에 "턴 종료"라고 쓰지 않는다.
      const value = turnSignalValue(analysis);
      return value && value !== 'ended' ? (TURN_FAIL_LABEL[value] ?? '턴 종료 안 됨') : '턴 종료 안 됨';
    }
    case 'no_pending_reply':
      return typeof count === 'number' ? `미답 질문 ${count}건` : '미답 질문 있음';
    case 'no_background':
      return typeof count === 'number' ? `백그라운드 작업 ${count}건` : '백그라운드 작업 있음';
    case 'committed':
      return typeof count === 'number' ? `미커밋 파일 ${count}` : '미커밋 파일 있음';
    case 'work_done':
      return typeof count === 'number' ? `미완 작업 ${count}건` : '미완 작업 있음';
  }
};

const checkLabel = (check: JudgementCheck, analysis: SessionAnalysisFull | undefined): string => {
  switch (check.state) {
    case 'pass':
      return CHECK_PASS_LABEL[check.key];
    case 'fail':
      return failLabel(check, analysis);
    case 'undeterminable':
      return check.key === 'work_done'
        ? (check.reason ? WORK_UNKNOWN_LABEL[check.reason] : undefined) ?? CHECK_UNKNOWN_LABEL.work_done
        : CHECK_UNKNOWN_LABEL[check.key];
  }
};

/* ---------------- 근거 문장 ---------------- */

const signalSentence = (analysis: SessionAnalysisFull, key: SignalKey, now: number): string | null => {
  const line = signalLines(analysis.signals, { now }).find((l) => l.key === key);
  return line ? line.sentence : null;
};

const lastTurnLine = (analysis: SessionAnalysisFull, now: number): string | null => {
  const last = analysis.turns[analysis.turns.length - 1];
  if (!last) return null;
  const who = last.request_no != null ? `요청 #${last.request_no}` : `턴 ${last.no}`;
  return `마지막 턴: ${who} (${formatClockNear(last.started_at, now)} 시작)`;
};

/** 답하지 않은 질문의 머리글. 질문 하나(도구 호출)에 항목이 여럿이면 `/`로 잇는다. */
const unansweredHeaders = (analysis: SessionAnalysisFull): string[] =>
  analysis.questions.filter((q) => q.status === 'unanswered').map((q) => q.items.map((i) => i.header).join(' / '));

const openWorkTexts = (analysis: SessionAnalysisFull): string[] => {
  const record = analysis.record;
  if (!record || (record.state !== 'ready' && record.state !== 'stale')) return [];
  return record.work.filter((w) => w.done === false).map((w) => w.text);
};

const MAX_EVIDENCE_ITEMS = 3;

const listLine = (prefix: string, items: readonly string[]): string[] =>
  items.slice(0, MAX_EVIDENCE_ITEMS).map((t) => `${prefix}${clip(t, 56)}`).concat(
    items.length > MAX_EVIDENCE_ITEMS ? [`${prefix}… 외 ${items.length - MAX_EVIDENCE_ITEMS}건`] : [],
  );

/**
 * 칩 하나의 근거 줄. 첫 줄은 상태 문장이고, 이어서 어느 신호·어느 항목이 그 상태를 만들었는지 적는다.
 * 응답에 근거로 쓸 값이 없으면(예: 신호가 없다) 있는 만큼만 적는다. 근거를 지어내지 않는다.
 */
const checkEvidence = (analysis: SessionAnalysisFull, check: JudgementCheck, now: number): string[] => {
  const head = `${CHECK_NAME[check.key]}: ${CHECK_STATE_WORD[check.state]}`;
  const lines: string[] = [head];

  if (check.state === 'undeterminable') {
    lines.push(reasonSentence(check.key, check.reason));
    lines.push(check.source ? `출처: ${check.source}` : '출처: 서버가 제공하지 않았습니다.');
    return lines;
  }

  switch (check.key) {
    case 'turn_ended': {
      const sentence = signalSentence(analysis, 'turn', now);
      if (sentence) lines.push(`신호 "턴": ${sentence}`);
      const last = lastTurnLine(analysis, now);
      if (last) lines.push(last);
      break;
    }
    case 'no_pending_reply': {
      const sentence = signalSentence(analysis, 'question', now);
      if (sentence) lines.push(`신호 "질문": ${sentence}`);
      if (check.state === 'fail') lines.push(...listLine('· ', unansweredHeaders(analysis)));
      break;
    }
    case 'no_background': {
      const sentence = signalSentence(analysis, 'background', now);
      if (sentence) lines.push(`신호 "백그라운드": ${sentence}`);
      const bg = analysis.signals?.background;
      if (check.state === 'fail' && bg && bg.state === 'observed' && bg.parts) {
        const parts = Object.entries(bg.parts).map(([k, v]) => `${k} ${v}`);
        if (parts.length > 0) lines.push(`· ${parts.join(' · ')}`);
      }
      break;
    }
    case 'committed': {
      const sentence = signalSentence(analysis, 'uncommitted', now);
      if (sentence) lines.push(`신호 "미커밋": ${sentence}`);
      const commits = analysis.commits;
      if (check.state === 'pass' && commits && commits.state === 'observed' && typeof commits.total === 'number') {
        lines.push(`이 세션의 커밋 ${commits.total}건`);
      }
      break;
    }
    case 'work_done': {
      const record = analysis.record;
      if (check.state === 'fail') {
        lines.push('세션 기록의 작업 항목 중 완료로 표시되지 않은 것:');
        lines.push(...listLine('· ', openWorkTexts(analysis)));
      } else if (record && (record.state === 'ready' || record.state === 'stale')) {
        lines.push(`세션 기록의 작업 항목 ${record.work.length}개가 모두 완료로 표시됐습니다.`);
      }
      break;
    }
  }
  return lines;
};

const checkChipView = (check: JudgementCheck, analysis?: SessionAnalysisFull, now = 0): CheckChipView => {
  const label = checkLabel(check, analysis);
  const evidence = analysis ? checkEvidence(analysis, check, now) : [`${CHECK_NAME[check.key]}: ${CHECK_STATE_WORD[check.state]}`];
  if (!analysis && check.state === 'undeterminable') {
    evidence.push(reasonSentence(check.key, check.reason));
    evidence.push(check.source ? `출처: ${check.source}` : '출처: 서버가 제공하지 않았습니다.');
  }
  return {
    key: check.key,
    state: check.state,
    tone: TONE[check.state],
    glyph: GLYPH[check.state],
    label,
    ariaLabel: `${CHECK_NAME[check.key]}: ${CHECK_STATE_WORD[check.state]}${check.state === 'pass' ? '' : ` — ${label}`}`,
    evidence,
  };
};

/** 판정 칩 다섯. 응답의 순서가 계약과 다르더라도 항상 정해진 순서로 놓는다. */
const checkChipViews = (analysis: SessionAnalysisFull, now: number): CheckChipView[] => {
  const judgement = analysis.judgement;
  if (!judgement) return [];
  const byKey = new Map(judgement.checks.map((c) => [c.key, c] as const));
  const views: CheckChipView[] = [];
  for (const key of JUDGEMENT_CHECK_ORDER) {
    const check = byKey.get(key);
    if (!check) continue;
    const record = analysis.record;
    const workCheck = key === 'work_done' && record && (record.state === 'ready' || record.state === 'stale') && !WORK_DONE_QUALITY_GATE_PASSED
      ? { ...check, state: 'undeterminable' as const, reason: 'unvalidated_model' as const, count: undefined, source: 'P1.5 사람 평가 검증 기준 미통과' }
      : check;
    views.push(checkChipView(workCheck, analysis, now));
  }
  return views;
};

/* ---------------- 머리 상태 칩 (D2) ---------------- */

type StatusTone = 'live' | 'success' | 'warning' | 'danger' | 'unknown';

interface StatusView {
  label: string;
  tone: StatusTone;
  hint: string;
}

const STATUS_VIEW: Record<JudgementStatus, StatusView> = {
  running: { label: '실행 중', tone: 'live', hint: '살아 있는 실행 인스턴스가 있는 세션입니다. 아래 판정은 지금까지 관측한 값입니다.' },
  done: { label: '완료', tone: 'success', hint: '다섯 판정이 모두 통과한 경우에만 완료로 셉니다.' },
  leftover: { label: '작업 도중', tone: 'warning', hint: '턴은 끝났지만 남은 것이 있습니다(답하지 않은 질문, 백그라운드 작업, 미커밋 파일, 미완 작업 중 하나 이상).' },
  interrupted: { label: '중단 중', tone: 'warning', hint: '살아 있는 세션이고 사용자가 마지막 턴을 중단했습니다.' },
  ended_midway: { label: '도중 종료', tone: 'danger', hint: '턴이 끝나기 전에 세션이 멈췄습니다(중단, 오류, 응답 없음, 진행 중인 채로 종료).' },
  unknown: { label: '판정 불가', tone: 'unknown', hint: '턴이 끝났는지 판단할 수 없어 상태를 정하지 않았습니다.' },
};

const statusView = (status: string): StatusView =>
  (STATUS_VIEW as Record<string, StatusView | undefined>)[status] ?? { label: status, tone: 'unknown', hint: '이 화면이 아직 모르는 상태입니다.' };

/* ---------------- 목록 카드: 기호만 ---------------- */

interface RowCheckGlyph {
  key: JudgementCheckKey;
  state: JudgementCheckState;
  glyph: CheckGlyph;
  tone: CheckTone;
  /** "턴 종료: 통과". 카드의 기호는 글자가 없으므로 이 문장이 이름이다. */
  title: string;
  description: string;
}

const rowCheckGlyphs = (judgement: RowJudgement): RowCheckGlyph[] =>
  JUDGEMENT_CHECK_ORDER.map((key, i) => {
    const state = judgement.checks[i] ?? 'undeterminable';
    return {
      key, state, glyph: GLYPH[state], tone: TONE[state],
      title: `${CHECK_NAME[key]}: ${CHECK_STATE_WORD[state]}`,
      description: checkLabel({ key, state }, undefined),
    };
  });

/* ---------------- 상태별 개수 (B3) ---------------- */

const BUCKET_LABEL: Record<JudgementBucket, string> = { done: '완료', leftover: '하다 만 것', ended_midway: '도중 종료', unknown: '판정 불가' };
const BUCKET_ORDER: readonly JudgementBucket[] = ['done', 'leftover', 'ended_midway', 'unknown'];

const BUCKET_HINT: Record<JudgementBucket, string> = {
  done: '다섯 판정이 모두 통과한 세션',
  leftover: '턴은 끝났지만 남은 것이 있는 세션',
  ended_midway: '턴이 끝나기 전에 멈춘 세션',
  unknown: '판정 자료가 없거나 불완전해 완료 여부를 알 수 없는 세션',
};

/**
 * 행에서 센 개수. 판정이 없는 행(비소유 범위, 서버가 아직 안 보냄)은 세지 않는다 — "판정 못 함(unknown)"과 다르다.
 * 네 범주의 합은 판정이 있는 행의 수다.
 */
const bucketCounts = (rows: readonly SessionOverview[]): SessionBucketCounts => {
  const counts: SessionBucketCounts = { done: 0, leftover: 0, ended_midway: 0, unknown: 0 };
  for (const row of rows) {
    const judgement = row.analysis?.judgement;
    if (!judgement) continue;
    counts[judgement.bucket ?? 'unknown'] += 1;
  }
  return counts;
};

const JUDGEMENT_CAPTION = '관측한 신호와 세션 기록으로 보인 판정입니다. 생산성이나 소요 시간을 평가하지 않고, 판단 불가는 통과로 세지 않습니다.';

export {
  JUDGEMENT_CHECK_ORDER,
  CHECK_PASS_LABEL,
  CHECK_NAME,
  CHECK_STATE_WORD,
  JUDGEMENT_CAPTION,
  BUCKET_LABEL,
  BUCKET_ORDER,
  BUCKET_HINT,
  checkChipView,
  checkChipViews,
  checkEvidence,
  statusView,
  rowCheckGlyphs,
  bucketCounts,
};
export type { CheckChipView, CheckTone, CheckGlyph, StatusView, StatusTone, RowCheckGlyph };
